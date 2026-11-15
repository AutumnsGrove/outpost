// Package publish fills the R2 buckets with the files an Outpost server boots
// from.
//
// It owns the key layout — the one internal/cloudinit reads — so that layout is
// written down in exactly one place on this side of the network:
//
//	mc-assets/server/{fabric-server-launch.jar,server.properties,ops.json,whitelist.json,eula.txt}
//	mc-assets/mods/*.jar
//	mc-assets/config/
//	mc-assets/scripts/{start.sh,stop.sh}
//	mc-assets/root/            a fetched modpack's non-mod files: config/, kubejs/, defaultconfigs/
//
// A missing world is not this package's problem: mc-worlds/current/world.tar.gz
// is written by the VPS on shutdown, and its absence just means Minecraft
// generates a fresh one. What publish does write there is
// mc-worlds/current/pack.json, the identity of the pack the assets belong to,
// which is how the next publish knows whether the saved world can be loaded with
// the pack it is about to upload.
package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"outpost/internal/config"
	"outpost/internal/r2"
)

// ErrNotFound is re-exported so a Store implementation can answer "no such key"
// without importing r2.
var ErrNotFound = r2.ErrNotFound

// DefaultProfile is what a publish without --profile writes to, and what a
// database that has never been told otherwise boots. It has to stay a valid
// name so the migration from the single-profile layout is a no-op.
const DefaultProfile = "default"

// profileName is what a profile id may look like. It lands in shell commands
// (the cloud-init pulls and the shutdown upload) and in R2 keys, so this is an
// allowlist rather than a check for "obviously bad".
var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidateProfile refuses a name that could not be a key or a shell argument.
func ValidateProfile(name string) error {
	if !profileName.MatchString(name) {
		return fmt.Errorf("profile %q is not a name (lowercase letters, digits, - and _, up to 32)", name)
	}
	return nil
}

// ProfileFromSlug is the profile a pack is installed into when nobody says: its
// own slug, so `outpost publish` after `outpost pack fetch` lands in
// profiles/cobblemon-fabric/ without anyone having to name it twice.
func ProfileFromSlug(slug string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, slug)
	clean = strings.Trim(clean, "-_")
	if len(clean) > 32 {
		clean = strings.TrimRight(clean[:32], "-_")
	}
	if clean == "" {
		return DefaultProfile
	}
	return clean
}

// ProfileOf returns the profile a run is scoped to.
func (o Options) ProfileOf() string {
	if o.Profile == "" {
		return DefaultProfile
	}
	return o.Profile
}

// profilePrefix is where one profile's files live in both buckets.
func profilePrefix(profile string) string { return "profiles/" + profile + "/" }

// playerKeys are the two files that belong to the server rather than to a
// profile. internal/roster writes them; cloud-init copies them last, over
// whatever a pack shipped.
var playerKeys = map[string]bool{
	"server/whitelist.json": true,
	"server/ops.json":       true,
}

// PlayerSet is the two player files as they are stored.
type PlayerSet struct {
	Whitelist []byte
	Ops       []byte
}

// ReadPlayers reads the server-wide whitelist and ops files. A missing ops.json is
// an empty list, because a server that has been played on but never opped anybody
// is the normal state.
func ReadPlayers(ctx context.Context, st Store, bucket string) (*PlayerSet, error) {
	whitelist, err := st.Get(ctx, bucket, "server/whitelist.json")
	if err != nil {
		return nil, err
	}
	ops, err := st.Get(ctx, bucket, "server/ops.json")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if ops == nil {
		ops = []byte("[]")
	}
	return &PlayerSet{Whitelist: whitelist, Ops: ops}, nil
}

//go:embed client-only-mods.txt
var defaultClientOnlyList string

// Store is the slice of R2 publish needs. The real implementation is *r2.Client;
// tests use a map.
type Store interface {
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64, sha256 string) error
	Get(ctx context.Context, bucket, key string) ([]byte, error)
	Stat(ctx context.Context, bucket, key string) (r2.Info, error)
	List(ctx context.Context, bucket, prefix string) ([]r2.Object, error)
	Remove(ctx context.Context, bucket, key string) error
}

