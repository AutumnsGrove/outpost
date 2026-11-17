package pack

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// modMetaLimit is how much of a fabric.mod.json is read. Every one in the wild
	// is a few kilobytes; the cap is only so a crafted jar cannot ask for gigabytes.
	modMetaLimit = 4 << 20

	// nestedJarLimit is how much of a jar nested in META-INF/jars/ is read. A
	// bundled library is megabytes, not kilobytes, so this is generous — and a
	// nested jar past it marks the scan incomplete instead of being treated as
	// empty, because an unreadable file is not evidence of a missing mod.
	nestedJarLimit = 128 << 20
)

// loaderIDs are ids a mod may require that no jar in a pack provides: the loader
// and the game itself do. Anything else has to come from a mod.
var loaderIDs = map[string]bool{
	"minecraft":     true,
	"java":          true,
	"fabricloader":  true,
	"fabric-loader": true,
	"fabric":        true,
}

// modMeta is one mod's own account of itself, read out of a fabric.mod.json.
type modMeta struct {
	ID       string
	Provides []string
	Depends  map[string]string // id -> version range, for the error message only
	// Carrier is the file it came from: "chipped-…jar" or "mod.jar!nested.jar".
	Carrier string
}

// modJars is every mod a jar declares, including the ones nested in it.
//
// Nested jars are not a detail to skip: Fabric's jar-in-jar is where a mod's
// bundled dependencies live, and Fabric API is itself a shell of thirty-odd
// nested modules. Counting them is what stops a satisfied dependency looking
// missing.
type modJars struct {
	Mods []modMeta
	// Incomplete is set when a nested jar could not be read in full, so Mods is
	// some of what this jar provides and not all of it. A scan that met one of
	// these cannot be used to refuse a pack: "I did not see it" is not "it is not
	// there".
	Incomplete bool
}

// provides is every id this jar makes available.
func (m modJars) provides() []string {
	var out []string
	for _, mod := range m.Mods {
		if mod.ID != "" {
			out = append(out, mod.ID)
		}
		out = append(out, mod.Provides...)
	}
	return out
}

// readModJar reads the fabric.mod.json in p and in every jar p nests.
func readModJar(p string) (modJars, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return modJars{}, err
	}
	defer zr.Close()
	return readModFiles(zr.File, filepath.Base(p))
}

func readModFiles(files []*zip.File, carrier string) (modJars, error) {
	var out modJars
	for _, f := range files {
		switch {
		case f.Name == "fabric.mod.json":
			raw, err := readEntry(f, modMetaLimit)
			if err != nil {
				return out, fmt.Errorf("%s: %w", carrier, err)
			}
			mod, err := parseModMeta(raw)
			if err != nil {
				return out, fmt.Errorf("%s: %w", carrier, err)
			}
			mod.Carrier = carrier
			out.Mods = append(out.Mods, mod)
		case strings.HasPrefix(f.Name, "META-INF/jars/") && strings.HasSuffix(f.Name, ".jar"):
			// A nested jar is read whole. It used to share the metadata cap, which
			// meant every bundled library over 4 MiB was silently cut in half: the
			// zip would not open, its ids went unseen, and a pack that boots fine was
			// refused for a dependency that was sitting right there.
			if int64(f.UncompressedSize64) > nestedJarLimit {
				out.Incomplete = true
				continue
			}
			raw, err := readEntry(f, nestedJarLimit)
			if err != nil || int64(len(raw)) == nestedJarLimit {
				out.Incomplete = true
				continue
			}
			sub, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				// A nested jar that will not open is not a reason to refuse a pack;
				// it just cannot vouch for the ids inside it, so say the scan is
				// incomplete and let the boot be the judge.
				out.Incomplete = true
				continue
			}
			nested, err := readModFiles(sub.File, carrier+"!"+path.Base(f.Name))
			if err != nil {
				return out, err
			}
			out.Mods = append(out.Mods, nested.Mods...)
			out.Incomplete = out.Incomplete || nested.Incomplete
		}
	}
	return out, nil
}

func readEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// parseModMeta reads the three fields that matter. Every field is optional and
// "depends" has had three spellings in the wild, so each is tried rather than
// refused: a pack is not ours to validate, only to keep bootable.
func parseModMeta(raw []byte) (modMeta, error) {
	var doc struct {
		ID       string          `json:"id"`
		Provides json.RawMessage `json:"provides"`
		Depends  json.RawMessage `json:"depends"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return modMeta{}, fmt.Errorf("fabric.mod.json: %w", err)
	}
	return modMeta{
		ID:       doc.ID,
		Provides: stringList(doc.Provides),
		Depends:  idMap(doc.Depends),
	}, nil
}

// stringList reads ["a","b"] or {"a":"1.0"}.
func stringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err == nil {
		out := make([]string, 0, len(m))
		for id := range m {
			out = append(out, id)
		}
		sort.Strings(out)
		return out
	}
	return nil
}

// idMap reads {"a":">=1.0"} and the array forms of the same thing. The version
// range is kept for the message a person reads, not for computation.
func idMap(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]string{}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err == nil {
		return m
	}
	var objs []map[string]string
	if err := json.Unmarshal(raw, &objs); err == nil {
		for _, o := range objs {
			id, ok := o["id"]
			if !ok {
				continue
			}
			out[id] = o["version"]
		}
		return out
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err == nil {
		for _, id := range ids {
			out[id] = ""
		}
	}
	return out
}

// depGap is a dependency a mod requires and nothing in the pack provides.
type depGap struct {
	Mod     string // the mod that wants it
	Carrier string // the file that mod came from
	ID      string // what it wants
	Range   string // what it says it wants, for the message
}

func (g depGap) String() string {
	mod := g.Carrier
	if g.Mod != "" {
		mod = g.Mod + " (" + g.Carrier + ")"
	}
	if g.Range == "" {
		return fmt.Sprintf("%s requires %s", mod, g.ID)
	}
	return fmt.Sprintf("%s requires %s %s", mod, g.ID, g.Range)
}

// scanModDeps reads every jar in files and reports the dependencies nothing in
// those jars provides. files are paths relative to dir.
//
// The pack's own word is the input, not a resolution algorithm: a mod that says
// it requires something nothing provides is a mod that Fabric Loader will refuse
// to load, which is a server that never answers RCON.
//
// complete is false when some jar could not be read in full, and then the gaps
// are what was not *seen* rather than what is not *there*: the caller may warn
// about them, but refusing a pack on that evidence would refuse bootable packs.
func scanModDeps(dir string, files []string) (gaps []depGap, provided map[string]bool, complete bool, err error) {
	provided = map[string]bool{}
	complete = true
	jars := make([]modJars, 0, len(files))

	for _, rel := range files {
		if !strings.HasSuffix(strings.ToLower(rel), ".jar") {
			continue
		}
		jarsIn, err := readModJar(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			// A jar we cannot read is not a pack we can reason about, but refusing
			// the install over it is worse than letting the server try: the loader
			// will say exactly what is wrong in a log a person can read.
			complete = false
			continue
		}
		complete = complete && !jarsIn.Incomplete
		jars = append(jars, jarsIn)
		for _, id := range jarsIn.provides() {
			provided[id] = true
		}
	}

	seen := map[string]bool{}
	for _, jar := range jars {
		for _, mod := range jar.Mods {
			for id, rng := range mod.Depends {
				if provided[id] || loaderIDs[id] {
					continue
				}
				key := mod.Carrier + "\x00" + id
				if seen[key] {
					continue
				}
				seen[key] = true
				gaps = append(gaps, depGap{Mod: mod.ID, Carrier: mod.Carrier, ID: id, Range: rng})
			}
		}
	}
	sort.Slice(gaps, func(i, j int) bool {
		if gaps[i].Carrier != gaps[j].Carrier {
			return gaps[i].Carrier < gaps[j].Carrier
		}
		return gaps[i].ID < gaps[j].ID
	})
	return gaps, provided, complete, nil
}

// idsInJar is what a freshly downloaded jar provides, or nil if it is not a mod.
func idsInJar(p string) []string {
	jarsIn, err := readModJar(p)
	if err != nil {
		return nil
	}
	return jarsIn.provides()
}

// rescueHardDeps keeps a file the pack marked client-only when a mod the same
// pack marks server-required says it cannot load without it, and refuses the
// pack when nothing in it can satisfy that.
//
// The cost is paid only when something is actually missing: a pack whose kept
// mods are satisfied — which is nearly all of them — downloads nothing extra. A
// pack with a gap has its skipped mods fetched into a staging directory, read for
// the ids they provide, and only the ones that close a gap are moved into mods/.
// The rest are deleted, so the server does not load a client mod for no reason.
//
// Refusing is deliberate. A gap that survives this is a server that boots into a
// Fabric Loader crash loop, which reads as "Building" on the page until the
// provisioning timeout deletes the VPS. Failing here costs a look at the message
// instead of a rental.
func rescueHardDeps(ctx context.Context, opts Options, skipped []ManifestFile, written map[string]bool, res *Result, log *slog.Logger) error {
	rels := make([]string, 0, len(written))
	for rel := range written {
		rels = append(rels, rel)
	}
	gaps, _, complete, err := scanModDeps(opts.Dir, rels)
	if err != nil {
		return err
	}
	if len(gaps) == 0 {
		return nil
	}

	// Everything the pack holds back, staged once. Optional files are in here too:
	// --skip-optional can drop a mod another mod needs just as easily.
	staged, targets, cleanup, err := stageSkipped(ctx, opts, skipped)
	if err != nil {
		return err
	}
	defer cleanup()
	if len(targets) == 0 {
		return unmetDepsError(gaps)
	}
	if _, err := download(ctx, opts, targets, log); err != nil {
		return err
	}

	// id -> the staged file that provides it.
	inventory := map[string]stagedFile{}
	for _, st := range staged {
		for _, id := range idsInJar(filepath.Join(opts.Dir, filepath.FromSlash(st.rel))) {
			if _, taken := inventory[id]; !taken {
				inventory[id] = st
			}
		}
	}

	// A jar is moved at most once, however many ids it answers for. One file that
	// provides two needed ids used to be moved for the first gap and then renamed
	// again for the second — from a path it had already left.
	adopted := map[string]bool{}

	// Closure, not one pass: the file that closes a gap can open one of its own.
	// Ten rounds is far past anything a real pack needs and still bounded.
	for round := 0; round < 10 && len(gaps) > 0; round++ {
		moved := false
		for _, gap := range gaps {
			st, ok := inventory[gap.ID]
			if !ok {
				continue
			}
			if !adopted[st.finalRel] {
				if err := adoptStaged(opts.Dir, st.rel, st.finalRel); err != nil {
					return err
				}
				adopted[st.finalRel] = true
				written[st.finalRel] = true
				res.Mods++
				res.Bytes += st.size
				res.Planned = append(res.Planned, PlannedFile{Path: st.finalRel, Size: st.size, Source: "pack"})
				res.Skipped = withoutPath(res.Skipped, st.path)
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s is marked client-only by the pack, but %s requires it; kept so the server can start", st.path, gap.Mod))
				log.Info("kept a client-only mod another mod requires", "file", st.path, "required_by", gap.Mod)
			}
			// Every id that file provides is now installed, not just the one that
			// pulled it in.
			for id, provider := range inventory {
				if provider.finalRel == st.finalRel {
					delete(inventory, id)
				}
			}
			moved = true
		}
		if !moved {
			break
		}
		gaps, _, complete, err = scanModDeps(opts.Dir, relsOf(written))
		if err != nil {
			return err
		}
	}

	if len(gaps) > 0 {
		if !complete {
			// Something in the pack could not be read, so these are gaps in the
			// evidence rather than in the pack. Say so and let the boot decide: a
			// refusal here would block a pack that is already installed and fine.
			for _, gap := range gaps {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: could not confirm %s is provided, because a jar could not be read in full", gap.Carrier, gap.ID))
			}
			log.Warn("could not check every dependency", "gaps", len(gaps))
			return nil
		}
		return unmetDepsError(gaps)
	}
	return nil
}

// stagedFile is one skipped file that has a URL: where it is being downloaded to
// and where it would go if a gap needs it.
type stagedFile struct {
	path     string // the pack path, for the message
	rel      string // where the download lands, under .deps/
	finalRel string // where it goes in the pack directory if it is kept
	size     int64
}

// stageSkipped works out which skipped files could possibly close a gap: the Mods
// that carry a URL and a jar name. Configs and resource packs cannot provide a
// mod id, so they are not downloaded to find out.
func stageSkipped(ctx context.Context, opts Options, skipped []ManifestFile) ([]stagedFile, []target, func(), error) {
	var (
		staged  []stagedFile
		targets []target
	)
	for i, f := range skipped {
		if len(f.Downloads) == 0 || !strings.HasSuffix(strings.ToLower(f.Path), ".jar") || !strings.HasPrefix(f.Path, "mods/") {
			continue
		}
		finalRel, err := place(f.Path)
		if err != nil {
			return nil, nil, func() {}, fmt.Errorf("%s: %w", f.Path, err)
		}
		algo, sum, _ := f.Hash()
		st := stagedFile{
			path:     f.Path,
			rel:      fmt.Sprintf(".deps/%03d/%s", i, path.Base(finalRel)),
			finalRel: finalRel,
			size:     f.FileSize,
		}
		staged = append(staged, st)
		targets = append(targets, target{rel: st.rel, url: f.Downloads[0], algo: algo, sum: sum, size: f.FileSize})
	}
	cleanup := func() { _ = os.RemoveAll(filepath.Join(opts.Dir, ".deps")) }
	return staged, targets, cleanup, nil
}

// adoptStaged moves a staged jar to where the pack directory wants it.
func adoptStaged(dir, rel, finalRel string) error {
	dest := filepath.Join(dir, filepath.FromSlash(finalRel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, filepath.FromSlash(rel)), dest)
}

// unmetDepsError is the message a person gets instead of a crash loop.
func unmetDepsError(gaps []depGap) error {
	lines := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		lines = append(lines, "  - "+gap.String())
	}
	return fmt.Errorf("this pack cannot start a dedicated server: nothing in it provides what these mods require\n%s\nthe pack's own annotations disagree with its own mods, or a required mod is missing from it; outpost will not rent a box for a server that will not boot", strings.Join(lines, "\n"))
}

func relsOf(written map[string]bool) []string {
	out := make([]string, 0, len(written))
	for rel := range written {
		out = append(out, rel)
	}
	return out
}

func withoutPath(paths []string, drop string) []string {
	out := paths[:0]
	for _, p := range paths {
		if p != drop {
			out = append(out, p)
		}
	}
	return out
}
