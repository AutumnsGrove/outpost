package publish

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"outpost/internal/r2"
)

// --- a pack on disk --------------------------------------------------------

// writePack creates the smallest pack that publishes: a launcher, a whitelist
// file, two mods (one client-only) and a script.
func writePack(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for _, d := range []string{"server", "mods", "config", "scripts"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	files := map[string]string{
		"server/fabric-server-launch.jar": "not really a jar, but a file",
		"server/whitelist.json":           "[]",
		"server/ops.json":                 "[]",
		"mods/create-0.5.1.jar":           "jar",
		"mods/sodium-0.5.13.jar":          "client only",
		"config/create-common.toml":       "key = value",
		"scripts/start.sh":                "#!/bin/bash\n",
		"client-only-mods.txt":            "# comment\nsodium-0.5.13.jar\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// key is the R2 key for a path inside a profile. Every key publish writes is
// scoped by profile, so the tests say it once here instead of on every line.
func key(rel string) string { return profilePrefix(DefaultProfile) + rel }

func keys(objects []Object) []string {
	out := make([]string, len(objects))
	for i, o := range objects {
		out[i] = o.Key
	}
	return out
}

// --- Build -----------------------------------------------------------------

func TestBuildMapsTheLayoutTheTemplateReads(t *testing.T) {
	pack := writePack(t)

	objects, warnings, err := Build(context.Background(), Options{Dir: pack, AssetBucket: "mc-assets"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	got := map[string]bool{}
	for _, k := range keys(objects) {
		got[k] = true
	}
	for _, want := range []string{
		key("server/fabric-server-launch.jar"),
		// The player list is global: same people, every profile.
		"server/whitelist.json",
		"server/ops.json",
		key("server/eula.txt"),          // generated
		key("server/server.properties"), // generated
		key("mods/create-0.5.1.jar"),
		key("config/create-common.toml"),
		key("scripts/start.sh"),
	} {
		if !got[want] {
			t.Errorf("missing key %s (have %v)", want, keys(objects))
		}
	}

	// The client-only mod must not reach the server: Sodium on a dedicated
	// server is a crash, not a performance win.
	if got[key("mods/sodium-0.5.13.jar")] {
		t.Error("a client-only mod was uploaded")
	}
	// The filter list itself is local bookkeeping, not a server file.
	if got["client-only-mods.txt"] {
		t.Error("the client-only list was uploaded")
	}
	if !warningsContain(warnings, "client-only") {
		t.Errorf("warnings should mention the skipped mods: %v", warnings)
	}

	// Sorted, so two runs produce the same order and a diff is readable.
	for i := 1; i < len(objects); i++ {
		if objects[i-1].Key > objects[i].Key {
			t.Fatalf("objects are not sorted: %s then %s", objects[i-1].Key, objects[i].Key)
		}
	}
}

func TestBuildRefusesAPackWithNoLauncher(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server", "whitelist.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := Build(context.Background(), Options{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "fabric-server-launch.jar") {
		t.Fatalf("expected a missing-launcher error, got %v", err)
	}
}

func TestBuildRefusesAPackNobodyCouldJoin(t *testing.T) {
	pack := writePack(t)
	// A pack with no whitelist and no --open would boot a server that refuses
	// every player, which is the least useful kind of working.
	if err := os.Remove(filepath.Join(pack, "server", "whitelist.json")); err != nil {
		t.Fatal(err)
	}

	_, _, err := Build(context.Background(), Options{Dir: pack})
	if err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatalf("expected a whitelist error, got %v", err)
	}

	// --open says so out loud and works.
	objects, _, err := Build(context.Background(), Options{Dir: pack, Open: true})
	if err != nil {
		t.Fatalf("open build: %v", err)
	}
	for _, o := range objects {
		if o.Key == "server/server.properties" {
			if !strings.Contains(string(o.Content), "white-list=false") {
				t.Fatalf("--open did not turn the whitelist off:\n%s", o.Content)
			}
		}
	}
}

func TestBuildGeneratesABootableServerFromNothing(t *testing.T) {
	// The bar for a first publish: launcher + whitelist, everything else made up.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server", "fabric-server-launch.jar"), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server", "whitelist.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	objects, warnings, err := Build(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	have := map[string]string{}
	for _, o := range objects {
		have[o.Key] = string(o.Content)
	}
	for _, want := range []string{key("server/eula.txt"), key("server/server.properties"), key("scripts/start.sh")} {
		if _, ok := have[want]; !ok {
			t.Fatalf("missing generated %s (have %v)", want, keys(objects))
		}
	}
	if !strings.Contains(have[key("scripts/start.sh")], "fabric-server-launch.jar") {
		t.Fatalf("start.sh does not start the server:\n%s", have[key("scripts/start.sh")])
	}
	if !warningsContain(warnings, "start.sh") {
		t.Errorf("warnings should mention the generated script: %v", warnings)
	}
}

func TestBuildSkipsWorldDirectories(t *testing.T) {
	pack := writePack(t)
	world := filepath.Join(pack, "server", "world")
	if err := os.MkdirAll(world, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("save"), 0o644); err != nil {
		t.Fatal(err)
	}

	objects, _, err := Build(context.Background(), Options{Dir: pack})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, o := range objects {
		if strings.Contains(o.Key, "world") {
			t.Fatalf("a world file was in the asset plan: %s", o.Key)
		}
	}
}

// --- Run against a fake store ---------------------------------------------

// fakeStore is bucket-aware: `objects` is the asset bucket and `worlds` is
// mc-worlds. The two hold keys of the same shape (profiles/<id>/pack.json), so a
// test that could not tell them apart could not see which one was written.
type fakeStore struct {
	objects map[string][]byte
	worlds  map[string][]byte
	sums    map[string]string
	puts    int
	removes int
}

const worldBucket = "mc-worlds"

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string][]byte{}, worlds: map[string][]byte{}, sums: map[string]string{}}
}

// bucket picks the map a call is talking about.
func (f *fakeStore) bucket(name string) map[string][]byte {
	if name == worldBucket {
		return f.worlds
	}
	return f.objects
}

func (f *fakeStore) Put(_ context.Context, bucket, key string, r io.Reader, size int64, sum string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return io.ErrUnexpectedEOF
	}
	f.bucket(bucket)[key] = b
	f.sums[key] = sum
	f.puts++
	return nil
}

func (f *fakeStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	b, ok := f.bucket(bucket)[key]
	if !ok {
		return nil, r2.ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) Stat(_ context.Context, bucket, key string) (r2.Info, error) {
	b, ok := f.bucket(bucket)[key]
	if !ok {
		return r2.Info{}, r2.ErrNotFound
	}
	return r2.Info{Size: int64(len(b)), SHA256: f.sums[key]}, nil
}

func (f *fakeStore) List(_ context.Context, bucket, prefix string) ([]r2.Object, error) {
	var out []r2.Object
	for k, v := range f.bucket(bucket) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, r2.Object{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func (f *fakeStore) Remove(_ context.Context, bucket, key string) error {
	delete(f.bucket(bucket), key)
	f.removes++
	return nil
}

func TestRunUploadsThenLeavesItAlone(t *testing.T) {
	pack := writePack(t)
	st := newFakeStore()
	ctx := context.Background()

	first, err := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(first.Uploaded) == 0 {
		t.Fatal("nothing uploaded")
	}
	if _, ok := st.objects[key("server/eula.txt")]; !ok {
		t.Fatal("eula.txt was not uploaded")
	}

	second, err := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.Uploaded) != 0 {
		t.Fatalf("second run re-uploaded %v", second.Uploaded)
	}
	if len(second.Unchanged) != len(first.Uploaded) {
		t.Fatalf("unchanged = %d, want %d", len(second.Unchanged), len(first.Uploaded))
	}
}

// --- the player list ------------------------------------------------------

// noMojang points the Mojang client at a server that fails the test the moment
// it is asked anything. Reusing the bucket's player list is a promise that no
// name is resolved, so the test has to be able to hear a lookup.
func noMojang(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a Mojang lookup was made: %s", r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	old := mojangBase
	mojangBase = srv.URL
	t.Cleanup(func() { mojangBase = old })
}

// stripPlayers removes the player files writePack ships, which is the state of a
// pack that was never authored with a list.
func stripPlayers(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"whitelist.json", "ops.json"} {
		if err := os.Remove(filepath.Join(dir, "server", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunReusesThePlayerListAlreadyInTheBucket(t *testing.T) {
	pack := writePack(t)
	stripPlayers(t, pack)

	const whitelist = `[{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch"}]`
	const ops = `[{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch","level":4,"bypassesPlayerLimit":false}]`
	st := newFakeStore()
	st.objects["server/whitelist.json"] = []byte(whitelist)
	st.objects["server/ops.json"] = []byte(ops)
	noMojang(t)

	res, err := Run(context.Background(), st, Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The plan has to actually hold both files, not merely leave the seeded bytes
	// in the bucket: Unchanged means "planned and already correct in R2", while a
	// file that was never planned appears in neither list.
	planned := map[string]string{}
	for _, k := range append(append([]string{}, res.Uploaded...), res.Unchanged...) {
		planned[k] = "yes"
	}
	for _, k := range []string{"server/whitelist.json", "server/ops.json"} {
		if planned[k] == "" {
			t.Fatalf("%s was not in the plan: uploaded %v, unchanged %v", k, res.Uploaded, res.Unchanged)
		}
	}
	// Verbatim: not re-marshalled, not reordered, not looked up again.
	if got := string(st.objects["server/whitelist.json"]); got != whitelist {
		t.Fatalf("the whitelist was not published verbatim: %q", got)
	}
	if got := string(st.objects["server/ops.json"]); got != ops {
		t.Fatalf("ops.json was not published verbatim: %q", got)
	}
	if len(res.Players) != 1 || res.Players[0] != "Notch" {
		t.Fatalf("players = %v", res.Players)
	}
	// A republish should say where the list came from, not leave it to be found.
	if !strings.Contains(strings.Join(res.Warnings, " "), "reused the player list") {
		t.Fatalf("no warning said the list was reused: %v", res.Warnings)
	}
}

func TestRunPrefersThePacksOwnPlayerListToTheBucket(t *testing.T) {
	// writePack ships server/whitelist.json holding "[]".
	pack := writePack(t)
	st := newFakeStore()
	st.objects["server/whitelist.json"] = []byte(`[{"uuid":"x","name":"Notch"}]`)
	noMojang(t)

	if _, err := Run(context.Background(), st, Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(st.objects["server/whitelist.json"]); strings.Contains(got, "Notch") {
		t.Fatalf("the bucket's list overwrote the pack's own: %q", got)
	}
}

func TestRunResolvesNamesEvenWhenTheBucketHasAList(t *testing.T) {
	pack := writePack(t)
	stripPlayers(t, pack)
	st := newFakeStore()
	st.objects["server/whitelist.json"] = []byte(`[{"uuid":"x","name":"Notch"}]`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"00112233445566778899aabbccddeeff","name":"SomeoneElse"}`)
	}))
	defer srv.Close()
	old := mojangBase
	mojangBase = srv.URL
	defer func() { mojangBase = old }()

	// Naming a player is a deliberate override: it wins over the bucket.
	if _, err := Run(context.Background(), st, Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true,
		Whitelist: []string{"SomeoneElse"},
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := string(st.objects["server/whitelist.json"])
	if !strings.Contains(got, "SomeoneElse") {
		t.Fatalf("--whitelist did not win: %q", got)
	}
	if strings.Contains(got, "Notch") {
		t.Fatalf("the bucket's list leaked into an explicit --whitelist: %q", got)
	}
}

func TestRunStillRefusesWhenThereIsNoListAnywhere(t *testing.T) {
	pack := writePack(t)
	stripPlayers(t, pack)
	noMojang(t)

	// An empty bucket and a pack with no list: a server nobody could join is not
	// a publish worth making, so the same refusal stands.
	_, err := Run(context.Background(), newFakeStore(), Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true,
	})
	if err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatalf("expected a whitelist error, got %v", err)
	}
}

func TestRunNeverTouchesTheBucketForPlayersWithOpen(t *testing.T) {
	pack := writePack(t)
	stripPlayers(t, pack)
	st := newFakeStore()
	st.objects["server/whitelist.json"] = []byte(`[{"uuid":"x","name":"Notch"}]`)
	noMojang(t)

	if _, err := Run(context.Background(), st, Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true, Open: true,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The seeded key is still there because it was never ours to remove, but the
	// run published no players of its own: nothing was added to the plan.
	res, err := Run(context.Background(), st, Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true, Open: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(res.Players) != 0 {
		t.Fatalf("--open pulled the bucket's whitelist into the plan: %v", res.Players)
	}
}

func TestRunPrunesStaleModsOnly(t *testing.T) {
	pack := writePack(t)
	st := newFakeStore()
	ctx := context.Background()

	st.objects[key("mods/removed-mod-1.0.jar")] = []byte("old")
	st.objects[key("config/old.toml")] = []byte("old")
	st.objects[key("root/kubejs/old.js")] = []byte("old")
	st.objects[key("server/somebody-elses-file.txt")] = []byte("mine")

	res, err := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets", Prune: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, ok := st.objects[key("mods/removed-mod-1.0.jar")]; ok {
		t.Fatal("a stale mod survived --prune")
	}
	if _, ok := st.objects[key("config/old.toml")]; ok {
		t.Fatal("a stale config survived --prune")
	}
	if _, ok := st.objects[key("root/kubejs/old.js")]; ok {
		t.Fatal("a stale file in root/ survived --prune")
	}
	// server/ is never pruned: it is where a person's own files live.
	if _, ok := st.objects[key("server/somebody-elses-file.txt")]; !ok {
		t.Fatal("--prune deleted a file from server/")
	}
	if len(res.Removed) != 3 {
		t.Fatalf("removed = %v", res.Removed)
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	pack := writePack(t)
	st := newFakeStore()

	res, err := Run(context.Background(), st, Options{Dir: pack, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(res.Uploaded) == 0 {
		t.Fatal("dry run should still report what it would upload")
	}
	if st.puts != 0 || len(st.objects) != 0 {
		t.Fatal("dry run wrote to the store")
	}
}

// --- Fabric and Mojang, against fake servers -------------------------------

func TestFabricServerPicksTheNewestStableLoader(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/versions/loader/1.20.1":
			_, _ = io.WriteString(w, `[
				{"loader":{"version":"0.21.0-beta.1","stable":false}},
				{"loader":{"version":"0.20.1","stable":true}},
				{"loader":{"version":"0.19.0","stable":true}}
			]`)
		case "/v2/versions/installer":
			_, _ = io.WriteString(w, `[
				{"version":"1.1.2","stable":true},
				{"version":"1.1.1","stable":true}
			]`)
		case "/v2/versions/loader/1.20.1/0.20.1/1.1.2/server/jar":
			asked = r.URL.Path
			_, _ = w.Write([]byte("PK\x03\x04rest-of-a-jar"))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := fabricMetaBase
	fabricMetaBase = srv.URL
	defer func() { fabricMetaBase = old }()

	loader, jar, err := FabricServer(context.Background(), "1.20.1", "")
	if err != nil {
		t.Fatalf("fabric: %v", err)
	}
	if loader != "0.20.1" {
		t.Fatalf("loader = %q, want the newest stable", loader)
	}
	if asked == "" || len(jar) == 0 {
		t.Fatal("the launcher jar was not downloaded")
	}
}

func TestFabricServerRejectsAnErrorPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/versions/installer" {
			_, _ = io.WriteString(w, `[{"version":"1.1.2","stable":true}]`)
			return
		}
		_, _ = io.WriteString(w, "<html>not found</html>")
	}))
	defer srv.Close()
	old := fabricMetaBase
	fabricMetaBase = srv.URL
	defer func() { fabricMetaBase = old }()

	if _, _, err := FabricServer(context.Background(), "1.20.1", "0.20.1"); err == nil {
		t.Fatal("an HTML error page should not be accepted as a jar")
	}
}

func TestFabricServerFromTheRealMeta(t *testing.T) {
	// The fake above cannot catch a wrong path: real Fabric Meta answers a bad
	// endpoint with a 200 and a text body. This test is the guard, and it is
	// opt-in because it needs the network.
	if os.Getenv("OUTPOST_LIVE_TESTS") == "" {
		t.Skip("set OUTPOST_LIVE_TESTS=1 to hit the real Fabric Meta")
	}

	loader, jar, err := FabricServer(context.Background(), "1.20.1", "")
	if err != nil {
		t.Fatalf("fabric: %v", err)
	}
	if loader == "" || len(jar) < 10_000 {
		t.Fatalf("loader = %q, jar = %d bytes", loader, len(jar))
	}
	t.Logf("loader %s, launcher %d bytes", loader, len(jar))
}

func TestPlayerFilesResolveUUIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/users/profiles/minecraft/") {
		case "AutumnsAdventure":
			_, _ = io.WriteString(w, `{"id":"4d9b897a6b404a308e83eccb33333817","name":"AutumnsAdventure"}`)
		case "SomeoneElse":
			_, _ = io.WriteString(w, `{"id":"00112233445566778899aabbccddeeff","name":"SomeoneElse"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := mojangBase
	mojangBase = srv.URL
	defer func() { mojangBase = old }()

	whitelist, ops, err := PlayerFiles(context.Background(),
		[]string{"AutumnsAdventure", "SomeoneElse"}, []string{"AutumnsAdventure"})
	if err != nil {
		t.Fatalf("players: %v", err)
	}
	if !strings.Contains(string(whitelist), `"uuid": "4d9b897a-6b40-4a30-8e83-eccb33333817"`) {
		t.Fatalf("uuid was not dashed:\n%s", whitelist)
	}
	if !strings.Contains(string(whitelist), `"SomeoneElse"`) {
		t.Fatalf("second player missing:\n%s", whitelist)
	}
	if !strings.Contains(string(ops), `"level": 4`) || !strings.Contains(string(ops), `"AutumnsAdventure"`) {
		t.Fatalf("ops.json = %s", ops)
	}
	if strings.Contains(string(ops), "SomeoneElse") {
		t.Fatalf("ops.json should only hold ops:\n%s", ops)
	}
}

func TestPlayerFilesRejectAnOpWhoCannotJoin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"4d9b897a6b404a308e83eccb33333817","name":"AutumnsAdventure"}`)
	}))
	defer srv.Close()
	old := mojangBase
	mojangBase = srv.URL
	defer func() { mojangBase = old }()

	if _, _, err := PlayerFiles(context.Background(), []string{"AutumnsAdventure"}, []string{"Ghost"}); err == nil {
		t.Fatal("an op outside the whitelist should be refused")
	}
}

func TestDashUUID(t *testing.T) {
	got, err := dashUUID("4D9B897A6B404A308E83ECCB33333817")
	if err != nil {
		t.Fatalf("dash: %v", err)
	}
	if got != "4d9b897a-6b40-4a30-8e83-eccb33333817" {
		t.Fatalf("uuid = %q", got)
	}
	for _, bad := range []string{"", "xyz", "4d9b897a-6b40-4a30-8e83-eccb3333381z"} {
		if _, err := dashUUID(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func warningsContain(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

// Same size, different content: the case a length comparison cannot see.
func TestRunUploadsAFileThatChangedWithoutChangingSize(t *testing.T) {
	pack := writePack(t)
	st := newFakeStore()
	ctx := context.Background()
	props := filepath.Join(pack, "server", "server.properties")

	if err := os.WriteFile(props, []byte("max-players=20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets"}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(props, []byte("max-players=30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets"})
	if err != nil {
		t.Fatal(err)
	}
	if string(st.objects[key("server/server.properties")]) != "max-players=30\n" {
		t.Fatalf("the edit never reached R2: %q (uploaded %v)", st.objects[key("server/server.properties")], res.Uploaded)
	}
}

// An object an older publish wrote has no checksum; it is re-uploaded once so it
// gets one rather than trusted forever on its length.
func TestRunBackfillsObjectsWithoutAChecksum(t *testing.T) {
	pack := writePack(t)
	st := newFakeStore()
	ctx := context.Background()

	if _, err := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets"}); err != nil {
		t.Fatal(err)
	}
	for k := range st.sums {
		st.sums[k] = ""
	}
	res, err := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets"})
	if err != nil || len(res.Uploaded) == 0 {
		t.Fatalf("uploaded=%v err=%v: objects with no checksum should be re-uploaded", res.Uploaded, err)
	}
	again, _ := Run(ctx, st, Options{Dir: pack, AssetBucket: "mc-assets"})
	if len(again.Uploaded) != 0 {
		t.Fatalf("a third run re-uploaded %v", again.Uploaded)
	}
}

func TestRunRefusesAnOpsOnlyPackInsteadOfMixingItWithTheBucket(t *testing.T) {
	// A hand-rolled pack that brought its own ops.json but no whitelist.json. The
	// pack's player files are a pair, so half of the bucket's list must not be
	// grafted onto the pack's half: that published a plan with server/ops.json
	// twice, and the bucket's copy won by accident of sort order.
	pack := writePack(t)
	if err := os.Remove(filepath.Join(pack, "server", "whitelist.json")); err != nil {
		t.Fatal(err)
	}
	st := newFakeStore()
	st.objects["server/whitelist.json"] = []byte(`[{"uuid":"x","name":"Notch"}]`)
	st.objects["server/ops.json"] = []byte(`[{"uuid":"x","name":"Notch","level":4}]`)
	noMojang(t)

	_, err := Run(context.Background(), st, Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true,
	})
	if err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatalf("expected the no-whitelist refusal, got %v", err)
	}
	// And nothing half-written: the refusal happens before any upload.
	if st.puts != 0 {
		t.Fatalf("refused after %d upload(s)", st.puts)
	}
}

func TestRunDoesNotRepublishAnEmptyRoster(t *testing.T) {
	// `[]` is what the roster writes when the last player is removed, and a server
	// nobody may join is exactly what the refusal is for.
	pack := writePack(t)
	stripPlayers(t, pack)
	st := newFakeStore()
	st.objects["server/whitelist.json"] = []byte("[]")
	st.objects["server/ops.json"] = []byte("[]")
	noMojang(t)

	_, err := Run(context.Background(), st, Options{
		Dir: pack, AssetBucket: "mc-assets", ReusePlayers: true,
	})
	if err == nil || !strings.Contains(err.Error(), "no whitelist") {
		t.Fatalf("expected a whitelist refusal for an empty roster, got %v", err)
	}
}

func TestAllowsNobodyCoversTheRosterStatesThatRefuseEveryJoin(t *testing.T) {
	cases := map[string]bool{
		`[]`:                            true,
		``:                              true,
		`not json`:                      true,
		`[{"uuid":"x","name":"Notch"}]`: false,
	}
	for raw, want := range cases {
		set := &PlayerSet{Whitelist: []byte(raw), Ops: []byte("[]")}
		if got := set.AllowsNobody(); got != want {
			t.Errorf("AllowsNobody(%q) = %v, want %v", raw, got, want)
		}
	}
}