// Options is one publish run.
type Options struct {
	// Dir is the pack directory: server/, mods/, config/, scripts/, root/.
	Dir         string
	AssetBucket string
	WorldBucket string

	// Profile scopes every key in both buckets as profiles/<Profile>/... A
	// profile is one pack and one world, so a second profile is a second game to
	// switch to without either world landing in the other. Empty means
	// "default", which is also what hand-rolled packs use.
	Profile string

	// FabricMC, when set (e.g. "1.20.1"), fetches a Fabric server launcher for
	// that Minecraft version and uploads it as server/fabric-server-launch.jar.
	// FabricLoader pins the loader; empty means the newest stable one.
	FabricMC     string
	FabricLoader string

	// Whitelist and Ops are Minecraft names. They are resolved to UUIDs through
	// Mojang at publish time, so the repo never holds a stale uuid.
	Whitelist []string
	Ops       []string

	// Players, when set, is published verbatim instead of resolving names. The
	// daemon uses it to reuse the roster it already has: Mojang rate-limits, and
	// the bucket is the authoritative copy anyway (PLAN 18).
	Players *PlayerSet

	// ReusePlayers publishes the player list already in the asset bucket when
	// nothing else supplies one: no Players, no Whitelist/Ops, no --open, and no
	// player file in the pack directory. Re-publishing an installed pack is
	// exactly that case, and the roster is the authority on those two keys, so
	// resolving the same names again through Mojang only adds a 429.
	//
	// A pack that ships its own server/whitelist.json is taken at its word: it
	// was authored with a list in mind, and the bucket's list is for packs that
	// were not.
	ReusePlayers bool

	// Open publishes a server with white-list=false. Needs saying out loud,
	// because the default is the opposite.
	Open bool

	// DryRun reports what would change without touching R2.
	DryRun bool

	// Prune deletes mods/ and config/ keys that are not in the pack. Without it
	// renaming a jar leaves the old one for the server to load. A pack with a
	// pack.json turns this on by itself: two versions of the same mod in mods/ is
	// a discovered-then-crashed server, not a warning.
	Prune bool

	// Regions are the plans a session can be rented on, used only to refuse a
	// pack none of them has the RAM for. Empty skips that check.
	Regions map[string]config.Region

	// Progress is called after each uploaded key, for a page that has to show
	// something for two minutes of uploading.
	Progress func(done, total int)

	// NewWorld discards the saved world when it belongs to another pack, and
	// KeepWorld loads it anyway. Neither is set: publish refuses instead of
	// guessing, because a world from a different pack is a world with missing
	// mods and renumbered blocks.
	NewWorld  bool
	KeepWorld bool

	Log *slog.Logger
}

// Object is one file to upload, either from disk or generated in memory.
type Object struct {
	Key     string
	Path    string
	Content []byte
	Size    int64
}

// Result summarises a run for the CLI to print.
type Result struct {
	Uploaded  []string
	Unchanged []string
	Removed   []string
	Warnings  []string
	Launcher  string
	TotalSize int64

	// Players is who the uploaded whitelist.json allows. It is the answer to the
	// only question that matters when nobody can join.
	Players []string
}

// Preflight is everything publish can decide before it uploads a byte: whether
// the pack can run here, whether the plan has the RAM, and whose world is saved
// in this profile. The page calls it to show a warning before someone commits to
// a 300 MB download, and Run calls it so there is one answer, not two.
type Preflight struct {
	// Pack is nil for a hand-rolled pack directory.
	Pack *Pack

	Profile string

	// WorldPack is the pack the saved world was built with, when the marker says
	// so. Nil means no world, or no readable marker.
	WorldPack *Pack

	// Discard is set when --new-world asked for a fresh world and there is one to
	// remove. Removal happens after a successful upload.
	Discard bool

	Warnings []string
}

// Inspect answers the questions that stop a publish before it starts.
func Inspect(ctx context.Context, st Store, opts Options) (Preflight, error) {
	pf := Preflight{Profile: opts.ProfileOf()}

	pk, packed, err := LoadPack(opts.Dir)
	if err != nil {
		return pf, err
	}
	if !packed {
		return pf, nil
	}
	return InspectPack(ctx, st, opts, pk)
}

