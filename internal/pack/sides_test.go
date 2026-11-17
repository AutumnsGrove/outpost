package pack

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectIDComesFromTheModrinthCDN(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://cdn.modrinth.com/data/AANobbMI/versions/x1/sodium.jar", "AANobbMI"},
		{"http://cdn.modrinth.com/data/abc123/versions/x/y.jar", "abc123"},
		{"https://github.com/someone/mod/releases/download/v1/mod.jar", ""},
		{"https://cdn.modrinth.com/data/", ""},
		{"", ""},
	} {
		if got := projectID(tc.url); got != tc.want {
			t.Fatalf("projectID(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// a stub of Modrinth's per-project sides, which no fixture can host: the CDN host
// is part of the answer, not something a test server can stand in for.
func stubSides(t *testing.T, sides map[string]string) {
	t.Helper()
	old := sidesLookup
	sidesLookup = func(_ context.Context, id string) (string, bool) {
		side, ok := sides[id]
		return side, ok
	}
	t.Cleanup(func() { sidesLookup = old })
}

func TestDropServerUnsupportedDropsWhatModrinthCallsClientOnly(t *testing.T) {
	stubSides(t, map[string]string{
		"AANobbMI": "unsupported", // sodium
		"gvQqBUqZ": "optional",    // lithium, runs anywhere
		"P7dR8mSH": "required",    // fabric-api
	})

	wanted := []ManifestFile{
		{Path: "mods/sodium.jar", Downloads: []string{"https://cdn.modrinth.com/data/AANobbMI/versions/1/sodium.jar"}},
		{Path: "mods/lithium.jar", Downloads: []string{"https://cdn.modrinth.com/data/gvQqBUqZ/versions/1/lithium.jar"}},
		{Path: "mods/fabric-api.jar", Downloads: []string{"https://cdn.modrinth.com/data/P7dR8mSH/versions/1/api.jar"}},
		{Path: "mods/elsewhere.jar", Downloads: []string{"https://example.test/elsewhere.jar"}},
	}

	var res Result
	kept, dropped := dropServerUnsupported(context.Background(), wanted, &res, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if len(dropped) != 1 || dropped[0].Path != "mods/sodium.jar" {
		t.Fatalf("dropped = %+v, want just sodium", dropped)
	}
	if len(kept) != 3 {
		t.Fatalf("kept = %d, want 3", len(kept))
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "mods/sodium.jar" {
		t.Fatalf("skipped = %v", res.Skipped)
	}
}

// A pack we cannot ask about is installed as the pack says: an API hiccup must not
// silently empty a server.
func TestDropServerUnsupportedKeepsWhatItCannotAskAbout(t *testing.T) {
	stubSides(t, map[string]string{})

	wanted := []ManifestFile{
		{Path: "mods/mystery.jar", Downloads: []string{"https://cdn.modrinth.com/data/unknown1/versions/1/m.jar"}},
	}
	var res Result
	kept, dropped := dropServerUnsupported(context.Background(), wanted, &res, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(dropped) != 0 || len(kept) != 1 {
		t.Fatalf("kept %d dropped %d; an unanswered question keeps the mod", len(kept), len(dropped))
	}
}

// stubProjectIDs maps a filename to a project id, so a fetch through the fake CDN
// can still exercise the side filter and the wiring that calls it.
func stubProjectIDs(t *testing.T, byNeedle map[string]string) {
	t.Helper()
	old := projectOfURL
	projectOfURL = func(url string) string {
		for needle, id := range byNeedle {
			if strings.Contains(url, needle) {
				return id
			}
		}
		return ""
	}
	t.Cleanup(func() { projectOfURL = old })
}

// The Fabulously Optimized shape end to end: the pack marks everything server
// required, Modrinth says most of it is client-only, and the two facts are
// reconciled before anything is downloaded.
func TestFetchSkipsTheModsModrinthCallsClientOnly(t *testing.T) {
	stubProjectIDs(t, map[string]string{"sodium": "AANobbMI", "lithium": "gvQqBUqZ"})
	stubSides(t, map[string]string{"AANobbMI": "unsupported", "gvQqBUqZ": "optional"})
	api := newModrinth(t)
	api.stageJars(t, map[string]stagedMod{
		"sodium-1.0.jar":  {body: modJar(t, "sodium", nil, nil)},
		"lithium-1.0.jar": {body: modJar(t, "lithium", nil, nil)},
	})

	dir := t.TempDir()
	res, err := Fetch(context.Background(), Options{Source: "test-pack", Dir: dir, MemoryGB: 4})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", "sodium-1.0.jar")); !os.IsNotExist(err) {
		t.Fatal("a mod Modrinth calls client-only was installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", "lithium-1.0.jar")); err != nil {
		t.Fatalf("a mod that runs on a server was dropped: %v", err)
	}
	if res.Mods != 1 {
		t.Fatalf("mods = %d, want 1", res.Mods)
	}
}
