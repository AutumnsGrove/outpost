// Package pack turns a Modrinth modpack into the pack directory `outpost
// publish` uploads.
//
// The pack decides the three things a hand-managed mods/ folder gets wrong: the
// exact jar versions, the loader version, and which files a dedicated server
// must not load. The last one is the interesting one — `env.server` in the
// .mrpack manifest replaces internal/publish/client-only-mods.txt, which is a
// list of filenames somebody had to keep up to date and could not have known
// about a pack that did not exist yet.
//
// What it does not decide: RAM. A .mrpack says nothing about memory, so the
// figure in pack.json is a judgement call by whoever ran the fetch, and
// `publish` is what refuses a plan that cannot hold it.
package pack

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Manifest is modrinth.index.json, the file at the root of a .mrpack.
type Manifest struct {
	FormatVersion int               `json:"formatVersion"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Summary       string            `json:"summary"`
	Dependencies  map[string]string `json:"dependencies"`
	Files         []ManifestFile    `json:"files"`
}

// ManifestFile is one file the pack wants on disk, with the URLs that serve it.
type ManifestFile struct {
	Path      string            `json:"path"`
	Hashes    map[string]string `json:"hashes"`
	Env       *Env              `json:"env"`
	Downloads []string          `json:"downloads"`
	FileSize  int64             `json:"fileSize"`
}

// Env is the pack author saying what a file is for. A value of "unsupported"
// means the other side must not load it at all; "optional" means it works there
// but the player may leave it out.
type Env struct {
	Client string `json:"client"`
	Server string `json:"server"`
}

// ParseManifest decodes and sanity-checks modrinth.index.json.
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("parse modrinth.index.json: %w", err)
	}
	if m.FormatVersion != 1 {
		return m, fmt.Errorf("modrinth.index.json is format %d; this knows format 1", m.FormatVersion)
	}
	return m, nil
}

// GameVersion is the Minecraft version the pack is built for.
func (m Manifest) GameVersion() string { return m.Dependencies["minecraft"] }

// Loader returns the mod loader the pack wants and its version. Modrinth writes
// the loader as a dependency key, and only one of them is ever set.
func (m Manifest) Loader() (name, version string, err error) {
	for _, key := range []string{"fabric-loader", "quilt-loader", "neoforge", "forge"} {
		if v, ok := m.Dependencies[key]; ok && v != "" {
			return strings.TrimSuffix(key, "-loader"), v, nil
		}
	}
	return "", "", fmt.Errorf("modrinth.index.json names no loader (dependencies: %v)", m.Dependencies)
}

// Wanted is what a dedicated server has to download, in path order.
//
// Client-only files are dropped on the pack's own word rather than on a list of
// filenames, and optional files are kept unless skipOptional says otherwise:
// server packs routinely mark "nice to have" mods that the pack's own configs
// then reference.
func (m Manifest) Wanted(skipOptional bool) (wanted []ManifestFile, skipped []ManifestFile, err error) {
	for _, f := range m.Files {
		if f.Path == "" {
			return nil, nil, fmt.Errorf("modrinth.index.json has a file with no path")
		}
		side := "required"
		if f.Env != nil && f.Env.Server != "" {
			side = f.Env.Server
		}
		switch side {
		case "unsupported":
			skipped = append(skipped, f)
			continue
		case "optional":
			if skipOptional {
				skipped = append(skipped, f)
				continue
			}
		}
		if len(f.Downloads) == 0 {
			// A file with no URL cannot be fetched, and silently leaving it out is
			// a server that boots with a mod missing and a config that mentions it.
			return nil, nil, fmt.Errorf("%s has no download URL in the pack", f.Path)
		}
		wanted = append(wanted, f)
	}
	sort.Slice(wanted, func(i, j int) bool { return wanted[i].Path < wanted[j].Path })
	return wanted, skipped, nil
}

// Paths is the skip list as names, which is what a page prints and what
// Result.Skipped carries. The files themselves are what rescueHardDeps needs.
func Paths(files []ManifestFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// Hash returns the strongest checksum the pack recorded for a file. Every
// Modrinth pack carries sha1 and sha512; a pack that carries neither is fetched
// unverified, which the caller warns about.
func (f ManifestFile) Hash() (algo, sum string, ok bool) {
	for _, algo := range []string{"sha512", "sha1", "sha256", "md5"} {
		if v := f.Hashes[algo]; v != "" {
			return algo, strings.ToLower(v), true
		}
	}
	return "", "", false
}
