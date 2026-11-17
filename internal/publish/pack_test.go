package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"outpost/internal/config"
	"outpost/internal/trash"
)

// testPack is the pack.json a fetch of a small 1.20.1 Fabric pack would leave.
func testPack() Pack {
	return Pack{
		Source:        "modrinth",
		Slug:          "test-pack",
		Name:          "Test Pack",
		VersionID:     "ver1",
		VersionNumber: "1.0.0",
		MC:            "1.20.1",
		Loader:        "fabric",
		LoaderVersion: "0.15.7",
		MemoryGB:      4,
	}
}

// writeModpack is writePack plus a pack.json and the root/ tree a fetched pack
// puts its non-mod files in.
func writeModpack(t *testing.T, p Pack) string {
	t.Helper()
	dir := writePack(t)

	if err := os.MkdirAll(filepath.Join(dir, "root", "kubejs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root", "kubejs", "start.js"), []byte("console.info('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePack(dir, p); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildTrustsThePackOverTheClientOnlyList(t *testing.T) {
	dir := writeModpack(t, testPack())

	objects, warnings, err := Build(context.Background(), Options{Dir: dir, AssetBucket: "mc-assets"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	have := map[string]bool{}
	for _, o := range objects {
		have[o.Key] = true
	}

	// sodium is in client-only-mods.txt, but the pack said env.server: required.
	// The pack wins: dropping a file the pack author marked server-side is a
	// server that boots with a config referring to a mod that is not there.
	if !have[key("mods/sodium-0.5.13.jar")] {
		t.Errorf("the hand-maintained client-only list was applied to a pack; keys: %v", keys(objects))
	}
	if !have[key("root/kubejs/start.js")] {
		t.Errorf("root/ was not uploaded; keys: %v", keys(objects))
	}
	for _, o := range objects {
		if strings.HasPrefix(o.Key, "pack.json") || strings.Contains(o.Key, "client-only-mods") {
			t.Errorf("%s is local bookkeeping and should not be uploaded", o.Key)
		}
	}
	if !warningsContain(warnings, "Test Pack") {
		t.Errorf("the summary should name the pack: %v", warnings)
	}
}

func TestBuildRefusesAPackForAVersionOutsideTheRanges(t *testing.T) {
	for _, mc := range []string{"1.16.5", "1.22.4"} {
		p := testPack()
		p.MC = mc
		dir := writeModpack(t, p)

		_, _, err := Build(context.Background(), Options{Dir: dir})
		if err == nil || !strings.Contains(err.Error(), MinecraftList()) {
			t.Fatalf("%s should be refused naming the ranges, got %v", mc, err)
		}
	}
}

// A version inside a range is bootable whether or not it is one of the versions
// the panel offers: the picker is a list of buttons, not the limit of the system.
func TestBuildAcceptsAVersionBetweenTheOffers(t *testing.T) {
	for _, mc := range []string{"1.19.4", "1.21.4", "1.20.6"} {
		p := testPack()
		p.MC = mc
		dir := writeModpack(t, p)

		if _, _, err := Build(context.Background(), Options{Dir: dir}); err != nil {
			t.Fatalf("%s has a JDK and should build: %v", mc, err)
		}
	}
}

func TestBuildAcceptsEveryVersionInTheTable(t *testing.T) {
	// 1.21.1 is bootable because cloud-init installs Java 21 for it, which is the
	// whole point of the table: the refusal follows the row, not a constant.
	p := testPack()
	p.MC = "1.21.1"
	dir := writeModpack(t, p)

	objects, _, err := Build(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("1.21.1 fabric was refused: %v", err)
	}
	for _, o := range objects {
		if strings.HasSuffix(o.Key, "/pack.json") {
			return
		}
	}
	t.Fatal("a 1.21.1 pack was built without its manifest")
}

func TestRunWarnsWhenNoRegionHoldsThePackButAPlanDoes(t *testing.T) {
	// The region's default and its fallbacks are both too small, but the picker can
	// rent cx33, so this is a "pick a bigger plan" note rather than a refusal.
	p := testPack()
	p.MemoryGB = 8
	dir := writeModpack(t, p)
	st := newFakeStore()

	res, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		Regions: map[string]config.Region{
			"eu": {ServerType: "cx23", FallbackTypes: []string{"cax11"}},
		},
	})
	if err != nil {
		t.Fatalf("a pack a rentable plan can hold was refused: %v", err)
	}
	if !warningsContain(res.Warnings, "cx33") {
		t.Fatalf("the warning should name the plan that fits: %v", res.Warnings)
	}
}

func TestRunRefusesAPackNoPlanCanHold(t *testing.T) {
	p := testPack()
	p.MemoryGB = 64 // bigger than every plan in the table
	dir := writeModpack(t, p)
	st := newFakeStore()

	_, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		Regions: map[string]config.Region{
			"eu": {ServerType: "cx23", FallbackTypes: []string{"cax11"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "64 GB") {
		t.Fatalf("want a RAM refusal, got %v", err)
	}
	if len(st.objects) != 0 {
		t.Fatal("the refusal happened after uploading")
	}

}

func TestRunRefusesAWorldFromAnotherPack(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("world")
	marker, _ := json.Marshal(Pack{Source: "modrinth", Slug: "homestead", VersionID: "other", VersionNumber: "0.9.0"})
	st.worlds[key("pack.json")] = marker

	opts := Options{Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds"}
	_, err := Run(context.Background(), st, opts)
	if err == nil || !strings.Contains(err.Error(), "--new-world") {
		t.Fatalf("want a refusal naming --new-world, got %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; !ok {
		t.Fatal("the world was removed by a refused publish")
	}

	// --keep-world says so out loud and lets it through.
	opts.KeepWorld = true
	res, err := Run(context.Background(), st, opts)
	if err != nil {
		t.Fatalf("keep-world run: %v", err)
	}
	if !warningsContain(res.Warnings, "--keep-world") {
		t.Fatalf("the warning should mention what was overridden: %v", res.Warnings)
	}
}

func TestRunKeepsTheWorldAcrossAPackUpdate(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("world")
	// The same pack, one version older: that is an update, not a different game.
	older, _ := json.Marshal(Pack{Source: "modrinth", Slug: "test-pack", VersionID: "older", VersionNumber: "0.9.0"})
	st.worlds[key("pack.json")] = older

	res, err := Run(context.Background(), st, Options{Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds"})
	if err != nil {
		t.Fatalf("a pack update was refused: %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; !ok {
		t.Fatal("a pack update discarded the world it should have kept")
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "world") {
			t.Fatalf("a pack update complained about the world: %v", res.Warnings)
		}
	}
}

// A newer version of the same pack for a different Minecraft version is not an
// update: the world cannot follow it. A pack version bump keeps the world (that
// is TestRunKeepsTheWorldAcrossAPackUpdate); a game version bump must not.
func TestRunRefusesAMinecraftVersionChangeIntoAProfile(t *testing.T) {
	p := testPack()
	p.MC = "1.21.1"
	dir := writeModpack(t, p)
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("1.20.1 world")
	// The same pack, same source and slug, installed for the Minecraft version
	// before this one: the case a version picker produces.
	older, _ := json.Marshal(Pack{Source: "modrinth", Slug: "test-pack", VersionID: "older", VersionNumber: "0.9.0", MC: "1.20.1"})
	st.worlds[key("pack.json")] = older

	opts := Options{Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds"}
	_, err := Run(context.Background(), st, opts)
	if err == nil {
		t.Fatal("1.21.1 was published over a 1.20.1 world")
	}
	// The refusal has to say what to do: a new profile keeps both worlds.
	if !strings.Contains(err.Error(), "Minecraft version change") || !strings.Contains(err.Error(), "--profile default-1-21-1") {
		t.Fatalf("the refusal is not actionable: %v", err)
	}
	if !strings.Contains(err.Error(), "--new-world") {
		t.Fatalf("the refusal does not offer --new-world: %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; !ok {
		t.Fatal("the world was removed by a refused publish")
	}

	// --keep-world is the door for somebody who knows what they are doing.
	opts.KeepWorld = true
	res, err := Run(context.Background(), st, opts)
	if err != nil {
		t.Fatalf("keep-world run: %v", err)
	}
	if !warningsContain(res.Warnings, "holds a Minecraft 1.20.1 world") || !warningsContain(res.Warnings, "--keep-world") {
		t.Fatalf("the warning should say what was overridden: %v", res.Warnings)
	}

	// --new-world discards the world, which is the other honest answer.
	opts.KeepWorld = false
	opts.NewWorld = true
	if _, err := Run(context.Background(), st, opts); err != nil {
		t.Fatalf("new-world run: %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; ok {
		t.Fatal("--new-world kept a world from another Minecraft version")
	}
}

func TestRunNewWorldDiscardsTheWorldAfterTheUpload(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("vanilla world from 2025")
	st.worlds[key("pack.json")] = []byte(`{"source":"modrinth","slug":"test-pack","versionId":"older"}`)

	_, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
		NewWorld:    true,
	})
	if err != nil {
		t.Fatalf("new-world run: %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; ok {
		t.Fatal("--new-world left the old world behind")
	}

	var marker Pack
	if err := json.Unmarshal(st.worlds[key("pack.json")], &marker); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if marker.VersionID != "ver1" {
		t.Fatalf("marker still says %q", marker.VersionID)
	}
}

func TestRunRefusesAnUnmarkedWorldAndAcceptsItWithKeepWorld(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("world from before packs existed")

	opts := Options{Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds"}
	if _, err := Run(context.Background(), st, opts); err == nil || !strings.Contains(err.Error(), "--new-world") {
		t.Fatalf("want a refusal for an unmarked world, got %v", err)
	}

	opts.KeepWorld = true
	if _, err := Run(context.Background(), st, opts); err != nil {
		t.Fatalf("keep-world: %v", err)
	}
}

func TestRunLeavesTheMarkerAloneForTheSameBuild(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("world")
	same, _ := json.Marshal(testPack())
	st.worlds[key("pack.json")] = same

	res, err := Run(context.Background(), st, Options{Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds"})
	if err != nil {
		t.Fatalf("same build should publish without ceremony: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "world") {
			t.Fatalf("a same-build publish complained about the world: %v", res.Warnings)
		}
	}
	if _, ok := st.worlds[key("world.tar.gz")]; !ok {
		t.Fatal("the world of the same pack was touched")
	}
}

func TestRunDoesNotTouchTheWorldOnADryRun(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("old world")

	res, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
		NewWorld:    true,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !warningsContain(res.Warnings, "would discard") {
		t.Fatalf("dry run should say what it would do: %v", res.Warnings)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; !ok {
		t.Fatal("a dry run removed the world")
	}
	if _, ok := st.worlds[key("pack.json")]; ok {
		t.Fatal("a dry run wrote the marker")
	}
}

func TestLoadPackTreatsAMissingManifestAsAHandRolledPack(t *testing.T) {
	if _, ok, err := LoadPack(t.TempDir()); err != nil || ok {
		t.Fatalf("missing pack.json: ok=%v err=%v", ok, err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PackFile), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPack(dir); err == nil {
		t.Fatal("a corrupt pack.json was accepted")
	}
}

func TestRunNewWorldStillWorksWhenTheMarkerIsUnreadable(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("world")
	st.worlds[key("pack.json")] = []byte("{ this is not json")

	// A corrupt marker must not be a dead end: --new-world is the way out, and it
	// has to work without a shell and a manual delete.
	_, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
		NewWorld:    true,
	})
	if err != nil {
		t.Fatalf("new-world with a corrupt marker: %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; ok {
		t.Fatal("--new-world left the world behind")
	}
	if err := json.Unmarshal(st.worlds[key("pack.json")], &Pack{}); err != nil {
		t.Fatalf("the marker was not rewritten: %v", err)
	}
}

func TestRunNewWorldDiscardsEvenAWorldOfTheSameBuild(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("a world I no longer want")
	same, _ := json.Marshal(testPack())
	st.worlds[key("pack.json")] = same

	// --new-world is the only way out of a world that is simply unwanted, and it
	// has to work when the pack has not changed: a corrupted chunk in a world of
	// the current pack is exactly that case.
	if _, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
		NewWorld:    true,
	}); err != nil {
		t.Fatalf("new-world on the same build: %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; ok {
		t.Fatal("--new-world was a no-op because the pack had not changed")
	}
}

// A hand-rolled directory dropped into a profile that holds a fetched pack's world
// is the same question as any other pack change, and it used to be answered
// silently: the marker and the manifest were deleted, so the next publish had no
// idea what made the world and booted the new jars over it. It is now refused like
// a pack swap, and --keep-world is the door.
func TestRunRefusesAHandRolledPackOverAnotherPacksWorld(t *testing.T) {
	st := newFakeStore()
	if _, err := Run(context.Background(), st, Options{
		Dir:         writeModpack(t, testPack()),
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
	}); err != nil {
		t.Fatalf("packed publish: %v", err)
	}
	st.worlds[key("world.tar.gz")] = []byte("world")

	_, err := Run(context.Background(), st, Options{
		Dir:         writePack(t),
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
	})
	if err == nil || !strings.Contains(err.Error(), "--keep-world") {
		t.Fatalf("want the world guard to ask, got %v", err)
	}

	// --keep-world loads it anyway, and the marker now says what is installed: a
	// hand-rolled pack with the Minecraft version it will boot.
	res, err := Run(context.Background(), st, Options{
		Dir:         writePack(t),
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
		KeepWorld:   true,
	})
	if err != nil {
		t.Fatalf("hand-rolled publish with --keep-world: %v", err)
	}
	if !warningsContain(res.Warnings, "--keep-world") {
		t.Fatalf("loading another pack's world said nothing: %v", res.Warnings)
	}
	if string(st.worlds[key("world.tar.gz")]) != "world" {
		t.Fatal("--keep-world removed the world it was told to keep")
	}
	var marker Pack
	if err := json.Unmarshal(st.worlds[key("pack.json")], &marker); err != nil {
		t.Fatalf("the marker is not a manifest: %v (%q)", err, st.worlds[key("pack.json")])
	}
	if marker.MC != "1.20.1" || marker.Source != "" {
		t.Fatalf("the marker does not describe what is installed: %+v", marker)
	}
	// The asset manifest is the generated one, so the page can name the Minecraft
	// version instead of "hand-rolled pack, no version".
	var installed Pack
	if err := json.Unmarshal(st.objects[key("pack.json")], &installed); err != nil {
		t.Fatalf("the asset manifest is not a manifest: %v", err)
	}
	if installed.MC != "1.20.1" || installed.Name != "Hand-rolled" {
		t.Fatalf("the uploaded manifest does not describe the profile: %+v", installed)
	}
}

func TestRunDoesNotRefuseOverAPlanTheTableDoesNotKnow(t *testing.T) {
	p := testPack()
	p.MemoryGB = 8
	dir := writeModpack(t, p)
	st := newFakeStore()

	// cx24 does not exist in the table. An unknown plan means "unproven", not
	// "too small": the table is a snapshot and Hetzner renames plans, so the
	// region has to be rented rather than refused.
	res, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		Regions: map[string]config.Region{
			"eu": {ServerType: "cx23"},
			"us": {ServerType: "cx24"},
		},
	})
	if err != nil {
		t.Fatalf("an unknown plan was treated as a refusal: %v", err)
	}
	if !warningsContain(res.Warnings, "cx24") {
		t.Fatalf("the unchecked region should be named: %v", res.Warnings)
	}
}

func TestBuildScopesEveryKeyByProfile(t *testing.T) {
	dir := writeModpack(t, testPack())
	if err := os.WriteFile(filepath.Join(dir, "server", "whitelist.json"),
		[]byte(`[{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	objects, _, err := Build(context.Background(), Options{Dir: dir, Profile: "homestead"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	prefix := profilePrefix("homestead")
	for _, o := range objects {
		if playerKeys[o.Key] {
			continue // the player list is deliberately server-wide
		}
		if !strings.HasPrefix(o.Key, prefix) {
			t.Fatalf("%s is not under %s: two profiles would overwrite each other", o.Key, prefix)
		}
	}
	// And the whitelist the summary prints still comes out, despite the prefix.
	if names := whitelistNames(objects); len(names) == 0 {
		t.Fatalf("the whitelist was lost behind the profile prefix: %v", keys(objects))
	}
}

func TestBuildRefusesAProfileNameThatIsNotOne(t *testing.T) {
	dir := writeModpack(t, testPack())
	for _, bad := range []string{"../etc", "Has Caps", "a/b", strings.Repeat("x", 40)} {
		if _, _, err := Build(context.Background(), Options{Dir: dir, Profile: bad}); err == nil {
			t.Errorf("profile %q was accepted", bad)
		}
	}
}

func TestPruneLeavesTheOtherProfileAlone(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	other := profilePrefix("cobblemon") + "mods/keep-me.jar"
	st.objects[other] = []byte("another game's mod")
	st.objects[profilePrefix("homestead")+"mods/stale.jar"] = []byte("old")

	if _, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		Profile:     "homestead",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, ok := st.objects[profilePrefix("homestead")+"mods/stale.jar"]; ok {
		t.Fatal("a stale mod survived in the profile being published")
	}
	if _, ok := st.objects[other]; !ok {
		t.Fatal("publishing one profile pruned another profile's mods")
	}
}

func TestReadPlayersAndPublishThemVerbatim(t *testing.T) {
	st := newFakeStore()
	st.objects["server/whitelist.json"] = []byte(`[{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch"}]`)

	// A missing ops.json is the normal state of a server nobody has opped on.
	players, err := ReadPlayers(context.Background(), st, "mc-assets")
	if err != nil {
		t.Fatalf("read players: %v", err)
	}
	if string(players.Ops) != "[]" {
		t.Fatalf("ops = %s", players.Ops)
	}

	// An install reuses those bytes instead of resolving names through Mojang,
	// which rate-limits and would make a page-driven install fail at random.
	dir := writeModpack(t, testPack())
	if err := os.Remove(filepath.Join(dir, "server", "whitelist.json")); err != nil {
		t.Fatal(err)
	}
	objects, _, err := Build(context.Background(), Options{Dir: dir, Players: players, Profile: "cobblemon"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	have := map[string]string{}
	for _, o := range objects {
		have[o.Key] = string(o.Content)
	}
	if got := have["server/whitelist.json"]; !strings.Contains(got, "Notch") {
		t.Fatalf("the whitelist was not published verbatim: %q", got)
	}
	if _, ok := have["server/whitelist.json"]; !ok {
		t.Fatal("the whitelist should stay global, not go under the profile")
	}

	if _, err := ReadPlayers(context.Background(), newFakeStore(), "mc-assets"); err == nil {
		t.Fatal("an empty bucket should say the list is missing")
	}
}

func TestBuildUploadsTheManifestSoThePageCanNameTheProfile(t *testing.T) {
	dir := writeModpack(t, testPack())

	objects, _, err := Build(context.Background(), Options{Dir: dir, Profile: "cobblemon"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var got Object
	for _, o := range objects {
		if strings.HasSuffix(o.Key, "/pack.json") {
			got = o
		}
	}
	if got.Key == "" {
		t.Fatalf("no pack.json in the plan; the page reads it to name a profile: %v", keys(objects))
	}
	if got.Key != profilePrefix("cobblemon")+PackFile {
		t.Fatalf("pack.json landed at %s, want it inside the profile", got.Key)
	}
	if !strings.Contains(string(got.Content), `"slug": "test-pack"`) {
		t.Fatalf("the uploaded manifest is not the pack's: %s", got.Content)
	}

}

// A hand-rolled directory has no pack.json, and used to ship none at all: the page
// then called the profile "hand-rolled pack" with no Minecraft version, and the
// orchestrator had nothing to pick a JDK from. The generated one fills exactly
// those gaps and no others — no source, no slug, so it is not "a different pack"
// to the world guard.
func TestBuildGeneratesAManifestForAHandRolledDirectory(t *testing.T) {
	plain := writePack(t)
	objects, _, err := Build(context.Background(), Options{Dir: plain, Profile: "default", FabricMC: "1.21.1"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var got Object
	for _, o := range objects {
		if strings.HasSuffix(o.Key, "/pack.json") {
			got = o
		}
	}
	if got.Key == "" {
		t.Fatal("a hand-rolled publish shipped no manifest")
	}
	var pk Pack
	if err := json.Unmarshal(got.Content, &pk); err != nil {
		t.Fatalf("parse the generated manifest: %v", err)
	}
	if pk.MC != "1.21.1" || pk.Loader != "fabric" {
		t.Fatalf("generated manifest does not say what will boot: %+v", pk)
	}
	if pk.Source != "" || pk.Slug != "" {
		t.Fatalf("a generated manifest claimed an identity: %+v", pk)
	}

	// Two builds of an unchanged directory must be byte-identical, or every republish
	// uploads a new manifest for nothing.
	again, _, err := Build(context.Background(), Options{Dir: plain, Profile: "default", FabricMC: "1.21.1"})
	if err != nil {
		t.Fatalf("build again: %v", err)
	}
	for _, o := range again {
		if o.Key == got.Key && !bytes.Equal(o.Content, got.Content) {
			t.Fatalf("the generated manifest is not deterministic:\n%s\n%s", got.Content, o.Content)
		}
	}
}

func TestAHandRolledPublishOwnsModsButNotConfigs(t *testing.T) {
	st := newFakeStore()
	// A former pack profile: forty-nine jars and a config written by hand.
	if _, err := Run(context.Background(), st, Options{
		Dir:         writeModpack(t, testPack()),
		AssetBucket: "mc-assets",
	}); err != nil {
		t.Fatalf("packed publish: %v", err)
	}
	st.objects[key("config/hand-written.toml")] = []byte("mine")

	// Now the same profile gets a hand-rolled directory with no mods.
	if _, err := Run(context.Background(), st, Options{
		Dir:         writePack(t),
		AssetBucket: "mc-assets",
	}); err != nil {
		t.Fatalf("hand-rolled publish: %v", err)
	}

	// mods/ is the publish's to own: a leftover jar is a second copy of a mod.
	for k := range st.objects {
		if strings.HasPrefix(k, key("mods/")) && strings.Contains(k, "sodium") {
			t.Fatalf("a former pack's mod survived: %s", k)
		}
	}
	// configs are not, because the first hand-rolled publish into a profile may be
	// the moment someone's own config lands there.
	if _, ok := st.objects[key("config/hand-written.toml")]; !ok {
		t.Fatal("a hand-rolled publish pruned a config it did not write")
	}
}

func TestVanillaIsAPackThisServerCanBoot(t *testing.T) {
	pk := VanillaPack("")
	// It goes through the same door every pack does: the same Minecraft version
	// check, the same loader check, the same world guard, the same prune.
	if err := CheckPack(pk); err != nil {
		t.Fatalf("vanilla is refused by the check every pack passes: %v", err)
	}
	if pk.MC != SupportedMC || pk.Loader != "fabric" {
		t.Fatalf("vanilla = %+v", pk)
	}
	// An identity, not a hand-rolled directory: without source and slug the world
	// guard cannot tell a vanilla world from one nothing knows the origin of, and
	// every publish into the profile would have to ask.
	if pk.Source == "" || pk.Slug == "" {
		t.Fatal("vanilla has no identity")
	}
	if !pk.SamePack(VanillaPack("")) {
		t.Fatal("a vanilla world is not claimed by a vanilla pack")
	}
	if pk.SamePack(Pack{Source: "modrinth", Slug: "cobblemon-fabric"}) {
		t.Fatal("a vanilla pack claims a Modrinth pack's world")
	}
}

// Vanilla is one pack that can be any row of the table. The two facts pull in
// opposite directions on purpose: SamePack stays true across Minecraft versions so
// a reinstall is an update of the same profile, and ChangedMinecraft is what stops
// that update from carrying a 1.20.1 world into 1.21.1. A version that made
// SamePack false would turn a reinstall into "someone else's pack" and lose the
// world without ever asking.
func TestVanillaPackTakesARowOfTheTable(t *testing.T) {
	for _, m := range Minecrafts {
		pk := VanillaPack(m.MC)
		if err := CheckPack(pk); err != nil {
			t.Fatalf("%s: %v", m.MC, err)
		}
		if pk.MC != m.MC || pk.VersionID != m.MC || pk.VersionNumber != m.MC {
			t.Fatalf("%s landed as %+v", m.MC, pk)
		}
		if !pk.SamePack(VanillaPack("")) {
			t.Fatalf("a %s vanilla install is not the same pack as a plain one", m.MC)
		}
		if pk.MC != SupportedMC && !ChangedMinecraft(VanillaPack(""), pk) {
			t.Fatalf("1.20.1 → %s is not seen as a Minecraft change", m.MC)
		}
	}

	// The table's spelling wins, because pack.json is compared with ChangedMinecraft
	// and shown in the panel: "1.21.1 " and "1.21.1" are one game.
	if pk := VanillaPack(" 1.21.1 "); pk.MC != "1.21.1" {
		t.Fatalf("whitespace survived into pack.json: %q", pk.MC)
	}
	if pk := VanillaPack(""); pk.MC != SupportedMC {
		t.Fatalf("an unnamed version moved the default to %q", pk.MC)
	}

	// Anything outside the table has no JDK here, so it is refused in the words the
	// panel shows rather than written and discovered at boot.
	if err := CheckPack(VanillaPack("1.16.5")); err == nil || !strings.Contains(err.Error(), MinecraftList()) {
		t.Fatalf("a version outside the ranges was not refused with them: %v", err)
	}
	// And one inside a range that no button offers is accepted: the offers are a
	// shortlist, not the boundary.
	if err := CheckPack(VanillaPack("1.19.4")); err != nil {
		t.Fatalf("1.19.4 is Java 17 and should be accepted: %v", err)
	}
}

func TestRunNewWorldPutsTheOldWorldInTheTrash(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("the world about to be replaced")
	st.worlds[key("pack.json")] = []byte(`{"source":"modrinth","slug":"test-pack","versionId":"older"}`)

	if _, err := Run(context.Background(), st, Options{
		Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds", NewWorld: true,
	}); err != nil {
		t.Fatalf("new-world run: %v", err)
	}
	if _, ok := st.worlds[key("world.tar.gz")]; ok {
		t.Fatal("--new-world left the old world behind")
	}

	// And it is in the trash, named the way `outpost world delete` names it, so
	// `world list` skips it and the sweep can date it.
	var trashed []string
	for k := range st.worlds {
		if strings.HasPrefix(k, trash.Prefix) {
			trashed = append(trashed, k)
		}
	}
	if len(trashed) != 1 {
		t.Fatalf("trash = %v", trashed)
	}
	if got := string(st.worlds[trashed[0]]); got != "the world about to be replaced" {
		t.Fatalf("the trashed world is %q", got)
	}
	if at, ok := trash.Time(trashed[0]); !ok {
		t.Fatalf("%s has no readable stamp", trashed[0])
	} else if time.Since(at) > time.Hour {
		t.Fatalf("the trash key is dated %s, which is not now", at)
	}
}

func TestRunNewWorldAbortsWhenTheCopyFails(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("the only copy")
	st.worlds[key("pack.json")] = []byte(`{"source":"modrinth","slug":"test-pack","versionId":"older"}`)
	st.failCopy = true

	_, err := Run(context.Background(), st, Options{
		Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds", NewWorld: true,
	})
	if err == nil || !strings.Contains(err.Error(), "nothing was discarded") {
		t.Fatalf("want a refusal that says nothing was discarded, got %v", err)
	}
	// The old world is exactly where it was, and the marker still describes it.
	if got := string(st.worlds[key("world.tar.gz")]); got != "the only copy" {
		t.Fatalf("the old world was removed anyway: %q", got)
	}
	var marker Pack
	if err := json.Unmarshal(st.worlds[key("pack.json")], &marker); err != nil {
		t.Fatal(err)
	}
	if marker.VersionID != "older" {
		t.Fatalf("the marker was rewritten to %q after a failed discard", marker.VersionID)
	}
}

func TestRunNewWorldSweepsTheTrashItGrows(t *testing.T) {
	dir := writeModpack(t, testPack())
	st := newFakeStore()
	st.worlds[key("world.tar.gz")] = []byte("world")
	st.worlds[key("pack.json")] = []byte(`{"source":"modrinth","slug":"test-pack","versionId":"older"}`)
	// An entry from before the retention window, and one that is still wanted.
	old := trash.Prefix + "test-pack-20260101T000000Z.tar.gz"
	fresh := trash.Prefix + "homestead-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	st.worlds[old] = []byte("long gone")
	st.worlds[fresh] = []byte("still wanted")

	if _, err := Run(context.Background(), st, Options{
		Dir: dir, AssetBucket: "mc-assets", WorldBucket: "mc-worlds", NewWorld: true,
	}); err != nil {
		t.Fatalf("new-world run: %v", err)
	}
	if _, ok := st.worlds[old]; ok {
		t.Fatal("the expired trash entry was not swept")
	}
	if _, ok := st.worlds[fresh]; !ok {
		t.Fatal("the sweep removed a world still inside its retention")
	}
}

// The table is what the boot, the refusals and the picker all read, so its shape is
// an invariant rather than a preference: row zero is the default a pack with no
// version gets, and every row is bootable.
func TestMinecraftTableIsConsistent(t *testing.T) {
	if len(Minecrafts) == 0 {
		t.Fatal("no Minecraft version is bootable")
	}
	if Minecrafts[0].MC != SupportedMC {
		t.Fatalf("SupportedMC = %s but row zero is %s", SupportedMC, Minecrafts[0].MC)
	}
	seen := map[string]bool{}
	for _, m := range Minecrafts {
		if seen[m.MC] {
			t.Fatalf("%s is in the table twice", m.MC)
		}
		seen[m.MC] = true
		java, ok := JavaFor(m.MC)
		if !ok || java != m.Java {
			t.Fatalf("JavaFor(%s) = %d, %v; the table says %d", m.MC, java, ok, m.Java)
		}
		if !Runnable(m.MC) {
			t.Fatalf("%s is a row and not runnable", m.MC)
		}
	}
	a, _ := JavaFor("1.20.1")
	b, _ := JavaFor("1.21.1")
	if a == b {
		t.Fatal("a Minecraft version change that does not change the JDK: the table is not saying anything")
	}
	if ChangedMinecraft(Pack{MC: "1.20.1"}, Pack{MC: "1.21.1"}) != true {
		t.Fatal("1.20.1 → 1.21.1 is not a Minecraft version change")
	}
	if ChangedMinecraft(Pack{MC: "1.20.1"}, Pack{}) || ChangedMinecraft(Pack{}, Pack{MC: "1.21.1"}) {
		t.Fatal("a pack that names no version is evidence of a change")
	}
}
