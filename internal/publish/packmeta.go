package publish

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SupportedMC is the only Minecraft version Outpost can boot today.
//
// internal/cloudinit installs openjdk-17, which stops at 1.20.4. A 1.21 pack
// fails at boot on "Unsupported class file major version 65", the orchestrator
// reads that as a server that never came up, and it deletes the VPS — a Java
// version showing up as a modpack problem twenty minutes into a rental. The two
// move together: change this and the packages list in the cloud-init template in
// the same commit.
const SupportedMC = "1.20.1"

// PackFile is the manifest `outpost pack fetch` leaves in a pack directory. Its
// absence is not an error: a directory of hand-picked jars is still a pack, and
// publish keeps working for it.
const PackFile = "pack.json"

// Pack records where a pack came from and what it needs to run. It exists so
// that publishing stops guessing: before this, the loader version was a flag
// somebody had to remember and "which pack made this world" had no answer.
type Pack struct {
	Source string `json:"source"` // "modrinth", or "" for a hand-rolled pack
	Slug   string `json:"slug"`
	Name   string `json:"name"`

	// VersionID is the immutable id of the pack version, and VersionNumber the
	// human one. Two fetches of the same number are the same build; the number
	// being edited in place is not a thing packs do.
	VersionID     string `json:"versionId"`
	VersionNumber string `json:"versionNumber"`

	MC            string `json:"mc"`
	Loader        string `json:"loader"` // "fabric"; forge later (see issue #12)
	LoaderVersion string `json:"loaderVersion"`

	// FileCount is how many files the pack put on the server, and MemoryGB the
	// RAM whoever ran `pack fetch` said the pack needs. The .mrpack manifest has
	// no RAM figure, so that number is a judgement call, not a fact from the pack.
	FileCount int `json:"fileCount"`
	MemoryGB  int `json:"memoryGB"`

	FetchedAt string `json:"fetchedAt"`
}

// Describe is the identity used in errors and the publish summary.
func (p Pack) Describe() string {
	name := p.Name
	if name == "" {
		name = p.Slug
	}
	if name == "" {
		name = "pack " + p.VersionID
	}
	if p.VersionNumber != "" {
		name += " " + p.VersionNumber
	}
	if p.Slug != "" && p.Source != "" {
		name += " (" + p.Source + "/" + p.Slug + ")"
	}
	return name
}

// SamePack reports whether two descriptions are the same pack, at any version.
// That is the question the world guard asks, and the answer changed when
// profiles arrived: within one profile, 1.5.1 → 1.5.2 is an update that keeps the
// world, which is the normal thing to do. A *different* pack in the same profile
// is what has to be asked about.
func (p Pack) SamePack(o Pack) bool {
	if p.Source == "" && o.Source == "" && p.Slug == "" && o.Slug == "" {
		return true // both unidentifiable: nothing says they differ
	}
	return p.Source == o.Source && p.Slug == o.Slug
}

// SameBuild reports whether two descriptions are the same build of the same
// pack. That is the question the world guard asks: a world saved under another
// build is not necessarily loadable, but the same build always is.
func (p Pack) SameBuild(o Pack) bool {
	if p.VersionID != "" || o.VersionID != "" {
		return p.Source == o.Source && p.Slug == o.Slug && p.VersionID == o.VersionID
	}
	return p.Source == o.Source && p.Slug == o.Slug && p.VersionNumber == o.VersionNumber
}

// LoadPack reads pack.json out of a pack directory. A directory without one is
// a hand-rolled pack, which returns ok=false rather than an error.
func LoadPack(dir string) (Pack, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, PackFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Pack{}, false, nil
	}
	if err != nil {
		return Pack{}, false, fmt.Errorf("read %s: %w", PackFile, err)
	}
	var p Pack
	if err := json.Unmarshal(raw, &p); err != nil {
		return Pack{}, false, fmt.Errorf("parse %s: %w", PackFile, err)
	}
	return p, true, nil
}

// WritePack writes pack.json into a pack directory.
func WritePack(dir string, p Pack) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, PackFile), raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", PackFile, err)
	}
	return nil
}

// VanillaPack is the identity of a server with no mods: Fabric, an empty
// mods/, the Minecraft version this image can boot.
//
// It is a pack.json like any other, with source "outpost" instead of
// "modrinth", which is what makes vanilla a first-class profile rather than a
// special case: the world guard, the pruning, the RAM check, the marker and
// `world import` all treat it as one thing — a pack. It is deliberately *not* the
// vanilla server jar from Mojang's manifest. A zero-mod Fabric server plays as
// vanilla and accepts vanilla clients, so a second jar name and a second
// start.sh would buy a difference no player can see, at the cost of a second
// boot path that nothing else in the system knows about.
func VanillaPack() Pack {
	return Pack{
		Source:        "outpost",
		Slug:          "vanilla",
		Name:          "Vanilla",
		VersionID:     SupportedMC,
		VersionNumber: SupportedMC,
		MC:            SupportedMC,
		Loader:        "fabric",
		FetchedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// CheckPack refuses a pack this server cannot run, before it rents or uploads
// anything. Exported because `outpost pack fetch` asks the same question before
// downloading 300 files instead of after.
func CheckPack(p Pack) error {
	if p.MC != "" && p.MC != SupportedMC {
		return fmt.Errorf("pack %s is Minecraft %s; outpost runs %s (cloud-init installs Java 17, which stops there)",
			p.Describe(), p.MC, SupportedMC)
	}
	if p.Loader != "" && !strings.EqualFold(p.Loader, "fabric") {
		return fmt.Errorf("pack %s needs the %s loader; only Fabric is supported so far", p.Describe(), p.Loader)
	}
	return nil
}
