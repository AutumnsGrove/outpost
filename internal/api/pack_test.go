package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"outpost/internal/config"
	"outpost/internal/pack"
	"outpost/internal/publish"
	"outpost/internal/r2"
)

// memStore is the slice of R2 the pack panel reads, in memory.
type memStore struct {
	objects map[string][]byte
}

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

// newPackFixture wires the pack panel into the usual fixture.
func newPackFixture(t *testing.T) (*fixture, *pack.Manager, *memStore) {
	t.Helper()
	f := newFixture(t, "")
	blobs := newMemStore()

	// One installed profile, one world.
	meta, _ := json.Marshal(publish.Pack{Source: "modrinth", Slug: "cobblemon-fabric", Name: "Cobblemon", VersionID: "v1", MC: "1.20.1", Loader: "fabric"})
	blobs.objects["profiles/cobblemon-fabric/pack.json"] = meta
	blobs.objects["profiles/cobblemon-fabric/mods/create-1.0.jar"] = []byte("jar")
	blobs.objects["profiles/cobblemon-fabric/mods/jei-1.0.jar"] = []byte("jar")
	blobs.objects["profiles/cobblemon-fabric/world.tar.gz"] = []byte("world")
	blobs.objects["profiles/cobblemon-fabric/pack.json"] = meta

	m := &pack.Manager{
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
		Dir:         t.TempDir(),
		Store:       blobs,
		Regions:     map[string]config.Region{"eu": {ServerType: "cx23"}},
		Activate:    func(_ context.Context, profile string) error { return f.orch.SetActiveProfile(profile) },
	}
	f.h = New(f.orch, f.store, Options{Roster: nil, Packs: m})
	return f, m, blobs
}

func packGet(t *testing.T, f *fixture) packOverview {
	t.Helper()
	rec := f.do("GET", "/api/pack", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/pack = %d: %s", rec.Code, rec.Body.String())
	}
	var out packOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPackPanelListsProfilesAndTheActiveOne(t *testing.T) {
	f, _, _ := newPackFixture(t)

	out := packGet(t, f)
	if len(out.Profiles) != 1 {
		t.Fatalf("profiles = %+v", out.Profiles)
	}
	p := out.Profiles[0]
	if p.ID != "cobblemon-fabric" || p.Mods != 2 || !p.HasWorld {
		t.Fatalf("profile = %+v", p)
	}
	if p.Pack == nil || p.Pack.Name != "Cobblemon" {
		t.Fatalf("profile pack = %+v", p.Pack)
	}
	if !out.Editable {
		t.Fatalf("an offline daemon should be editable: %+v", out)
	}
	if out.Active != publish.DefaultProfile {
		t.Fatalf("active = %q", out.Active)
	}
}

func TestPackPanelIsReadOnlyWhileAServerIsRented(t *testing.T) {
	f, _, _ := newPackFixture(t)
	f.setLive(t)

	out := packGet(t, f)
	if out.Editable || out.Detail == "" {
		t.Fatalf("a rented server should be read-only with a reason: %+v", out)
	}

	// And the install itself is refused, not just greyed out on the page.
	rec := f.do("POST", "/api/pack/install", `{"token":"`+f.formToken(t)+`","source":"cobblemon-fabric"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("install while running = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "save and stop") {
		t.Fatalf("the refusal should say what to do: %s", rec.Body.String())
	}
}

func TestPackPanelNeedsTheFormToken(t *testing.T) {
	f, _, _ := newPackFixture(t)

	rec := f.do("POST", "/api/pack/install", `{"source":"cobblemon-fabric"}`, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("install without a token = %d", rec.Code)
	}
}

func TestPackUseSwitchesAndIsRefusedWhileRented(t *testing.T) {
	f, _, _ := newPackFixture(t)
	token := f.formToken(t)

	rec := f.do("POST", "/api/pack/use", `{"token":"`+token+`","profile":"cobblemon-fabric"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("use = %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := f.orch.ActiveProfile(); got != "cobblemon-fabric" {
		t.Fatalf("active = %q", got)
	}

	f.setLive(t)
	rec = f.do("POST", "/api/pack/use", `{"token":"`+token+`","profile":"other"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("switching while rented = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPackPanelSaysSoWhenTheDaemonHasNoR2(t *testing.T) {
	f := newFixture(t, "") // no Packs option

	rec := f.do("POST", "/api/pack/install", `{"token":"`+f.formToken(t)+`","source":"x"}`, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("install without R2 = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "R2 credentials") {
		t.Fatalf("the refusal should name the reason: %s", rec.Body.String())
	}
}

func TestPackVersionsNeedsTheFormTokenAndReportsBadSources(t *testing.T) {
	f, _, _ := newPackFixture(t)

	rec := f.do("POST", "/api/pack/versions", `{"source":"cobblemon-fabric"}`, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("versions without a token = %d", rec.Code)
	}

	// A .mrpack URL names one version; the API says so rather than pretending the
	// list is empty.
	rec = f.do("POST", "/api/pack/versions",
		`{"token":"`+f.formToken(t)+`","source":"https://cdn.modrinth.com/data/x/versions/y/Pack.mrpack"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a .mrpack source = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nothing to choose") {
		t.Fatalf("the refusal should explain itself: %s", rec.Body.String())
	}
}
