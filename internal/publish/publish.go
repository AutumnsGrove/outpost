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
//
// A missing world is not this package's problem: mc-worlds/current/world.tar.gz
// is written by the VPS on shutdown, and its absence just means Minecraft
// generates a fresh one.
package publish

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"outpost/internal/r2"
)

// ErrNotFound is re-exported so a Store implementation can answer "no such key"
// without importing r2.
var ErrNotFound = r2.ErrNotFound

//go:embed client-only-mods.txt
var defaultClientOnlyList string

// Store is the slice of R2 publish needs. The real implementation is *r2.Client;
// tests use a map.
type Store interface {
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error
	Stat(ctx context.Context, bucket, key string) (int64, error)
	List(ctx context.Context, bucket, prefix string) ([]r2.Object, error)
	Remove(ctx context.Context, bucket, key string) error
}

// Options is one publish run.
type Options struct {
	// Dir is the pack directory: server/, mods/, config/, scripts/.
	Dir         string
	AssetBucket string

	// FabricMC, when set (e.g. "1.20.1"), fetches a Fabric server launcher for
	// that Minecraft version and uploads it as server/fabric-server-launch.jar.
	// FabricLoader pins the loader; empty means the newest stable one.
	FabricMC     string
	FabricLoader string

	// Whitelist and Ops are Minecraft names. They are resolved to UUIDs through
	// Mojang at publish time, so the repo never holds a stale uuid.
	Whitelist []string
	Ops       []string

	// Open publishes a server with white-list=false. Needs saying out loud,
	// because the default is the opposite.
	Open bool

	// DryRun reports what would change without touching R2.
	DryRun bool

	// Prune deletes mods/ and config/ keys that are not in the pack. Without it
	// renaming a jar leaves the old one for the server to load.
	Prune bool

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
}

// Run uploads the pack, verifies it, and prunes what the pack no longer has.
func Run(ctx context.Context, st Store, opts Options) (Result, error) {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	objects, warnings, err := Build(ctx, opts)
	if err != nil {
		return Result{}, err
	}

	var res Result
	res.Warnings = append(res.Warnings, warnings...)

	// Verify every key exists at the expected size once it is up. A modpack that
	// uploads 187 of 188 jars is a server that fails to boot in a way nobody can
	// read from a log.
	for _, obj := range objects {
		existing, err := st.Stat(ctx, opts.AssetBucket, obj.Key)
		switch {
		case err == nil && existing == obj.Size:
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

		if err := put(ctx, st, opts.AssetBucket, obj); err != nil {
			return res, err
		}
		got, err := st.Stat(ctx, opts.AssetBucket, obj.Key)
		if err != nil {
			return res, fmt.Errorf("verify %s: %w", obj.Key, err)
		}
		if got != obj.Size {
			return res, fmt.Errorf("verify %s: uploaded %d bytes, R2 says %d", obj.Key, obj.Size, got)
		}
		res.Uploaded = append(res.Uploaded, obj.Key)
		res.TotalSize += obj.Size
	}

	if opts.Prune {
		removed, err := prune(ctx, st, opts, objects)
		if err != nil {
			return res, err
		}
		res.Removed = removed
	}

	if opts.DryRun {
		log.Info("dry run: nothing written", "upload", len(res.Uploaded), "prune", len(res.Removed))
	}
	return res, nil
}

// Build is Run without the network: it decides exactly what a publish would
// upload. Exported because it is the part worth testing, and because the CLI
// prints the plan before asking R2 anything.
func Build(ctx context.Context, opts Options) ([]Object, []string, error) {
	var warnings []string

	root, err := os.Stat(opts.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read pack directory %s: %w", opts.Dir, err)
	}
	if !root.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", opts.Dir)
	}

	serverDir := filepath.Join(opts.Dir, "server")
	if _, err := os.Stat(serverDir); err != nil {
		return nil, nil, fmt.Errorf("%s has no server/ directory; expected server/, mods/, config/, scripts/", opts.Dir)
	}

	var objects []Object

	add := func(key string, o Object) { objects = append(objects, o) }

	// server/ and config/ and scripts/ are copied as-is. world/ is excluded:
	// the world comes from the mc-worlds bucket, never from the asset pack.
	dirs := map[string]string{
		"server":  "server/",
		"config":  "config/",
		"scripts": "scripts/",
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
	skip := clientOnly(opts.Dir)
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

	// Whitelist and ops. Names are resolved now, never stored as uuids.
	if len(opts.Whitelist) > 0 || len(opts.Ops) > 0 {
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
	if !hasKey(objects, "scripts/start.sh") {
		add("scripts/start.sh", textObject("scripts/start.sh", defaultStartScript))
		warnings = append(warnings, "generated scripts/start.sh (2 GB heap; raise it in the pack if the modpack needs more)")
	}

	if !opts.Open && !hasKey(objects, "server/whitelist.json") {
		return nil, nil, errors.New("no whitelist: pass --whitelist name1,name2 (resolved through Mojang), put server/whitelist.json in the pack, or pass --open for a server anyone can join")
	}

	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, warnings, nil
}

// prune removes stale mods and config files. It never touches server/ (that is
// where a person's own files live) or the world bucket.
func prune(ctx context.Context, st Store, opts Options, objects []Object) ([]string, error) {
	want := map[string]bool{}
	for _, o := range objects {
		want[o.Key] = true
	}

	var removed []string
	for _, prefix := range []string{"mods/", "config/"} {
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

func put(ctx context.Context, st Store, bucket string, obj Object) error {
	if obj.Content != nil {
		return st.Put(ctx, bucket, obj.Key, strings.NewReader(string(obj.Content)), obj.Size)
	}
	f, err := os.Open(obj.Path)
	if err != nil {
		return fmt.Errorf("open %s: %w", obj.Path, err)
	}
	defer f.Close()
	return st.Put(ctx, bucket, obj.Key, f, obj.Size)
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
