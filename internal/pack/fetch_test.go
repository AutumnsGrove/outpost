package pack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"outpost/internal/publish"
)

// zipBytes builds a .mrpack in memory: a manifest, an overrides tree and a
// server-overrides tree.
func zipBytes(t *testing.T, m Manifest, overrides map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("modrinth.index.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}

	for name, body := range overrides {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// modrinth is a fake API plus CDN. Everything it serves is set on it before the
// fetch runs, so the handler can read the fields directly.
type modrinth struct {
	base      string
	mrpack    []byte
	mods      map[string][]byte
	apiCalls  atomic.Int32
	downloads atomic.Int32
}

func newModrinth(t *testing.T) *modrinth {
	t.Helper()
	f := &modrinth{mods: map[string][]byte{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/project/test-pack":
			f.apiCalls.Add(1)
			_, _ = io.WriteString(w, `{"id":"5FFgwNNP","slug":"test-pack","title":"Test Pack","project_type":"modpack"}`)

		case r.URL.Path == "/project/test-pack/version":
			f.apiCalls.Add(1)
			sum := sha512.Sum512(f.mrpack)
			versions := []Version{{
				ID:            "ver1",
				VersionNumber: "1.0.0",
				VersionType:   "release",
				GameVersions:  []string{"1.20.1"},
				Loaders:       []string{"fabric"},
				Files: []VersionFile{{
					URL:      f.base + "/pack.mrpack",
					Filename: "Test Pack 1.0.0.mrpack",
					Primary:  true,
					Size:     int64(len(f.mrpack)),
					Hashes:   map[string]string{"sha512": hex.EncodeToString(sum[:])},
				}},
			}}
			_ = json.NewEncoder(w).Encode(versions)

		case r.URL.Path == "/pack.mrpack":
			_, _ = w.Write(f.mrpack)

		case strings.HasPrefix(r.URL.Path, "/files/"):
			f.downloads.Add(1)
			body, ok := f.mods[strings.TrimPrefix(r.URL.Path, "/files/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	f.base = srv.URL
	old := modrinthBase
	modrinthBase = srv.URL
	t.Cleanup(func() { modrinthBase = old })
	return f
}

// stage puts a pack on the fake server: two mods, one of them client-only, and
// an overrides tree where server-overrides/ has to win.
func (f *modrinth) stage(t *testing.T) {
	t.Helper()

	f.mods["create-1.0.jar"] = []byte("create server jar")
	f.mods["sodium-0.5.13.jar"] = []byte("sodium client jar")

	hash := func(name string) map[string]string {
		sum := sha512.Sum512(f.mods[name])
		return map[string]string{"sha512": hex.EncodeToString(sum[:])}
	}

	m := Manifest{
		FormatVersion: 1,
		VersionID:     "ver1",
		Name:          "Test Pack",
		Dependencies:  map[string]string{"minecraft": "1.20.1", "fabric-loader": "0.15.7"},
		Files: []ManifestFile{
			{
				Path:      "mods/create-1.0.jar",
				Downloads: []string{f.base + "/files/create-1.0.jar"},
				Hashes:    hash("create-1.0.jar"),
				FileSize:  int64(len(f.mods["create-1.0.jar"])),
				Env:       &Env{Client: "required", Server: "required"},
			},
			{
				Path:      "mods/sodium-0.5.13.jar",
				Downloads: []string{f.base + "/files/sodium-0.5.13.jar"},
				Hashes:    hash("sodium-0.5.13.jar"),
				FileSize:  int64(len(f.mods["sodium-0.5.13.jar"])),
				Env:       &Env{Client: "required", Server: "unsupported"},
			},
		},
	}

	f.mrpack = zipBytes(t, m, map[string]string{
		"overrides/config/create.toml":             "shared",
		"server-overrides/config/create.toml":      "server wins",
		"server-overrides/server.properties":       "motd=pack\n",
		"overrides/resourcepacks/fancy.zip":        "client only",
		"overrides/kubejs/server_scripts/start.js": "console.info('hi')",
	})
}

func TestFetchMaterialisesAServerPack(t *testing.T) {
	api := newModrinth(t)
	api.stage(t)

	dir := t.TempDir()
	res, err := Fetch(context.Background(), Options{
		Source:   "https://modrinth.com/modpack/test-pack",
		Dir:      dir,
		MemoryGB: 4,
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}

	if got := read("mods/create-1.0.jar"); got != "create server jar" {
		t.Fatalf("server mod body = %q", got)
	}
	// env.server: unsupported is the pack saying "not on a dedicated server".
	if _, err := os.Stat(filepath.Join(dir, "mods", "sodium-0.5.13.jar")); !os.IsNotExist(err) {
		t.Fatal("a client-only mod was written into the pack")
	}
	// server-overrides wins over overrides on a shared path.
	if got := read("root/config/create.toml"); got != "server wins" {
		t.Fatalf("override precedence: %q", got)
	}
	// Configs belong at the server root, not in config/.
	if got := read("root/kubejs/server_scripts/start.js"); !strings.Contains(got, "hi") {
		t.Fatalf("kubejs script = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "root", "resourcepacks")); !os.IsNotExist(err) {
		t.Fatal("a client resource pack reached the server pack")
	}

	script := read("scripts/start.sh")
	if !strings.Contains(script, "-Xmx2G") {
		t.Fatalf("start.sh does not size the heap to a 4 GB box:\n%s", script)
	}

	meta, ok, err := publish.LoadPack(dir)
	if err != nil || !ok {
		t.Fatalf("pack.json: ok=%v err=%v", ok, err)
	}
	if meta.Slug != "test-pack" || meta.MC != "1.20.1" || meta.Loader != "fabric" || meta.LoaderVersion != "0.15.7" {
		t.Fatalf("pack.json = %+v", meta)
	}
	if meta.VersionID != "ver1" || meta.MemoryGB != 4 || meta.FileCount != res.Files {
		t.Fatalf("pack.json = %+v", meta)
	}

	// The pack ships its own server.properties and we ignore it, so say so.
	if !warningsContain(res.Warnings, "server.properties") {
		t.Fatalf("warnings should admit the pack's server.properties is ignored: %v", res.Warnings)
	}
}

func TestFetchIsCheapTheSecondTimeAndPrunesWhatThePackDropped(t *testing.T) {
	api := newModrinth(t)
	api.stage(t)

	dir := t.TempDir()
	opts := Options{Source: "test-pack", Dir: dir, MemoryGB: 4}
	if _, err := Fetch(context.Background(), opts); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	first := api.downloads.Load()
	if first == 0 {
		t.Fatal("nothing was downloaded")
	}

	// A jar from an older pack version is exactly what makes a server crash on a
	// duplicate mod, so a fetch has to remove it.
	stale := filepath.Join(dir, "mods", "stale-mod-0.9.jar")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Fetch(context.Background(), opts); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := api.downloads.Load(); got != first {
		t.Fatalf("second fetch re-downloaded %d file(s) that had not changed", got-first)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a jar the pack no longer has survived the fetch")
	}
}

func TestFetchFromAFileOnDisk(t *testing.T) {
	api := newModrinth(t)
	api.stage(t)

	// The overrides are irrelevant here; only the manifest source matters.
	path := filepath.Join(t.TempDir(), "Test Pack.mrpack")
	if err := os.WriteFile(path, api.mrpack, 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	res, err := Fetch(context.Background(), Options{Source: path, Dir: dir, MemoryGB: 4})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.Pack.VersionID != "ver1" || res.Pack.MC != "1.20.1" {
		t.Fatalf("pack.json = %+v", res.Pack)
	}
	if res.Pack.Slug == "" {
		t.Fatal("a pack with no project slug should still get a name-derived slug")
	}
	if api.apiCalls.Load() != 0 {
		t.Fatal("a local .mrpack made API calls")
	}
}

func TestFetchRefusesAnotherMinecraftVersionBeforeTheFileDownloads(t *testing.T) {
	api := newModrinth(t)
	api.stage(t)

	// The manifest is only readable once the .mrpack is down, but the 300 mod
	// files are not: a pack for a version outside the table should cost one
	// archive, not the whole pack. 1.21.1 is in the table (Java 21); 1.21.4 is not,
	// which is the case that must still be refused before the download.
	api.mrpack = zipBytes(t, Manifest{
		FormatVersion: 1,
		Dependencies:  map[string]string{"minecraft": "1.21.4", "fabric-loader": "0.16.0"},
	}, nil)

	_, err := Fetch(context.Background(), Options{Source: "test-pack", Dir: t.TempDir(), MemoryGB: 4})
	if err == nil || !strings.Contains(err.Error(), "1.20.1 (Java 17)") {
		t.Fatalf("want a refusal naming the versions this image boots, got %v", err)
	}
	if got := api.downloads.Load(); got != 0 {
		t.Fatalf("%d mod files were downloaded for a pack that cannot boot", got)
	}
}

func TestFetchRefusesAnArchiveWithNoManifest(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = io.WriteString(w, "not a pack")
	_ = zw.Close()

	path := filepath.Join(t.TempDir(), "bogus.mrpack")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Fetch(context.Background(), Options{Source: path, Dir: t.TempDir(), MemoryGB: 4})
	if err == nil || !strings.Contains(err.Error(), "modrinth.index.json") {
		t.Fatalf("want a not-a-Modrinth-pack error, got %v", err)
	}
}

func warningsContain(warnings []string, want string) bool {
	for _, w := range warnings {
		if strings.Contains(w, want) {
			return true
		}
	}
	return false
}