// InspectPack is Inspect for a pack that is not on disk yet, which is what the
// page has after it resolves a link and before anyone commits to the download.
func InspectPack(ctx context.Context, st Store, opts Options, pk Pack) (Preflight, error) {
	pf := Preflight{Profile: opts.ProfileOf()}

	if err := CheckPack(pk); err != nil {
		return pf, err
	}
	memWarnings, err := checkPackMemory(pk, opts.Regions)
	if err != nil {
		return pf, err
	}
	pf.Warnings = append(pf.Warnings, memWarnings...)

	discard, worldPack, warnings, err := worldGuard(ctx, st, opts, pk)
	if err != nil {
		return pf, err
	}
	pf.Pack = &pk
	pf.Discard = discard
	pf.WorldPack = worldPack
	pf.Warnings = append(pf.Warnings, warnings...)
	return pf, nil
}

// Run uploads the pack, verifies it, and prunes what the pack no longer has.
func Run(ctx context.Context, st Store, opts Options) (Result, error) {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	var res Result
	var discardWorld bool

	// A pack knows things a flag list cannot: which Minecraft version it is for,
	// which loader it wants, and how much RAM the person who fetched it said it
	// needs. Read that before anything is uploaded, so the refusals happen before
	// the bytes and before the rental.
	pf, err := Inspect(ctx, st, opts)
	if err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, pf.Warnings...)
	discardWorld = pf.Discard

	if pk := pf.Pack; pk != nil {
		// Pruning is not optional for a pack: the previous version's jars have to
		// go, or the server loads two of everything.
		opts.Prune = true
		if pk.MC != "" {
			if opts.FabricMC != "" && opts.FabricMC != pk.MC {
				res.Warnings = append(res.Warnings, fmt.Sprintf("--fabric %s ignored: %s is for Minecraft %s", opts.FabricMC, pk.Describe(), pk.MC))
			}
			opts.FabricMC = pk.MC
			opts.FabricLoader = pk.LoaderVersion
		}
		if discardWorld && opts.DryRun {
			res.Warnings = append(res.Warnings, "dry run: would discard the saved world and start a fresh one")
		}
	}

	// The player list, when nobody named one. Build cannot do this itself: it
	// never sees the bucket, and the whole point is not to ask Mojang again.
	reusedPlayers := false
	if opts.ReusePlayers && !opts.Open && opts.Players == nil &&
		len(opts.Whitelist) == 0 && len(opts.Ops) == 0 && !packShipsPlayers(opts.Dir) {
		players, err := ReadPlayers(ctx, st, opts.AssetBucket)
		switch {
		case errors.Is(err, ErrNotFound):
			// No list anywhere: Build refuses below, and its message names the
			// three ways to supply one.
		case err != nil:
			return res, err
		case len(whitelistNamesIn(players.Whitelist)) == 0:
			// An empty whitelist is the roster's way of saying "nobody", and a server
			// nobody may join is exactly what Build's refusal exists to prevent. It is
			// also a normal state — removing the last player writes `[]` — so it must
			// not be treated as a list worth republishing.
		default:
			opts.Players = players
			reusedPlayers = true
		}
	}

	objects, warnings, err := Build(ctx, opts)
	if err != nil {
		return res, err
	}

	res.Warnings = append(res.Warnings, warnings...)
	res.Players = whitelistNames(objects)
	if reusedPlayers {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"reused the player list already in %s (%d name(s), operators included) instead of resolving them again",
			opts.AssetBucket, len(res.Players)))
	}

	// Verify every key exists at the expected size once it is up. A modpack that
	// uploads 187 of 188 jars is a server that fails to boot in a way nobody can
	// read from a log.
	for i, obj := range objects {
		if opts.Progress != nil {
			opts.Progress(i+1, len(objects))
		}
		sum, err := sum(obj)
		if err != nil {
			return res, err
		}
		existing, err := st.Stat(ctx, opts.AssetBucket, obj.Key)
		switch {
		// Same length is not same content: flipping `difficulty=hard` to
		// `difficulty=easy` changes nothing a size check can see. An object with
		// no recorded checksum (written by an older publish) is uploaded once so
		// it gets one.
		case err == nil && existing.Size == obj.Size && existing.SHA256 == sum:
			res.Unchanged = append(res.Unchanged, obj.Key)
			continue
		case err != nil && !errors.Is(err, ErrNotFound):
			return res, err
		}

		if opts.DryRun {
			res.Uploaded = append(res.Uploaded, obj.Key)
			res.TotalSize += obj.Size
			continue
		}

		if err := put(ctx, st, opts.AssetBucket, obj, sum); err != nil {
			return res, err
		}
		got, err := st.Stat(ctx, opts.AssetBucket, obj.Key)
		if err != nil {
			return res, fmt.Errorf("verify %s: %w", obj.Key, err)
		}
		if got.Size != obj.Size {
			return res, fmt.Errorf("verify %s: uploaded %d bytes, R2 says %d", obj.Key, obj.Size, got.Size)
		}
		res.Uploaded = append(res.Uploaded, obj.Key)
		res.TotalSize += obj.Size
	}

	// mods/ always belongs to the publish: the directory (or the pack) *is* the
	// list of jars the server loads, so anything left from an earlier publish is a
	// second copy of a mod, which is a crash rather than a stale file. A hand-rolled
	// publish over a former pack profile is the case that needs it — without this a
	// "vanilla" profile keeps forty-nine jars. config/ and root/ are only pruned on
	// request, because a hand-rolled directory may be the first publish into a
	// profile whose configs somebody wrote by hand.
	if packed := pf.Pack != nil; opts.Prune || !packed {
		removed, err := prune(ctx, st, opts, objects, opts.Prune || packed)
		if err != nil {
			return res, err
		}
		res.Removed = removed
	}

	if opts.DryRun {
		log.Info("dry run: nothing written", "upload", len(res.Uploaded), "prune", len(res.Removed))
		return res, nil
	}

	// Everything is up and verified: only now is it true that this bucket holds
	// this pack, which is what the marker claims. Discarding the old world happens
	// here too, never before the upload.
	switch {
	case pf.Pack != nil:
		if err := recordPack(ctx, st, opts, *pf.Pack, discardWorld); err != nil {
			return res, err
		}
	default:
		// A hand-rolled pack has no identity to record, so both halves of an earlier
		// fetched publish have to go: the marker in the world bucket, or the next
		// packed publish trusts a label that no longer describes the assets, and the
		// manifest in the asset bucket, or the page keeps naming a pack that is not
		// installed any more.
		if opts.AssetBucket != "" {
			if err := st.Remove(ctx, opts.AssetBucket, profilePrefix(opts.ProfileOf())+PackFile); err != nil {
				return res, fmt.Errorf("clear the pack manifest: %w", err)
			}
		}
		if opts.WorldBucket != "" {
			_, marker := worldKeys(opts.ProfileOf())
			if err := st.Remove(ctx, opts.WorldBucket, marker); err != nil {
				return res, fmt.Errorf("clear the pack marker: %w", err)
			}
		}
	}
	return res, nil
}

