package pack

import (
	"strings"
	"testing"
)

func TestParseSourceUnderstandsWhatPrismHandsYou(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    source
		wantErr string
	}{
		{
			name: "bare slug",
			in:   "cobblemon-fabric",
			want: source{kind: sourceProject, slug: "cobblemon-fabric"},
		},
		{
			name: "the link Prism's browser button gives you",
			in:   "https://modrinth.com/modpack/cobblemon-fabric",
			want: source{kind: sourceProject, slug: "cobblemon-fabric"},
		},
		{
			name: "a version page",
			in:   "https://modrinth.com/modpack/cobblemon-fabric/version/1.5.2",
			want: source{kind: sourceProject, slug: "cobblemon-fabric", version: "1.5.2"},
		},
		{
			name: "www and a trailing slash",
			in:   "https://www.modrinth.com/modpack/fabulously-optimized/",
			want: source{kind: sourceProject, slug: "fabulously-optimized"},
		},
		{
			name: "a direct .mrpack URL",
			in:   "https://cdn.modrinth.com/data/5FFgwNNP/versions/bpaivauC/Pack.mrpack",
			want: source{kind: sourceURL, slug: "Pack", url: "https://cdn.modrinth.com/data/5FFgwNNP/versions/bpaivauC/Pack.mrpack"},
		},
		{
			name: ".mrpack on disk",
			in:   "/tmp/Pack.mrpack",
			want: source{kind: sourceFile, path: "/tmp/Pack.mrpack"},
		},
		{
			name:    "CurseForge is a considered no",
			in:      "https://www.curseforge.com/minecraft/modpacks/all-the-mods-9",
			wantErr: "CurseForge",
		},
		{
			name:    "not a slug",
			in:      "All The Mods 9",
			wantErr: "not a Modrinth slug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSource(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want an error mentioning %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPlaceKeepsModsAndRootsEverythingElse(t *testing.T) {
	tests := map[string]string{
		"mods/create-1.0.jar":             "mods/create-1.0.jar",
		"config/create.toml":              "root/config/create.toml",
		"kubejs/server_scripts/x.js":      "root/kubejs/server_scripts/x.js",
		"defaultconfigs/create.toml":      "root/defaultconfigs/create.toml",
		"./config/create.toml":            "root/config/create.toml",
		"config/../mods/really-a-mod.jar": "mods/really-a-mod.jar",
	}
	for in, want := range tests {
		got, err := place(in)
		if err != nil {
			t.Fatalf("place %q: %v", in, err)
		}
		if got != want {
			t.Errorf("place %q = %q, want %q", in, got, want)
		}
	}

	for _, bad := range []string{"", ".", "/etc/passwd", "../outside.jar", "mods/../../outside.jar"} {
		if got, err := place(bad); err == nil {
			t.Errorf("place %q = %q, want an error", bad, got)
		}
	}
}

func TestOverridePathDropsClientAssets(t *testing.T) {
	tests := map[string]struct {
		want   string
		wantOK bool
	}{
		"overrides/config/create.toml":        {"root/config/create.toml", true},
		"server-overrides/config/create.toml": {"root/config/create.toml", true},
		"overrides/resourcepacks/pack.zip":    {"", false},
		"overrides/shaderpacks/shiny.zip":     {"", false},
		"overrides/options.txt":               {"", false},
		"overrides/servers.dat":               {"", false},
		"overrides/config/":                   {"", false},
		"overrides/mods/kubejs-forge-1.0.jar": {"mods/kubejs-forge-1.0.jar", true},
	}
	for in, want := range tests {
		got, ok, err := overridePath(in)
		if err != nil {
			t.Fatalf("overridePath %q: %v", in, err)
		}
		if ok != want.wantOK || got != want.want {
			t.Errorf("overridePath %q = (%q, %v), want (%q, %v)", in, got, ok, want.want, want.wantOK)
		}
	}
}

func TestHeapForLeavesTwoGigabytesForTheBox(t *testing.T) {
	tests := map[int]int{0: 2, 2: 2, 4: 2, 8: 6, 16: 14}
	for box, want := range tests {
		if got := HeapFor(box); got != want {
			t.Errorf("HeapFor(%d) = %d, want %d", box, got, want)
		}
	}
}
