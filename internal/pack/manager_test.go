package pack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"outpost/internal/publish"
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

func TestTheInstallMarkerLivesExactlyAsLongAsTheJob(t *testing.T) {
	m, _ := newManager(t)

	if _, _, busy := InstallingNow(m.Dir); busy {
		t.Fatal("nothing is installing yet")
	}

	// run owns the marker for install jobs, so this exercises the same path the
	// page takes — deleting the marker logic leaves this failing.
	job, err := m.begin("install", "cobblemon", "cobblemon")
	if err != nil {
		t.Fatal(err)
	}

	var during string
	var busyDuring bool
	m.run(context.Background(), job, func(context.Context) (*JobResult, error) {
		during, _, busyDuring = InstallingNow(m.Dir)
		return &JobResult{}, nil
	})

	deadline := time.Now().Add(2 * time.Second)
	for m.Job().running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !busyDuring || during != "cobblemon" {
		t.Fatalf("the marker was not there during the install: %q %v", during, busyDuring)
	}
	if _, _, busy := InstallingNow(m.Dir); busy {
		t.Fatal("the marker outlived the install")
	}
}

func TestAProgressTickKeepsTheMarkerFresh(t *testing.T) {
	m, _ := newManager(t)
	clear, touch, err := MarkInstalling(m.Dir, "cobblemon")
	if err != nil {
		t.Fatal(err)
	}
	defer clear()

	// Backdate it, then refresh: freshness is silence, not duration.
	old := time.Now().Add(-2 * staleInstall)
	path := filepath.Join(m.Dir, installMarker)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, busy := InstallingNow(m.Dir); busy {
		t.Fatal("a marker with no progress for two hours was trusted")
	}
	touch()
	if _, _, busy := InstallingNow(m.Dir); !busy {
		t.Fatal("a refreshed marker was not trusted")
	}
}