// Build is Run without the network: it decides exactly what a publish would
// upload. Exported because it is the part worth testing, and because the CLI
// prints the plan before asking R2 anything.
func Build(ctx context.Context, opts Options) ([]Object, []string, error) {
	var warnings []string

	if err := ValidateProfile(opts.ProfileOf()); err != nil {
		return nil, nil, err
	}

	root, err := os.Stat(opts.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read pack directory %s: %w", opts.Dir, err)
	}
	if !root.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", opts.Dir)
	}

	var objects []Object

	add := func(key string, o Object) {
		// Replace, never append: two objects with one key would both be uploaded and
		// whichever landed last would win, which sort.Slice below makes undefined.
		o.Key = key
		for i := range objects {
			if objects[i].Key == key {
				objects[i] = o
				return
			}
		}
		objects = append(objects, o)
	}

	pk, packed, err := LoadPack(opts.Dir)
	if err != nil {
		return nil, nil, err
	}
	if packed {
		if err := CheckPack(pk); err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, fmt.Sprintf("%s: minecraft %s, loader %s %s, %d server-side file(s) from the pack",
			pk.Describe(), pk.MC, pk.Loader, pk.LoaderVersion, pk.FileCount))

		// The pack decides the launcher: a loader version that disagrees with the
		// pack is a server that fails on a mixin, which reads like a broken mod.
		if pk.MC != "" {
			opts.FabricMC = pk.MC
			opts.FabricLoader = pk.LoaderVersion
		}
	}

	// server/, config/, scripts/ and root/ are copied as-is; any of them may be
	// absent, because the defaults below can fill a bare pack. world/ is excluded:
	// the world comes from the mc-worlds bucket, never from the asset pack.
	//
	// root/ is where a fetched modpack's non-mod files go (config/, kubejs/,
	// defaultconfigs/) and cloud-init copies it to /opt/minecraft before the other
	// prefixes, so our server.properties still wins over a pack's.
	dirs := map[string]string{
		"server":  "server/",
		"config":  "config/",
		"scripts": "scripts/",
		"root":    "root/",
	}
	for dirName, prefix := range dirs {
		base := filepath.Join(opts.Dir, dirName)
		if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "world" {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".swp") || strings.HasSuffix(d.Name(), "~") {
				return nil
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return err
			}
			obj, err := fileObject(path.Join(prefix, filepath.ToSlash(rel)), p)
			if err != nil {
				return err
			}
			add(obj.Key, obj)
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("walk %s: %w", dirName, err)
		}
	}

	// mods/ is the one directory with a filter: client-only mods crash or
	// confuse a dedicated server, and a modpack export ships them anyway.
	//
	// A fetched pack needs no list here — its .mrpack said `env.server:
	// unsupported` for those files and `outpost pack fetch` left them out — so the
	// hand-maintained list only applies to hand-rolled packs. Applying it to a
	// pack would silently drop a mod the pack author marked server-required
	// because an old filename happened to match.
	skip := map[string]bool{}
	if !packed {
		skip = clientOnly(opts.Dir)
	}
	modsDir := filepath.Join(opts.Dir, "mods")
	entries, err := os.ReadDir(modsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("read mods: %w", err)
	}
	skipped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			continue
		}
		if skip[e.Name()] {
			skipped++
			continue
		}
		obj, err := fileObject("mods/"+e.Name(), filepath.Join(modsDir, e.Name()))
		if err != nil {
			return nil, nil, err
		}
		add(obj.Key, obj)
	}
	if skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d client-only mod(s) listed in client-only-mods.txt", skipped))
	}

	// The launcher: local if the pack shipped one, otherwise fetched.
	if opts.FabricMC != "" && !hasKey(objects, "server/fabric-server-launch.jar") {
		loader, jar, err := FabricServer(ctx, opts.FabricMC, opts.FabricLoader)
		if err != nil {
			return nil, nil, err
		}
		add("server/fabric-server-launch.jar", Object{
			Key:     "server/fabric-server-launch.jar",
			Content: jar,
			Size:    int64(len(jar)),
		})
		warnings = append(warnings, fmt.Sprintf("fetched Fabric %s with loader %s from Fabric Meta", opts.FabricMC, loader))
	}
	if !hasKey(objects, "server/fabric-server-launch.jar") {
		return nil, nil, errors.New("no fabric-server-launch.jar in server/: pass --fabric 1.20.1 to fetch one, or put the jar in the pack")
	}

	// eula.txt and server.properties are generated when the pack has neither, so
	// a first publish only has to supply a launcher and a mod list.
	if !hasKey(objects, "server/eula.txt") {
		add("server/eula.txt", textObject("server/eula.txt", "eula=true\n"))
		warnings = append(warnings, "generated server/eula.txt (eula=true)")
	}
	if !hasKey(objects, "server/server.properties") {
		add("server/server.properties", textObject("server/server.properties", defaultProperties(opts.Open)))
		warnings = append(warnings, "generated server/server.properties")
	}

	// The player list, either handed over whole or resolved from names.
	if opts.Players != nil {
		add("server/whitelist.json", Object{
			Key:     "server/whitelist.json",
			Content: opts.Players.Whitelist,
			Size:    int64(len(opts.Players.Whitelist)),
		})
		add("server/ops.json", Object{
			Key:     "server/ops.json",
			Content: opts.Players.Ops,
			Size:    int64(len(opts.Players.Ops)),
		})
	} else if len(opts.Whitelist) > 0 || len(opts.Ops) > 0 {
		whitelist, ops, err := PlayerFiles(ctx, opts.Whitelist, opts.Ops)
		if err != nil {
			return nil, nil, err
		}
		add("server/whitelist.json", textObject("server/whitelist.json", string(whitelist)))
		add("server/ops.json", textObject("server/ops.json", string(ops)))
	}

	// start.sh is what minecraft.service runs. Generating one means a bare
	// `outpost publish --fabric 1.20.1` produces a bootable server rather than a
	// VPS that fails systemd twenty minutes later.
	// pack.json is not just local bookkeeping: it is how the page names a profile
	// ("Fabulously Optimized 5.2.8", "minecraft 1.20.1") and how the RAM guard
	// knows what it is looking at. It is small, and it belongs beside the files it
	// describes.
	if packed {
		raw, err := os.ReadFile(filepath.Join(opts.Dir, PackFile))
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", PackFile, err)
		}
		add(PackFile, Object{Key: PackFile, Content: raw, Size: int64(len(raw))})
	}

	if !hasKey(objects, "scripts/start.sh") {
		add("scripts/start.sh", textObject("scripts/start.sh", defaultStartScript))
		warnings = append(warnings, "generated scripts/start.sh (2 GB heap; raise it in the pack if the modpack needs more)")
	}

	if !opts.Open && !hasKey(objects, "server/whitelist.json") {
		return nil, nil, errors.New("no whitelist: pass --whitelist name1,name2 (resolved through Mojang), put server/whitelist.json in the pack, or pass --open for a server anyone can join")
	}

	// Every key is scoped by profile last, after all the key checks above, so the
	// rules ("a launcher is required", "our server.properties wins") are written
	// once against the layout inside a profile and the prefix is not repeated at
	// every call site.
	//
	// Two keys stay global: the player list. The same people should be able to
	// join every pack, and internal/roster is its only writer, so scoping it per
	// profile would mean adding somebody once per world.
	prefix := profilePrefix(opts.ProfileOf())
	for i := range objects {
		if !playerKeys[objects[i].Key] {
			objects[i].Key = prefix + objects[i].Key
		}
	}

	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, warnings, nil
}

