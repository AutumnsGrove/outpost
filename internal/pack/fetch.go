package pack

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"outpost/internal/publish"
)

// workers is how many files are in flight at once. Modrinth's API limit is
// 300 requests a minute but mod files come from the CDN, so the useful cap is
// politeness rather than a quota.
const workers = 6

// Options is one fetch.
type Options struct {
	// Source is a Modrinth slug, a modrinth.com modpack URL, a .mrpack URL, or a
	// path to a .mrpack on disk.
	Source string

	// Version pins a pack version by id or version number. Empty takes the
	// newest release for the Minecraft version Outpost can boot.
	Version string

	// Dir is where the pack directory is written.
	Dir string

	// MemoryGB is the RAM of the box this pack is for. The .mrpack has no such
	// figure, so this is the one thing a person has to say; it becomes the heap
	// in scripts/start.sh and the number `publish` checks a plan against.
	MemoryGB int

	// SkipOptional leaves out files the pack marks `env.server: optional`.
	SkipOptional bool

	// Force re-downloads files whose hash already matches.
	Force bool

	Log *slog.Logger
}

// Result summarises a fetch for the CLI to print.
type Result struct {
	Pack publish.Pack

	// Files is everything that landed in the pack directory, Mods only the ones
	// in mods/.
	Files    int
	Mods     int
	Bytes    int64
	Skipped  []string
	Warnings []string
}

// target is one file to download and where it lands.
type target struct {
	rel  string // path inside the pack directory
	url  string
	algo string
	sum  string
	size int64 // what the pack says the file is, for catching a truncated answer
}

// Fetch materialises a pack directory from a Modrinth modpack.
func Fetch(ctx context.Context, opts Options) (Result, error) {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	var res Result
	if opts.Dir == "" {
		opts.Dir = "pack"
	}
	if opts.MemoryGB < 0 {
		return res, fmt.Errorf("--memory cannot be negative")
	}

	src, err := parseSource(opts.Source)
	if err != nil {
		return res, err
	}

	pk := publish.Pack{
		Source:    "modrinth",
		Slug:      src.slug,
		MemoryGB:  opts.MemoryGB,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Resolve before touching the disk: a pack for another Minecraft version
	// should cost a lookup, not a 300-file download.
	book, cleanup, err := openSource(ctx, src, opts.Version, &pk)
	if err != nil {
		return res, err
	}
	defer cleanup()

	zr, err := zip.NewReader(book.reader(), book.size)
	if err != nil {
		return res, fmt.Errorf("read %s: not a .mrpack (a zip): %w", book.label, err)
	}

	manifest, overrides, err := readMrpack(zr)
	if err != nil {
		return res, fmt.Errorf("%s: %w", book.label, err)
	}

	pk.MC = manifest.GameVersion()
	if pk.VersionID == "" {
		pk.VersionID = manifest.VersionID
	}
	if pk.Name == "" {
		pk.Name = manifest.Name
	}
	if pk.Slug == "" {
		pk.Slug = slugify(manifest.Name)
	}

	name, loaderVersion, err := manifest.Loader()
	if err != nil {
		return res, fmt.Errorf("%s: %w", pk.Describe(), err)
	}
	pk.Loader, pk.LoaderVersion = name, loaderVersion

	// The refusals that would otherwise be a rental, a Java stack trace, and a
	// deleted VPS. They match publish's, on purpose: catching it here saves the
	// download instead of the upload.
	if err := publish.CheckPack(pk); err != nil {
		return res, err
	}

	wanted, skipped, err := manifest.Wanted(opts.SkipOptional)
	if err != nil {
		return res, fmt.Errorf("%s: %w", pk.Describe(), err)
	}
	res.Skipped = skipped

	// A pack that says nothing about sides is a pack where every client mod lands
	// on the server. Fabulously Optimized is one; most server-capable packs are
	// not. The warning is the only honest thing to do when the pack has no opinion
	// — guessing from filenames is what client-only-mods.txt was, and it was wrong
	// more often than it was right.
	annotated := false
	for _, f := range manifest.Files {
		if f.Env != nil && f.Env.Server != "" {
			annotated = true
			break
		}
	}
	if !annotated && len(wanted) > 0 {
		res.Warnings = append(res.Warnings, "the pack does not mark any file as client-only, so every file is being installed on the server; if it is a client pack like Fabulously Optimized, the server will not load the client mods")
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return res, fmt.Errorf("create %s: %w", opts.Dir, err)
	}

	// Where everything goes, and what the pack directory should end up holding.
	written := map[string]bool{}
	targets := make([]target, 0, len(wanted))
	for _, f := range wanted {
		rel, err := place(f.Path)
		if err != nil {
			return res, fmt.Errorf("%s: %w", pk.Describe(), err)
		}
		algo, sum, hashed := f.Hash()
		if !hashed {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s carries no checksum; downloaded unverified", f.Path))
		}
		url := f.Downloads[0]
		targets = append(targets, target{rel: rel, url: url, algo: algo, sum: sum, size: f.FileSize})
		written[rel] = true
	}

	// Overrides after the indexed files: the pack author's own copy of a config
	// wins over the one a jar ships, which is the whole point of overrides.
	for _, f := range overrides {
		rel, ok, err := overridePath(f.Name)
		if err != nil {
			return res, fmt.Errorf("%s: %w", pk.Describe(), err)
		}
		if !ok {
			continue
		}
		if err := writeOverride(opts.Dir, f, rel); err != nil {
			return res, err
		}
		written[rel] = true
		if path.Base(rel) == "server.properties" {
			res.Warnings = append(res.Warnings, "the pack ships a server.properties; outpost generates its own (port, RCON, whitelist) and the pack's other settings there are not applied")
		}
	}

	if bytes, err := download(ctx, opts, targets, log); err != nil {
		return res, err
	} else {
		res.Bytes = bytes
	}
	res.Mods = len(targets)

	// A pack version that drops a mod has to drop the jar too, or the server
	// loads two versions of it and fails on the mixin.
	pruned, err := pruneLocal(opts.Dir, written)
	if err != nil {
		return res, err
	}
	if len(pruned) > 0 {
		log.Info("removed files the pack no longer has", "count", len(pruned))
	}

	// The start script belongs to the box, not the pack: it is the one place the
	// heap is decided, and publish's default is sized for a vanilla server.
	if err := os.MkdirAll(filepath.Join(opts.Dir, "scripts"), 0o755); err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join(opts.Dir, "scripts", "start.sh"), []byte(startScript(opts.MemoryGB)), 0o755); err != nil {
		return res, err
	}

	res.Files = len(written)
	pk.FileCount = res.Files
	if err := publish.WritePack(opts.Dir, pk); err != nil {
		return res, err
	}
	res.Pack = pk
	return res, nil
}

