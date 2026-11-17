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

func (m *memStore) Copy(_ context.Context, _, srcKey, dstKey string) (int64, error) {
	b, ok := m.objects[srcKey]
	if !ok {
		return 0, r2.ErrNotFound
	}
	m.objects[dstKey] = append([]byte(nil), b...)
	return int64(len(b)), nil
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

// The vanilla picker is the table, not a version number: the page renders what
// this answers, and the profile it names is the one the select's default comes
// from.
func TestPackPanelOffersTheWholeMinecraftTable(t *testing.T) {
	f, _, _ := newPackFixture(t)

	out := packGet(t, f)
	if len(out.Minecrafts) != len(publish.Minecrafts) {
		t.Fatalf("the panel offers %d versions, the table bootable %d", len(out.Minecrafts), len(publish.Minecrafts))
	}
	for i, m := range publish.Minecrafts {
		if out.Minecrafts[i] != m {
			t.Fatalf("row %d = %+v, table says %+v", i, out.Minecrafts[i], m)
		}
	}
	if out.SupportedMC != publish.SupportedMC {
		t.Fatalf("supportedMc = %q", out.SupportedMC)
	}
	if out.VanillaProfile != "vanilla" {
		t.Fatalf("vanillaProfile = %q, want the profile an unnamed install lands in", out.VanillaProfile)
	}
}

func TestPackVanillaTakesAVersionAndRefusesOneWithNoJDK(t *testing.T) {
	f, _, blobs := newPackFixture(t)
	token := f.formToken(t)

	// A version from the table gets a job; the version is in the job's own name, so
	// nothing has to run to see it arrived.
	rec := f.do("POST", "/api/pack/vanilla", `{"token":"`+token+`","mc":"1.21.1"}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install vanilla 1.21.1 = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Vanilla 1.21.1") {
		t.Fatalf("the job does not name the version: %s", rec.Body.String())
	}

	// A version outside the table is a refusal, not a rental that dies at boot.
	rec = f.do("POST", "/api/pack/vanilla", `{"token":"`+token+`","mc":"1.19.4"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("install vanilla 1.19.4 = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), publish.MinecraftList()) {
		t.Fatalf("the refusal should name the table: %s", rec.Body.String())
	}
	// And the first job was refused rather than half-done: no manifest was written
	// for the version nobody can boot.
	if _, ok := blobs.objects["profiles/vanilla/pack.json"]; ok {
		t.Fatal("a refused vanilla install wrote a manifest")
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

func TestPackWorldDeleteMovesTheWorldToTrash(t *testing.T) {
	f, _, blobs := newPackFixture(t)

	rec := f.do("POST", "/api/pack/world/delete", `{"token":"`+f.formToken(t)+`","profile":"cobblemon-fabric"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := blobs.objects["profiles/cobblemon-fabric/world.tar.gz"]; ok {
		t.Fatal("the world is still there")
	}
	found := false
	for key := range blobs.objects {
		if strings.HasPrefix(key, "trash/cobblemon-fabric-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no trash copy: %v", blobs.objects)
	}
	// The message has to name the piece of the answer people care about.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body["message"], "recoverable until") {
		t.Fatalf("message = %q", body["message"])
	}
}

func TestPackDeleteRefusesTheActiveProfile(t *testing.T) {
	f, _, blobs := newPackFixture(t)
	// Make it the profile Start boots.
	_ = f.store.MetaSet("profile", "cobblemon-fabric")

	rec := f.do("POST", "/api/pack/delete", `{"token":"`+f.formToken(t)+`","profile":"cobblemon-fabric"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete of the active profile = %d, want a refusal", rec.Code)
	}
	if _, ok := blobs.objects["profiles/cobblemon-fabric/mods/create-1.0.jar"]; !ok {
		t.Fatal("the refusal still removed files")
	}
}

func TestPackDeleteRemovesAnInactiveProfile(t *testing.T) {
	f, _, blobs := newPackFixture(t)
	// The fixture's active profile is "default", so the pack profile is fair game.
	rec := f.do("POST", "/api/pack/delete", `{"token":"`+f.formToken(t)+`","profile":"cobblemon-fabric"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	for key := range blobs.objects {
		if strings.HasPrefix(key, "profiles/cobblemon-fabric/") {
			t.Fatalf("%s survived", key)
		}
	}
}

func TestDestructivePackEndpointsRefuseWhileAServerIsRented(t *testing.T) {
	// The only thing between a running session's world and a delete. A live server
	// uploads over its own profile's world when it shuts down, so a delete
	// underneath it is a delete that undoes itself.
	for _, route := range []string{"/api/pack/world/delete", "/api/pack/delete"} {
		f, _, blobs := newPackFixture(t)
		f.setLive(t)

		rec := f.do("POST", route, `{"token":"`+f.formToken(t)+`","profile":"cobblemon-fabric"}`, nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s while running = %d: %s", route, rec.Code, rec.Body.String())
		}
		if _, ok := blobs.objects["profiles/cobblemon-fabric/world.tar.gz"]; !ok {
			t.Fatalf("%s deleted the world under a live server", route)
		}
		for key := range blobs.objects {
			if strings.HasPrefix(key, "trash/") {
				t.Fatalf("%s put a world in the trash under a live server: %s", route, key)
			}
		}
	}
}

func TestOverviewOffersThePlansAndPricesThem(t *testing.T) {
	f, _, _ := newPackFixture(t)
	rec := f.do("GET", "/api/overview", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview = %d: %s", rec.Code, rec.Body.String())
	}
	var out overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Plans) < 4 {
		t.Fatalf("plans = %+v", out.Plans)
	}
	for _, p := range out.Plans {
		if p.Type == "" || p.VCPU <= 0 || p.RAMGB <= 0 || p.HourlyRate <= 0 {
			t.Fatalf("plan %+v is missing what the picker shows", p)
		}
	}
	// The default opens on a plan the server would rent, not on an empty string.
	if out.SelectedType == "" {
		t.Fatal("no plan was selected")
	}
	found := false
	for _, p := range out.Plans {
		if p.Type == out.SelectedType {
			found = true
		}
	}
	if !found {
		t.Fatalf("selected %q is not in the list", out.SelectedType)
	}
}
