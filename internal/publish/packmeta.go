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

// Minecraft is one Minecraft version this image can boot, and the JDK it needs.
//
// A 1.21 pack on Java 17 fails at boot on "Unsupported class file major version
// 65", the orchestrator reads that as a server that never came up, and it deletes
// the VPS — a Java version showing up as a modpack problem twenty minutes into a
// rental. So the version and its JDK are one row here, never two facts that can
// drift: internal/cloudinit installs exactly the JDK of the row a pack names, and
// the orchestrator reads that row from the profile's pack.json before it rents.
//
// The table is a list of versions somebody has vouched for, not every version
// that exists. Adding a row says "a shared world can live here", so the next one
// is a decision (a Java version, a Fabric build, a boot) rather than a line.
// Minecraft versions are not interchangeable either: a world made in 1.20.1 does
// not open in 1.21.1, which is why the version a profile holds is checked and not
// just the pack (see worldGuard).
//
// Oldest first, so [0] is the default and vanilla's version, and tagged because
// the panel renders the table rather than hardcoding a version of its own.
type Minecraft struct {
	MC   string `json:"mc"`
	Java int    `json:"java"`
}

// Minecrafts is every Minecraft version Outpost can boot.
var Minecrafts = []Minecraft{
	{MC: "1.20.1", Java: 17},
	{MC: "1.21.1", Java: 21},
}

// SupportedMC is the Minecraft version a pack that names none is assumed to be
// for, and the one vanilla is when nobody says otherwise: the oldest row, which
// is also the one every world saved so far was made in. `pack fetch` without
// --version keeps resolving to it, so the default fetch does not move somebody's
// world.
//
// It is a default, not a ceiling. Vanilla can be asked for any row of the table
// (VanillaPack); naming none has to keep meaning this one, or the day a third row
// is added is the day a plain `outpost pack vanilla` replaces a world.
const SupportedMC = "1.20.1"

// MinecraftFor is the table's spelling of a Minecraft version, and whether the
// table has it at all.
//
// The spelling matters: "1.21.1" and "1.21.1 " are the same game, but pack.json is
// compared with ChangedMinecraft and shown in the panel, so a version reaches the
// manifest as the table writes it rather than as somebody typed it.
func MinecraftFor(mc string) (string, bool) {
	for _, m := range Minecrafts {
		if strings.EqualFold(m.MC, strings.TrimSpace(mc)) {
			return m.MC, true
		}
	}
	return "", false
}

// JavaFor is the JDK a Minecraft version needs, and whether this image can boot
// it at all.
func JavaFor(mc string) (int, bool) {
	for _, m := range Minecrafts {
		if strings.EqualFold(m.MC, mc) {
			return m.Java, true
		}
	}
	return 0, false
}

// Runnable reports whether this Minecraft version is one the image can boot.
func Runnable(mc string) bool {
	_, ok := JavaFor(mc)
	return ok
}

// MinecraftList is the table as prose, for the refusals and the panel: "1.20.1
// (Java 17), 1.21.1 (Java 21)". One wording, because a refusal and a greyed-out
// row that disagree are worse than either alone.
func MinecraftList() string {
	out := make([]string, 0, len(Minecrafts))
	for _, m := range Minecrafts {
		out = append(out, fmt.Sprintf("%s (Java %d)", m.MC, m.Java))
	}
	return strings.Join(out, ", ")
}

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

// handRolledPack describes a pack directory with no pack.json: server/, mods/
// and config/ somebody curated, or a bare `publish --fabric 1.21.1` with nothing
// but a launcher.
//
// It deliberately names no source and no slug. Two hand-rolled builds are not "a
// different pack" in SamePack's terms (source+slug), which is what lets a curated
// directory be republished over its own world without the guard treating each
// upload as a pack swap. What it does say is the Minecraft version, because that
// is the one thing a boot and a world both need to agree on, and the flag is the
// only place a hand-rolled pack can say it.
//
// No FetchedAt on purpose: nothing was fetched, and a timestamp here would make
// every republish of an unchanged directory upload a byte-different manifest.
func handRolledPack(opts Options, files int) Pack {
	mc := opts.FabricMC
	if mc == "" {
		mc = SupportedMC
	}
	return Pack{
		Name:          "Hand-rolled",
		MC:            mc,
		Loader:        "fabric",
		LoaderVersion: opts.FabricLoader,
		FileCount:     files,
	}
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
// mods/, and the Minecraft version it was asked for — SupportedMC when nobody
// names one.
//
// It is a pack.json like any other, with source "outpost" instead of
// "modrinth", which is what makes vanilla a first-class profile rather than a
// special case: the world guard, the pruning, the RAM check, the marker and
// `world import` all treat it as one thing — a pack. It is deliberately *not* the
// vanilla server jar from Mojang's manifest. A zero-mod Fabric server plays as
// vanilla and accepts vanilla clients, so a second jar name and a second
// start.sh would buy a difference no player can see, at the cost of a second
// boot path that nothing else in the system knows about.
//
// The version is a parameter rather than a constant because the image boots more
// than one (Minecrafts) and the panel offers the table. CheckPack is what refuses
// a version outside it; this function only spells it the way the table does.
func VanillaPack(mc string) Pack {
	if canonical, ok := MinecraftFor(mc); ok {
		mc = canonical
	}
	if strings.TrimSpace(mc) == "" {
		mc = SupportedMC
	}
	return Pack{
		Source:        "outpost",
		Slug:          "vanilla",
		Name:          "Vanilla",
		VersionID:     mc,
		VersionNumber: mc,
		MC:            mc,
		Loader:        "fabric",
		FetchedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// CheckPack refuses a pack this server cannot run, before it rents or uploads
// anything. Exported because `outpost pack fetch` asks the same question before
// downloading 300 files instead of after.
func CheckPack(p Pack) error {
	if p.MC != "" && !Runnable(p.MC) {
		return fmt.Errorf("pack %s is Minecraft %s; outpost boots %s — a version outside that table has no JDK here, so it is not offered",
			p.Describe(), p.MC, MinecraftList())
	}
	if p.Loader != "" && !strings.EqualFold(p.Loader, "fabric") {
		return fmt.Errorf("pack %s needs the %s loader; only Fabric is supported so far", p.Describe(), p.Loader)
	}
	return nil
}

// ChangedMinecraft reports whether two descriptions disagree about the Minecraft
// version. Two empty ones agree: a hand-rolled pack that names no version is not
// evidence of a change.
//
// This is the question a world cannot answer with "probably fine". Block ids and
// registry entries move between Minecraft versions, and the server either
// migrates the world with no way back or refuses to open it at all.
func ChangedMinecraft(a, b Pack) bool {
	if a.MC == "" || b.MC == "" {
		return false
	}
	return !strings.EqualFold(a.MC, b.MC)
}