// book is an opened .mrpack: a local file or a downloaded temp file.
type book struct {
	file  *os.File
	size  int64
	label string
}

func (b book) reader() io.ReaderAt { return b.file }

// openSource turns a parsed source into an opened .mrpack, recording what it
// learned about the pack on the way.
func openSource(ctx context.Context, src source, wantVersion string, pk *publish.Pack) (book, func(), error) {
	noop := func() {}

	switch src.kind {
	case sourceFile:
		f, err := os.Open(src.path)
		if err != nil {
			return book{}, noop, fmt.Errorf("open %s: %w", src.path, err)
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return book{}, noop, err
		}
		return book{file: f, size: info.Size(), label: src.path}, func() { f.Close() }, nil

	case sourceURL:
		f, cleanup, err := openMrpack(ctx, src.url, "")
		if err != nil {
			return book{}, noop, err
		}
		info, err := f.Stat()
		if err != nil {
			cleanup()
			return book{}, noop, err
		}
		return book{file: f, size: info.Size(), label: src.url}, cleanup, nil

	case sourceProject:
		project, err := fetchProject(ctx, src.slug)
		if err != nil {
			return book{}, noop, err
		}
		if project.ProjectType != "" && project.ProjectType != "modpack" {
			return book{}, noop, fmt.Errorf("%s is a %s, not a modpack", src.slug, project.ProjectType)
		}

		versions, err := fetchVersions(ctx, src.slug)
		if err != nil {
			return book{}, noop, err
		}
		if wantVersion == "" {
			wantVersion = src.version
		}
		version, err := pickVersion(versions, wantVersion)
		if err != nil {
			return book{}, noop, fmt.Errorf("%s: %w", src.slug, err)
		}
		file, err := version.Mrpack()
		if err != nil {
			return book{}, noop, err
		}

		pk.Slug = project.Slug
		if pk.Slug == "" {
			pk.Slug = src.slug
		}
		pk.Name = project.Title
		pk.VersionID = version.ID
		pk.VersionNumber = version.VersionNumber

		f, cleanup, err := openMrpack(ctx, file.URL, file.Hashes["sha512"])
		if err != nil {
			return book{}, noop, err
		}
		info, err := f.Stat()
		if err != nil {
			cleanup()
			return book{}, noop, err
		}
		return book{file: f, size: info.Size(), label: file.Filename}, cleanup, nil
	}
	return book{}, noop, fmt.Errorf("unknown pack source")
}