// whitelistNamesIn reads the names out of one whitelist.json, and answers nil for
// anything it cannot parse: the answer is only ever used to decide whether there
// is anybody at all.
func whitelistNamesIn(raw []byte) []string {
	var players []Player
	if err := json.Unmarshal(raw, &players); err != nil {
		return nil
	}
	names := make([]string, 0, len(players))
	for _, p := range players {
		names = append(names, p.Name)
	}
	return names
}

// packShipsPlayers reports whether the pack directory carries its own player
// list. Either file counts: Build publishes both from one source, so a pack that
// brought one of them must not have the other half swapped for the bucket's —
// and a pack with an ops.json but no whitelist.json lets nobody join anyway,
// which Build's refusal is there to say.
func packShipsPlayers(dir string) bool {
	for _, name := range []string{"whitelist.json", "ops.json"} {
		if _, err := os.Stat(filepath.Join(dir, "server", name)); err == nil {
			return true
		}
	}
	return false
}

// prune removes stale mods, config and root files. It never touches server/ (that
// is where a person's own files live) or the world bucket. root/ is pruned
// because a fetched pack owns every file in it: a config the new pack version
// dropped would otherwise stay in R2 and load with the next boot.
func prune(ctx context.Context, st Store, opts Options, objects []Object, everything bool) ([]string, error) {
	want := map[string]bool{}
	for _, o := range objects {
		want[o.Key] = true
	}

	base := profilePrefix(opts.ProfileOf())
	prefixes := []string{base + "mods/"}
	if everything {
		prefixes = append(prefixes, base+"config/", base+"root/")
	}

	var removed []string
	for _, prefix := range prefixes {
		existing, err := st.List(ctx, opts.AssetBucket, prefix)
		if err != nil {
			return nil, err
		}
		for _, obj := range existing {
			if want[obj.Key] {
				continue
			}
			if opts.DryRun {
				removed = append(removed, obj.Key)
				continue
			}
			if err := st.Remove(ctx, opts.AssetBucket, obj.Key); err != nil {
				return removed, err
			}
			removed = append(removed, obj.Key)
		}
	}
	sort.Strings(removed)
	return removed, nil
}

