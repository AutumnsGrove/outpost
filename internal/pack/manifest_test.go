package pack

import (
	"strings"
	"testing"

	"outpost/internal/publish"
)

func manifest() Manifest {
	return Manifest{
		FormatVersion: 1,
		VersionID:     "abc123",
		Name:          "Test Pack",
		Dependencies: map[string]string{
			"minecraft":     "1.20.1",
			"fabric-loader": "0.15.7",
		},
		Files: []ManifestFile{
			{
				Path:      "mods/create-1.0.jar",
				Downloads: []string{"https://example/create.jar"},
				Hashes:    map[string]string{"sha512": "abc"},
				Env:       &Env{Client: "required", Server: "required"},
			},
			{
				Path:      "mods/sodium-0.5.13.jar",
				Downloads: []string{"https://example/sodium.jar"},
				Hashes:    map[string]string{"sha512": "def"},
				Env:       &Env{Client: "required", Server: "unsupported"},
			},
			{
				Path:      "mods/jei-15.0.jar",
				Downloads: []string{"https://example/jei.jar"},
				Hashes:    map[string]string{"sha512": "ghi"},
				Env:       &Env{Client: "required", Server: "optional"},
			},
			{
				// No env at all means both sides need it.
				Path:      "mods/fabric-api-0.92.jar",
				Downloads: []string{"https://example/api.jar"},
			},
		},
	}
}

func TestWantedUsesThePacksOwnEnvField(t *testing.T) {
	wanted, skipped, err := manifest().Wanted(false)
	if err != nil {
		t.Fatalf("wanted: %v", err)
	}

	var paths []string
	for _, f := range wanted {
		paths = append(paths, f.Path)
	}
	want := []string{
		"mods/create-1.0.jar",
		"mods/fabric-api-0.92.jar",
		"mods/jei-15.0.jar", // optional, kept by default
	}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("wanted = %v, want %v", paths, want)
	}
	if len(skipped) != 1 || skipped[0] != "mods/sodium-0.5.13.jar" {
		t.Fatalf("skipped = %v, want the client-only mod", skipped)
	}

	// --skip-optional drops the optional one as well.
	wanted, skipped, err = manifest().Wanted(true)
	if err != nil {
		t.Fatalf("wanted: %v", err)
	}
	if len(wanted) != 2 || len(skipped) != 2 {
		t.Fatalf("wanted %d, skipped %v; optional should be skipped too", len(wanted), skipped)
	}
}

func TestWantedRefusesAFileWithNoURL(t *testing.T) {
	m := manifest()
	m.Files[0].Downloads = nil
	if _, _, err := m.Wanted(false); err == nil || !strings.Contains(err.Error(), "no download URL") {
		t.Fatalf("want a no-URL error, got %v", err)
	}
}

func TestParseManifestRefusesAFormatItDoesNotKnow(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"formatVersion":2}`)); err == nil {
		t.Fatal("format 2 was accepted")
	}
	if _, err := ParseManifest([]byte(`not json`)); err == nil {
		t.Fatal("garbage was accepted")
	}
}

func TestManifestLoaderAndGameVersion(t *testing.T) {
	m := manifest()
	if got := m.GameVersion(); got != "1.20.1" {
		t.Fatalf("game version = %q", got)
	}
	name, version, err := m.Loader()
	if err != nil {
		t.Fatal(err)
	}
	if name != "fabric" || version != "0.15.7" {
		t.Fatalf("loader = %q %q", name, version)
	}

	m.Dependencies = map[string]string{"minecraft": "1.20.1", "neoforge": "20.4.1"}
	if name, _, err := m.Loader(); err != nil || name != "neoforge" {
		t.Fatalf("neoforge loader = %q, %v", name, err)
	}

	m.Dependencies = map[string]string{"minecraft": "1.20.1"}
	if _, _, err := m.Loader(); err == nil {
		t.Fatal("a pack with no loader was accepted")
	}
}

func TestPickVersionPrefersAReleaseForTheVersionWeCanBoot(t *testing.T) {
	versions := []Version{
		{ID: "new", VersionNumber: "2.0.0", VersionType: "release", GameVersions: []string{"1.21.1"}, Loaders: []string{"fabric"}},
		{ID: "beta", VersionNumber: "1.9.0-beta", VersionType: "beta", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"}},
		{ID: "right", VersionNumber: "1.5.2", VersionType: "release", GameVersions: []string{"1.20.1"}, Loaders: []string{"fabric"}},
	}

	got, err := pickVersion(versions, "")
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got.ID != "right" {
		t.Fatalf("picked %s, want the 1.20.1 release over a newer 1.21 one", got.ID)
	}

	// A pinned version wins even when it is not the one we would have picked.
	got, err = pickVersion(versions, "1.9.0-beta")
	if err != nil || got.ID != "beta" {
		t.Fatalf("pinned pick = %+v, %v", got, err)
	}

	// Nothing for 1.20.1: say so and name what the pack does offer.
	_, err = pickVersion(versions[:1], "")
	if err == nil || !strings.Contains(err.Error(), "1.21.1") {
		t.Fatalf("want an error naming the versions on offer, got %v", err)
	}
}

func TestCheckPackRefusesAnotherMinecraftVersion(t *testing.T) {
	err := publish.CheckPack(publish.Pack{Name: "Pack", MC: "1.21.1", Loader: "fabric"})
	if err == nil || !strings.Contains(err.Error(), "Java 17") {
		t.Fatalf("want a Java 17 explanation, got %v", err)
	}

	if err := publish.CheckPack(publish.Pack{Name: "Pack", MC: "1.20.1", Loader: "fabric"}); err != nil {
		t.Fatalf("1.20.1 fabric was refused: %v", err)
	}
}