// readMrpack pulls the manifest and the override entries out of the archive.
func readMrpack(zr *zip.Reader) (Manifest, []*zip.File, error) {
	var (
		manifest  Manifest
		overrides []*zip.File
		found     bool
	)

	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		switch {
		case name == "modrinth.index.json":
			rc, err := f.Open()
			if err != nil {
				return manifest, nil, err
			}
			raw, err := io.ReadAll(io.LimitReader(rc, 32<<20))
			rc.Close()
			if err != nil {
				return manifest, nil, err
			}
			m, err := ParseManifest(raw)
			if err != nil {
				return manifest, nil, err
			}
			manifest, found = m, true
		case strings.HasPrefix(name, "overrides/"), strings.HasPrefix(name, "server-overrides/"):
			overrides = append(overrides, f)
		}
		// client-overrides/ is deliberately not collected: it is the pack saying
		// "this is for the game client only".
	}

	if !found {
		return manifest, nil, errors.New("no modrinth.index.json in the archive: this is not a Modrinth pack")
	}

	// server-overrides/ is applied after overrides/, so it wins on a shared path.
	sort.SliceStable(overrides, func(i, j int) bool {
		return overrideRank(overrides[i].Name) < overrideRank(overrides[j].Name)
	})
	return manifest, overrides, nil
}

func overrideRank(name string) int {
	if strings.HasPrefix(strings.TrimPrefix(name, "./"), "server-overrides/") {
		return 1
	}
	return 0
}

// skippedOverride is what a dedicated server has no use for: client assets, and
// the files a game client writes about itself.
//
// saves/ earns its place the hard way: Cobblemon's pack ships its 73 MB tutorial
// world in overrides/, and uploading that to R2 is 73 MB of a server's boot spent
// on a world nobody will load. A server's world comes from mc-worlds, never from
// the pack.
var skippedOverride = []string{
	"resourcepacks/", "shaderpacks/", "saves/", "screenshots/", "logs/",
	"crash-reports/", "options.txt", "optionsof.txt", "servers.dat",
	"usercache.json", "icon.png", "instance.png", "banner.png", ".DS_Store",
}

// overridePath maps an archive entry to a pack path, and says whether the server
// wants it at all.
func overridePath(name string) (string, bool, error) {
	name = strings.TrimPrefix(name, "./")
	rel := strings.TrimPrefix(strings.TrimPrefix(name, "server-overrides/"), "overrides/")
	if rel == "" || strings.HasSuffix(rel, "/") {
		return "", false, nil // a directory entry
	}
	for _, skip := range skippedOverride {
		if rel == strings.TrimSuffix(skip, "/") || strings.HasPrefix(rel, skip) {
			return "", false, nil
		}
	}
	placed, err := place(rel)
	if err != nil {
		return "", false, err
	}
	return placed, true, nil
}

func writeOverride(dir string, f *zip.File, rel string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	dest := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("extract %s: %w", f.Name, err)
	}
	return nil
}

// place maps a path inside a pack to where it goes in the pack directory.
//
// mods/ is mods/. Everything else — config/, kubejs/, defaultconfigs/ — only
// works at the server root, so it goes under root/, which cloud-init copies to
// /opt/minecraft before the other prefixes and therefore loses to our
// server.properties.
func place(rel string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "/") ||
		clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q is not inside the pack", rel)
	}
	if clean == "mods" || strings.HasPrefix(clean, "mods/") {
		return clean, nil
	}
	return "root/" + clean, nil
}

// HeapFor is the -Xmx a box of this size gets: two GB stay with the OS, the
// JVM's own memory and the save, because a heap that fills the box swaps.
func HeapFor(memoryGB int) int {
	box := memoryGB
	if box <= 0 {
		box = 4
	}
	heap := box - 2
	if heap < 2 {
		heap = 2
	}
	return heap
}

// startScript is the launcher a fetched pack gets. publish's own default is
// sized for the vanilla server this project started with; a pack has to fit the
// box that was actually rented, and -Xmx is the one line that decides whether it
// does.
func startScript(memoryGB int) string {
	box := memoryGB
	if box <= 0 {
		box = 4
	}
	heap := HeapFor(box)
	init := heap / 2
	if init < 1 {
		init = 1
	}

	return fmt.Sprintf(`#!/bin/bash
# Written by outpost pack fetch: a %[1]d GB box, so a %[2]d GB heap.
# Leaving 2 GB for the OS, the JVM's own memory and the save. Raise --memory and
# re-fetch if the pack needs more; raising -Xmx past what the box has swaps.
cd /opt/minecraft
exec java -Xms%[3]dG -Xmx%[2]dG \
  -XX:+UseG1GC -XX:+ParallelRefProcEnabled -XX:MaxGCPauseMillis=200 \
  -XX:+UnlockExperimentalVMOptions -XX:+DisableExplicitGC -XX:+AlwaysPreTouch \
  -XX:G1NewSizePercent=30 -XX:G1MaxNewSizePercent=40 -XX:G1HeapRegionSize=8M \
  -XX:G1ReservePercent=20 -XX:G1HeapWastePercent=5 -XX:G1MixedGCCountTarget=4 \
  -XX:InitiatingHeapOccupancyPercent=15 -XX:G1MixedGCLiveThresholdPercent=90 \
  -XX:G1RSetUpdatingPauseTimePercent=5 -XX:SurvivorRatio=32 \
  -XX:+PerfDisableSharedMem -XX:MaxTenuringThreshold=1 \
  -jar fabric-server-launch.jar nogui
`, box, heap, init)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
