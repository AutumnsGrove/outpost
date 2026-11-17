package pack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// modJar builds a real mod jar in memory: a fabric.mod.json, and any jars it
// nests in META-INF/jars. Fabric reads exactly these two places, so a fixture
// that only has a file name would prove nothing about a dependency check.
func modJar(t *testing.T, id string, depends map[string]string, nested map[string][]byte) []byte {
	t.Helper()
	return modJarProviding(t, id, nil, depends, nested)
}

// modJarProviding also declares `provides`: one file answering for more than one
// mod id is how a library ships its own add-ons, and it is the case a rescue has
// to move exactly once.
func modJarProviding(t *testing.T, id string, provides []string, depends map[string]string, nested map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	meta := map[string]any{"schemaVersion": 1, "id": id, "version": "1.0.0"}
	if depends != nil {
		meta["depends"] = depends
	}
	if provides != nil {
		meta["provides"] = provides
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("fabric.mod.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(nested))
	for name := range nested {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create("META-INF/jars/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(nested[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stagedMod is one file the fake server serves.
type stagedMod struct {
	body   []byte
	client string
	server string
}

// stageJars puts jars on the fake server and writes a manifest that indexes them,
// in name order so a failure reads the same way twice.
func (f *modrinth) stageJars(t *testing.T, mods map[string]stagedMod) {
	t.Helper()

	names := make([]string, 0, len(mods))
	for name := range mods {
		names = append(names, name)
	}
	sort.Strings(names)

	m := Manifest{
		FormatVersion: 1,
		VersionID:     "ver1",
		Name:          "Test Pack",
		Dependencies:  map[string]string{"minecraft": "1.20.1", "fabric-loader": "0.15.7"},
	}
	for _, name := range names {
		st := mods[name]
		f.mods[name] = st.body

		env := &Env{Client: st.client, Server: st.server}
		if st.client == "" {
			env = nil
		}
		sum := sha512.Sum512(st.body)
		m.Files = append(m.Files, ManifestFile{
			Path:      "mods/" + name,
			Downloads: []string{f.base + "/files/" + name},
			Hashes:    map[string]string{"sha512": hex.EncodeToString(sum[:])},
			FileSize:  int64(len(st.body)),
			Env:       env,
		})
	}
	f.mrpack = zipBytes(t, m, nil)
}

func TestFetchKeepsAClientOnlyModAnotherModRequires(t *testing.T) {
	api := newModrinth(t)
	// Chipped is required on both sides and says it will not load without athena,
	// which the pack marks client-only. Homestead 2.0.1 is exactly this shape.
	api.stageJars(t, map[string]stagedMod{
		"chipped-3.0.7.jar": {
			body:   modJar(t, "chipped", map[string]string{"athena": ">=3.1.1", "minecraft": "1.20.x"}, nil),
			client: "required", server: "required",
		},
		"athena-3.1.2.jar": {
			body:   modJar(t, "athena", nil, nil),
			client: "required", server: "unsupported",
		},
	})

	dir := t.TempDir()
	res, err := Fetch(context.Background(), Options{Source: "test-pack", Dir: dir, MemoryGB: 4})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "mods", "athena-3.1.2.jar")); err != nil {
		t.Fatalf("athena was dropped and the server cannot boot: %v", err)
	}
	for _, skipped := range res.Skipped {
		if skipped == "mods/athena-3.1.2.jar" {
			t.Fatal("athena is reported as skipped and also installed")
		}
	}
	if res.Mods != 2 {
		t.Fatalf("mods = %d, want 2 (the rescued jar is a mod too)", res.Mods)
	}
	if !warningsContain(res.Warnings, "athena") || !warningsContain(res.Warnings, "chipped") {
		t.Fatalf("the rescue should say what it did and why: %v", res.Warnings)
	}

	// The staging directory is a download site, not part of the pack.
	if _, err := os.Stat(filepath.Join(dir, ".deps")); !os.IsNotExist(err) {
		t.Fatal("the staging directory survived the fetch")
	}
}

func TestFetchRefusesAPackWhoseModsCannotLoad(t *testing.T) {
	api := newModrinth(t)
	api.stageJars(t, map[string]stagedMod{
		"chipped-3.0.7.jar": {
			body:   modJar(t, "chipped", map[string]string{"athena": ">=3.1.1"}, nil),
			client: "required", server: "required",
		},
	})

	_, err := Fetch(context.Background(), Options{Source: "test-pack", Dir: t.TempDir(), MemoryGB: 4})
	if err == nil {
		t.Fatal("a pack that boots into a mod-resolution crash was installed")
	}
	for _, want := range []string{"chipped", "athena", "cannot start a dedicated server"} {
		if !bytes.Contains([]byte(err.Error()), []byte(want)) {
			t.Fatalf("error does not say %q:\n%v", want, err)
		}
	}
}

func TestFetchIgnoresADependencyTheLoaderProvides(t *testing.T) {
	api := newModrinth(t)
	api.stageJars(t, map[string]stagedMod{
		"create-1.0.jar": {
			body:   modJar(t, "create", map[string]string{"minecraft": "1.20.x", "java": ">=17", "fabricloader": ">=0.15"}, nil),
			client: "required", server: "required",
		},
	})

	if _, err := Fetch(context.Background(), Options{Source: "test-pack", Dir: t.TempDir(), MemoryGB: 4}); err != nil {
		t.Fatalf("fabricloader, minecraft and java are not mods to install: %v", err)
	}
}

func TestScanModDepsCountsNestedAndProvidedIDs(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, body []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "mods"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mods", name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Fabric API is a shell of nested modules, and a mod depends on one of them by
	// its own id. Reading only the outer jar would call that missing.
	api := modJar(t, "fabric-api", nil, map[string][]byte{
		"fabric-api-base.jar": modJar(t, "fabric-api-base", nil, nil),
	})
	mk("fabric-api-0.92.jar", api)
	mk("create-1.0.jar", modJar(t, "create", map[string]string{"fabric-api-base": "*"}, nil))

	gaps, provided, complete, err := scanModDeps(dir, []string{"mods/fabric-api-0.92.jar", "mods/create-1.0.jar"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !complete {
		t.Fatal("every jar was readable, so the scan should be complete")
	}
	if len(gaps) != 0 {
		t.Fatalf("nested and provided ids should satisfy a dependency, gaps = %v", gaps)
	}
	for _, id := range []string{"fabric-api", "fabric-api-base"} {
		if !provided[id] {
			t.Fatalf("%s missing from provided: %v", id, provided)
		}
	}

	// And a jar that is not a mod is not a reason to refuse a pack.
	mk("not-a-mod.jar", []byte("not a zip"))
	if _, _, _, err := scanModDeps(dir, []string{"mods/not-a-mod.jar"}); err != nil {
		t.Fatalf("an unreadable jar stopped the scan: %v", err)
	}
}

func TestScanModDepsReportsWhatEachModWants(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := modJar(t, "chipped", map[string]string{"athena": ">=3.1.1"}, nil)
	if err := os.WriteFile(filepath.Join(dir, "mods", "chipped-3.0.7.jar"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	gaps, _, _, err := scanModDeps(dir, []string{"mods/chipped-3.0.7.jar"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("gaps = %v, want one", gaps)
	}
	if gaps[0].Mod != "chipped" || gaps[0].ID != "athena" || gaps[0].Carrier != "chipped-3.0.7.jar" {
		t.Fatalf("gap = %+v", gaps[0])
	}
	if got := gaps[0].String(); got != "chipped (chipped-3.0.7.jar) requires athena >=3.1.1" {
		t.Fatalf("message = %q", got)
	}
}

// One skipped jar can answer for two gaps: its own id and something it provides.
// It was moved for the first gap and then renamed again for the second, from a
// path it had already left, so a pack that was satisfiable was refused.
func TestFetchMovesAJarThatClosesTwoGapsOnce(t *testing.T) {
	api := newModrinth(t)
	api.stageJars(t, map[string]stagedMod{
		"chipped-3.0.7.jar": {
			body:   modJar(t, "chipped", map[string]string{"athena": ">=3.1.1", "athena-core": "*"}, nil),
			client: "required", server: "required",
		},
		"athena-3.1.2.jar": {
			body:   modJarProviding(t, "athena", []string{"athena-core"}, nil, nil),
			client: "required", server: "unsupported",
		},
	})

	dir := t.TempDir()
	res, err := Fetch(context.Background(), Options{Source: "test-pack", Dir: dir, MemoryGB: 4})
	if err != nil {
		t.Fatalf("a satisfiable pack was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", "athena-3.1.2.jar")); err != nil {
		t.Fatalf("athena was not kept: %v", err)
	}
	if res.Mods != 2 {
		t.Fatalf("mods = %d, want 2 — one rescue, counted once", res.Mods)
	}
	var rescues int
	for _, w := range res.Warnings {
		if strings.Contains(w, "athena-3.1.2.jar is marked client-only") {
			rescues++
		}
	}
	if rescues != 1 {
		t.Fatalf("the rescue was reported %d times, want 1: %v", rescues, res.Warnings)
	}
}

// A bundled library bigger than the metadata cap used to be cut off mid-file: the
// nested zip would not open, its ids went unseen, and a pack that boots fine was
// refused for a dependency sitting right there in it.
func TestScanModDepsReadsANestedJarPastTheMetadataCap(t *testing.T) {
	big := make([]byte, 5<<20)
	rng := rand.New(rand.NewSource(1))
	if _, err := rng.Read(big); err != nil {
		t.Fatal(err)
	}
	nested := modJarWithPayload(t, "biglib", big)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mods", "carrier.jar"), modJar(t, "carrier", nil, map[string][]byte{"biglib.jar": nested}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mods", "wants.jar"), modJar(t, "wants", map[string]string{"biglib": "*"}, nil), 0o644); err != nil {
		t.Fatal(err)
	}

	gaps, provided, complete, err := scanModDeps(dir, []string{"mods/carrier.jar", "mods/wants.jar"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !complete {
		t.Fatal("a 5 MiB nested jar is readable; the scan called itself incomplete")
	}
	if len(gaps) != 0 {
		t.Fatalf("a bundled library was reported missing: %v", gaps)
	}
	if !provided["biglib"] {
		t.Fatal("the nested jar's id was not counted")
	}
}

// modJarWithPayload is a jar carrying id, its metadata and size bytes of filler,
// so its own bytes are bigger than a metadata cap.
func modJarWithPayload(t *testing.T, id string, payload []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "id": id, "version": "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("fabric.mod.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("assets/filler.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An unreadable jar means the gaps are in the evidence, not in the pack. That
// held only on the path that had skipped files to stage: with nothing to stage,
// the fetch refused before it asked whether it had seen everything.
func TestFetchDoesNotRefuseOnAnIncompleteScan(t *testing.T) {
	api := newModrinth(t)
	api.stageJars(t, map[string]stagedMod{
		"chipped-3.0.7.jar": {
			body:   modJar(t, "chipped", map[string]string{"athena": ">=3.1.1"}, nil),
			client: "required", server: "required",
		},
		// Nothing can read this one, so nothing can say what it provides.
		"corrupt-1.0.jar": {
			body:   []byte("this is not a zip"),
			client: "required", server: "required",
		},
	})

	res, err := Fetch(context.Background(), Options{Source: "test-pack", Dir: t.TempDir(), MemoryGB: 4})
	if err != nil {
		t.Fatalf("a pack was refused on evidence the scan admitted was incomplete: %v", err)
	}
	if !warningsContain(res.Warnings, "could not confirm athena") {
		t.Fatalf("the gap should be reported as unconfirmed, not closed: %v", res.Warnings)
	}
}