// whitelistNames reads the names out of the whitelist we are about to upload,
// whether it came from the pack or was generated from --whitelist.
func whitelistNames(objects []Object) []string {
	for _, obj := range objects {
		// The whitelist is global, so this matches both "server/whitelist.json"
		// and a profile-scoped one from a pack that shipped its own.
		if !strings.HasSuffix(obj.Key, "server/whitelist.json") {
			continue
		}
		raw := obj.Content
		if raw == nil && obj.Path != "" {
			b, err := os.ReadFile(obj.Path)
			if err != nil {
				return nil
			}
			raw = b
		}
		return whitelistNamesIn(raw)
	}
	return nil
}

func put(ctx context.Context, st Store, bucket string, obj Object, sum string) error {
	if obj.Content != nil {
		return st.Put(ctx, bucket, obj.Key, bytes.NewReader(obj.Content), obj.Size, sum)
	}
	f, err := os.Open(obj.Path)
	if err != nil {
		return fmt.Errorf("open %s: %w", obj.Path, err)
	}
	defer f.Close()
	return st.Put(ctx, bucket, obj.Key, f, obj.Size, sum)
}

// sum is the hex SHA-256 of an object's bytes, whether it lives on disk or was
// generated in memory.
func sum(obj Object) (string, error) {
	h := sha256.New()
	if obj.Content != nil {
		h.Write(obj.Content)
	} else {
		f, err := os.Open(obj.Path)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", obj.Path, err)
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", fmt.Errorf("hash %s: %w", obj.Path, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileObject(key, p string) (Object, error) {
	info, err := os.Stat(p)
	if err != nil {
		return Object{}, fmt.Errorf("stat %s: %w", p, err)
	}
	return Object{Key: key, Path: p, Size: info.Size()}, nil
}

func textObject(key, body string) Object {
	b := []byte(body)
	return Object{Key: key, Content: b, Size: int64(len(b))}
}

func hasKey(objects []Object, key string) bool {
	for _, o := range objects {
		if o.Key == key {
			return true
		}
	}
	return false
}

// clientOnly reads the pack's own list if it has one, otherwise the one carried
// over from the December 2025 pack. A pack-specific list wins because mod names
// change with versions and the same rule ("rendering mods stay on the client")
// produces a different set of filenames every time.
func clientOnly(dir string) map[string]bool {
	list := defaultClientOnlyList
	if raw, err := os.ReadFile(filepath.Join(dir, "client-only-mods.txt")); err == nil {
		list = string(raw)
	}

	skip := map[string]bool{}
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		skip[line] = true
	}
	return skip
}