func TestAStaleInstallMarkerDoesNotBlockAStart(t *testing.T) {
	m, _ := newManager(t)
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A daemon killed mid-install leaves the marker behind. Later it must not stop
	// a rental for ever.
	path := filepath.Join(m.Dir, installMarker)
	if err := os.WriteFile(path, []byte("cobblemon\n"+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, busy := InstallingNow(m.Dir); busy {
		t.Fatal("a stale marker blocked a start")
	}
}

func TestProgressRefreshesTheInstallMarker(t *testing.T) {
	m, _ := newManager(t)
	job, err := m.begin("install", "cobblemon", "cobblemon")
	if err != nil {
		t.Fatal(err)
	}

	// Both halves of an install report progress, and each report has to keep the
	// marker fresh: that is what stops a slow install from dropping the guard that
	// keeps a rental from landing mid-rewrite. The assertions run inside the job
	// goroutine, so failures come back through a channel rather than t.Fatal.
	done := make(chan error, 1)
	m.run(context.Background(), job, func(context.Context) (*JobResult, error) {
		path := filepath.Join(m.Dir, installMarker)
		backdate := func() error {
			old := time.Now().Add(-2 * staleInstall)
			if err := os.Chtimes(path, old, old); err != nil {
				return err
			}
			if _, _, busy := InstallingNow(m.Dir); busy {
				return errors.New("a two-hour-old marker was trusted")
			}
			return nil
		}

		if err := backdate(); err != nil {
			return nil, err
		}
		m.progress(job)("files", 1, 2) // the fetch callback
		if _, _, busy := InstallingNow(m.Dir); !busy {
			return nil, errors.New("the fetch progress callback did not refresh the marker")
		}

		if err := backdate(); err != nil {
			return nil, err
		}
		m.uploadProgress(job)(1, 2) // the publish callback
		if _, _, busy := InstallingNow(m.Dir); !busy {
			return nil, errors.New("the upload progress callback did not refresh the marker")
		}
		return &JobResult{}, nil
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := m.Job()
		if !got.running {
			if got.Error != "" {
				done <- errors.New(got.Error)
			} else {
				done <- nil
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the job never finished")
	}
}

func TestVersionsSaysWhatThisServerCanRun(t *testing.T) {
	m, store := newManager(t)

	// The fake Modrinth answers with four versions spanning three game versions.
	srv := newFakeModrinth(t, []Version{
		{ID: "v4", VersionNumber: "15.0.0-beta.4", VersionType: "beta", GameVersions: []string{"26.3"}, Loaders: []string{"fabric"}},
		{ID: "v3", VersionNumber: "14.1.2", VersionType: "release", GameVersions: []string{"26.2"}, Loaders: []string{"fabric"}},
		{ID: "v2", VersionNumber: "5.4.1", VersionType: "alpha", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"}},
		{ID: "v1", VersionNumber: "5.2.8", VersionType: "release", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"}},
		{ID: "v0", VersionNumber: "0.9.0", VersionType: "release", GameVersions: []string{"1.20.1"}, Loaders: []string{"forge"}},
	})
	_ = srv
	store.objects["profiles/test-pack/pack.json"] = []byte(`{"slug":"test-pack","versionId":"v1"}`)

	out, err := m.Versions(context.Background(), "https://modrinth.com/modpack/test-pack", "")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if out.Total != 5 || len(out.Choices) != 5 {
		t.Fatalf("choices = %d of %d", len(out.Choices), out.Total)
	}
	if out.Installed != "v1" {
		t.Fatalf("installed = %q, want v1", out.Installed)
	}
	if out.Newer != "5.4.1" {
		t.Fatalf("newer = %q, want the newest bootable one that is not installed", out.Newer)
	}

	byNumber := map[string]VersionChoice{}
	for _, c := range out.Choices {
		byNumber[c.Number] = c
	}
	if !byNumber["5.2.8"].Bootable || !byNumber["5.2.8"].Installed {
		t.Fatalf("5.2.8 = %+v", byNumber["5.2.8"])
	}
	if !byNumber["5.4.1"].Bootable {
		t.Fatalf("an alpha for the supported Minecraft version is still runnable: %+v", byNumber["5.4.1"])
	}
	// The reason has to name the blocker, not just say no.
	if c := byNumber["14.1.2"]; c.Bootable || !strings.Contains(c.Reason, "Java 17") {
		t.Fatalf("14.1.2 = %+v", c)
	}
	if c := byNumber["0.9.0"]; c.Bootable || !strings.Contains(c.Reason, "Fabric") {
		t.Fatalf("0.9.0 = %+v", c)
	}
}

func TestVersionsFindsTheRunnableOnesUnderHundredsOfNewerOnes(t *testing.T) {
	m, store := newManager(t)

	// Fabulously Optimized as it actually is: 300 versions for game versions this
	// server cannot run, then the Fabric/1.20.1 line. A plain "newest 40" window
	// would show forty grey rows and nothing selectable.
	var versions []Version
	for i := 0; i < 300; i++ {
		versions = append(versions, Version{
			ID: fmt.Sprintf("new-%d", i), VersionNumber: fmt.Sprintf("90.%d.0", 300-i),
			VersionType: "release", GameVersions: []string{"26.2"}, Loaders: []string{"fabric"},
		})
	}
	for i := 0; i < 5; i++ {
		versions = append(versions, Version{
			ID: fmt.Sprintf("old-%d", i), VersionNumber: fmt.Sprintf("5.%d.0", 5-i),
			VersionType: "release", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"},
		})
	}
	newFakeModrinth(t, versions)
	store.objects["profiles/test-pack/pack.json"] = []byte(`{"slug":"test-pack","versionId":"old-1"}`)

	out, err := m.Versions(context.Background(), "test-pack", "")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}

	runnable := 0
	for _, c := range out.Choices {
		if c.Bootable {
			runnable++
		}
	}
	if runnable != 5 {
		t.Fatalf("runnable rows = %d, want all five: %+v", runnable, out.Choices)
	}
	if out.Skipped != 300-1 {
		t.Fatalf("skipped = %d, want the unrunable ones beyond the newest per game version", out.Skipped)
	}
	// The installed one is 5.4.0 and the newest runnable is 5.5.0, so there is an
	// update; from the newest runnable itself there would not be.
	if out.Newer != "5.5.0" {
		t.Fatalf("newer = %q, want 5.5.0", out.Newer)
	}
}

func TestVersionsDoesNotCallAnOlderVersionAnUpdate(t *testing.T) {
	m, store := newManager(t)
	newFakeModrinth(t, []Version{
		{ID: "newest", VersionNumber: "5.5.0", VersionType: "release", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"}},
		{ID: "older", VersionNumber: "5.4.0", VersionType: "release", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"}},
	})
	// The profile already holds the newest runnable one.
	store.objects["profiles/test-pack/pack.json"] = []byte(`{"slug":"test-pack","versionId":"newest"}`)

	out, err := m.Versions(context.Background(), "test-pack", "")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if out.Newer != "" {
		t.Fatalf("newer = %q: an older version is a downgrade, not an update", out.Newer)
	}
}

func TestVersionsRefusesASourceThatNamesOneVersion(t *testing.T) {
	m, _ := newManager(t)
	_, err := m.Versions(context.Background(), "https://cdn.modrinth.com/data/x/versions/y/Pack.mrpack", "")
	if !errors.Is(err, ErrNoVersions) {
		t.Fatalf("err = %v, want ErrNoVersions", err)
	}
}

func TestEveryPathNamesAProfileByTheSameRule(t *testing.T) {
	m, _ := newManager(t)
	newFakeModrinth(t, []Version{
		{ID: "v1", VersionNumber: "1.0.0", VersionType: "release", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"}},
	})

	// A slug with characters the two derivations used to disagree about, and one
	// long enough to need truncating.
	for _, slug := range []string{"cobblemon_fabric", strings.Repeat("a", 40)} {
		src := "https://modrinth.com/modpack/" + slug
		fromSource, err := m.profileFor(src, "")
		if err != nil {
			t.Fatalf("profileFor(%q): %v", src, err)
		}
		listed, err := m.Versions(context.Background(), src, "")
		if err != nil {
			t.Fatalf("versions(%q): %v", src, err)
		}
		if listed.Profile != fromSource {
			t.Errorf("slug %q: picker says %q, install says %q — one of them reads the wrong profile",
				slug, listed.Profile, fromSource)
		}
		if err := publish.ValidateProfile(listed.Profile); err != nil {
			t.Errorf("slug %q produced an invalid profile %q: %v", slug, listed.Profile, err)
		}
	}
}

func TestInstallVanillaLandsInTheVanillaProfile(t *testing.T) {
	m, store := newManager(t)
	// No player list in the bucket: the install must stop before it fetches a
	// launcher, because a server nobody can join is not worth renting.
	store.objects["profiles/other/mods/a.jar"] = []byte("jar")

	job, err := m.InstallVanilla(context.Background(), VanillaRequest{Activate: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if job.Profile != "vanilla" {
		t.Fatalf("profile = %q, want the pack's own slug", job.Profile)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := m.Job()
		if got.Phase == "failed" {
			if !strings.Contains(got.Error, "player list is empty") {
				t.Fatalf("error = %q, want the roster refusal", got.Error)
			}
			if _, err := store.Get(context.Background(), "", "profiles/vanilla/pack.json"); err == nil {
				t.Fatal("a failed vanilla install still wrote a manifest")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the job never reported a failure")
}

func TestInstallVanillaRefusesANameThatCouldNotBeAKey(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.InstallVanilla(context.Background(), VanillaRequest{Profile: "Vanilla World"}); err == nil {
		t.Fatal("want a bad-profile refusal")
	}
}

func TestInstallRefusesAnEmptyRosterBeforeDownloading(t *testing.T) {
	m, store := newManager(t)
	// The roster exists but holds nobody: what removing the last player leaves.
	store.objects["server/whitelist.json"] = []byte("[]")
	store.objects["server/ops.json"] = []byte("[]")

	// No Modrinth fake is installed, so anything that reached the network would
	// fail differently: the roster refusal has to come first.
	if _, err := m.Install(context.Background(), InstallRequest{Source: "test-pack"}); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job := m.Job()
		if job.Phase == "failed" {
			if !strings.Contains(job.Error, "player list is empty") {
				t.Fatalf("error = %q, want the roster refusal", job.Error)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the job never reported a failure")
}

func TestInstallVanillaCanAnswerTheWorldQuestion(t *testing.T) {
	m, store := newManager(t)
	store.objects["server/whitelist.json"] = []byte(`[{"uuid":"x","name":"Notch"}]`)
	// The vanilla profile holds a world somebody else's pack made.
	store.objects["profiles/vanilla/world.tar.gz"] = []byte("world")
	store.objects["profiles/vanilla/pack.json"] = []byte(`{"source":"modrinth","slug":"cobblemon","versionId":"v1"}`)

	// Without an answer the job refuses, and the panel turns that into a button.
	if _, err := m.InstallVanilla(context.Background(), VanillaRequest{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.Job(); got.Phase == "failed" {
			if !strings.Contains(got.Error, "--new-world") {
				t.Fatalf("error = %q, want the world question", got.Error)
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// With NewWorld it is answerable from the page, which has no CLI flag to pass.
	if _, err := m.InstallVanilla(context.Background(), VanillaRequest{NewWorld: true}); err != nil {
		t.Fatalf("second start: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job := m.Job()
		// The Fabric fetch is real, so a network failure is not this test's business;
		// what matters is that the world question no longer stops it.
		if strings.Contains(job.Error, "--new-world") {
			t.Fatalf("NewWorld did not answer the question: %s", job.Error)
		}
		if job.Phase == "failed" || (job.Phase == "done" && job.Kind == "install") {
			if !strings.Contains(job.Error, "--new-world") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}
