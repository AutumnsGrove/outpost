package pack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"outpost/internal/r2"
)

// memStore is the slice of R2 the manager reads and writes, in memory.
type memStore struct{ objects map[string][]byte }

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, _, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objects[key] = b
	return nil
}

func (m *memStore) Get(_ context.Context, _, key string) ([]byte, error) {
	b, ok := m.objects[key]
	if !ok {
		return nil, r2.ErrNotFound
	}
	return b, nil
}

func (m *memStore) Stat(_ context.Context, _, key string) (r2.Info, error) {
	b, ok := m.objects[key]
	if !ok {
		return r2.Info{}, r2.ErrNotFound
	}
	return r2.Info{Size: int64(len(b))}, nil
}

func (m *memStore) List(_ context.Context, _, prefix string) ([]r2.Object, error) {
	var out []r2.Object
	for k, v := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, r2.Object{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func (m *memStore) Remove(_ context.Context, _, key string) error {
	delete(m.objects, key)
	return nil
}

func newManager(t *testing.T) (*Manager, *memStore) {
	t.Helper()
	store := newMemStore()
	return &Manager{
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
		Dir:         t.TempDir(),
		Store:       store,
		Log:         testLogger(),
	}, store
}

func TestProfilesSeesBothBucketsAndTheActiveOne(t *testing.T) {
	m, store := newManager(t)
	store.objects["profiles/cobblemon/pack.json"] = []byte(`{"slug":"cobblemon","name":"Cobblemon","versionId":"v1"}`)
	store.objects["profiles/cobblemon/mods/a.jar"] = []byte("jar")
	store.objects["profiles/cobblemon/mods/b.jar"] = []byte("jar")
	store.objects["profiles/cobblemon/world.tar.gz"] = []byte("world")
	store.objects["profiles/cobblemon/pack.json"] = []byte(`{"slug":"cobblemon","name":"Cobblemon","versionId":"v1"}`)
	// A profile that only has a world (installed assets were removed by hand).
	store.objects["profiles/orphan/world.tar.gz"] = []byte("world")

	profiles, err := m.Profiles(context.Background(), "cobblemon")
	if err != nil {
		t.Fatalf("profiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles = %+v", profiles)
	}

	byID := map[string]Profile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}
	cob := byID["cobblemon"]
	if !cob.Active || cob.Mods != 2 || !cob.HasWorld || cob.Pack == nil || cob.Pack.Name != "Cobblemon" {
		t.Fatalf("cobblemon = %+v", cob)
	}
	if byID["orphan"].Active || byID["orphan"].Pack != nil || !byID["orphan"].HasWorld {
		t.Fatalf("orphan = %+v", byID["orphan"])
	}
}

func TestTheProfileComesFromTheLinkWhenNobodyNamesOne(t *testing.T) {
	m, _ := newManager(t)
	for src, want := range map[string]string{
		"https://modrinth.com/modpack/cobblemon-fabric": "cobblemon-fabric",
		"cobblemon-fabric": "cobblemon-fabric",
		"https://cdn.modrinth.com/data/x/versions/y/My Pack.mrpack": "my-pack",
	} {
		got, err := m.profileFor(src, "")
		if err != nil {
			t.Fatalf("profileFor(%q): %v", src, err)
		}
		if got != want {
			t.Errorf("profileFor(%q) = %q, want %q", src, got, want)
		}
	}

	// A name that is not one is refused before anything is downloaded.
	if _, err := m.profileFor("cobblemon-fabric", "../etc"); err == nil {
		t.Fatal("a path was accepted as a profile")
	}
}

func TestOneJobAtATime(t *testing.T) {
	m, _ := newManager(t)

	first, err := m.begin("preview", "cobblemon", "cobblemon")
	if err != nil {
		t.Fatalf("first job: %v", err)
	}
	if _, err := m.begin("install", "cobblemon", "cobblemon"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second job = %v, want ErrBusy", err)
	}

	// Finishing it frees the slot.
	m.run(context.Background(), first, func(context.Context) (*JobResult, error) {
		return &JobResult{Profile: "cobblemon"}, nil
	})
	deadline := time.Now().Add(2 * time.Second)
	for m.Job().running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := m.begin("preview", "again", "cobblemon"); err != nil {
		t.Fatalf("the slot was not freed: %v", err)
	}
}

func TestAFailedJobSaysWhy(t *testing.T) {
	m, _ := newManager(t)
	job, err := m.begin("install", "cobblemon", "cobblemon")
	if err != nil {
		t.Fatal(err)
	}
	m.run(context.Background(), job, func(context.Context) (*JobResult, error) {
		return nil, errors.New("modrinth has no such project")
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := m.Job()
		if got.Phase == "failed" {
			if !strings.Contains(got.Error, "no such project") {
				t.Fatalf("error = %q", got.Error)
			}
			if got.EndedAt == "" {
				t.Fatal("a failed job should record when it ended")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the job never reported a failure")
}

// testLogger is a quiet logger for the manager's own noise.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAnInstallMarksItselfSoAStartWaits(t *testing.T) {
	m, _ := newManager(t)

	if _, _, busy := InstallingNow(m.Dir); busy {
		t.Fatal("nothing is installing yet")
	}

	job, err := m.begin("install", "cobblemon", "cobblemon")
	if err != nil {
		t.Fatal(err)
	}
	clear, err := MarkInstalling(m.Dir, "cobblemon")
	if err != nil {
		t.Fatal(err)
	}
	profile, _, busy := InstallingNow(m.Dir)
	if !busy || profile != "cobblemon" {
		t.Fatalf("marker = %q %v", profile, busy)
	}

	clear()
	m.run(context.Background(), job, func(context.Context) (*JobResult, error) { return nil, nil })
	if _, _, busy := InstallingNow(m.Dir); busy {
		t.Fatal("the marker outlived the install")
	}
}

func TestAStaleInstallMarkerDoesNotBlockAStart(t *testing.T) {
	m, _ := newManager(t)
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A daemon killed mid-install leaves the marker behind. An hour later it must
	// not stop a rental for ever.
	old := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(m.Dir, installMarker), []byte("cobblemon\n"+old+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, busy := InstallingNow(m.Dir); busy {
		t.Fatal("a stale marker blocked a start")
	}
}