// defaultStartScript suits a 4 GB VPS running the vanilla-ish server this
// project starts with. A pack that ships its own start.sh wins, which is how a
// heavier modpack tunes its heap.
const defaultStartScript = `#!/bin/bash
# Written by outpost publish. The box is 4 GB, so the heap stays well under it;
# raising -Xmx past ~2.5G on that class of server swaps and stutters.
cd /opt/minecraft
exec java -Xms1G -Xmx2G \
  -XX:+UseG1GC -XX:+ParallelRefProcEnabled -XX:MaxGCPauseMillis=200 \
  -XX:+UnlockExperimentalVMOptions -XX:+DisableExplicitGC -XX:+AlwaysPreTouch \
  -jar fabric-server-launch.jar nogui
`

// defaultProperties is deliberately small: the cloud-init template rewrites the
// port and RCON lines, and everything else should be Minecraft's own default so
// a strange setting has to be a deliberate choice.
func defaultProperties(open bool) string {
	whitelist := "true"
	if open {
		whitelist = "false"
	}
	return `# Written by ` + "`outpost publish`" + ` on first upload.
# Adjust here or in the pack, then publish again.
motd=Outpost
server-port=25565
enable-rcon=false
rcon.port=25575
rcon.password=
max-players=20
white-list=` + whitelist + `
enforce-whitelist=` + whitelist + `
online-mode=true
view-distance=10
simulation-distance=10
difficulty=normal
spawn-protection=0
enable-command-block=false
`
}
