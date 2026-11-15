package publish

import (
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

func TestBuildRefusesAPackForAnotherMinecraftVersion(t *testing.T) {
	p := testPack()
	p.MC = "1.21.1"
	dir := writeModpack(t, p)

	_, _, err := Build(context.Background(), Options{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "Java 17") {
		t.Fatalf("want a Java 17 refusal, got %v", err)
	}
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

func TestRunClearsTheMarkerWhenPublishingAHandRolledPack(t *testing.T) {
	// No pack.json: an ordinary directory of jars, after a fetched pack was
	// published. The marker still names Cobblemon, so it has to go.
	dir := writePack(t)
	st := newFakeStore()
	// A fetched pack was installed here first: it left a manifest in the asset
	// bucket and a marker in the world bucket.
	if _, err := Run(context.Background(), st, Options{
		Dir:         writeModpack(t, testPack()),
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
	}); err != nil {
		t.Fatalf("packed publish: %v", err)
	}
	st.worlds[key("world.tar.gz")] = []byte("world")

	if _, err := Run(context.Background(), st, Options{
		Dir:         dir,
		AssetBucket: "mc-assets",
		WorldBucket: "mc-worlds",
	}); err != nil {
		t.Fatalf("hand-rolled publish: %v", err)
	}
	if _, ok := st.worlds[key("pack.json")]; ok {
		t.Fatal("a stale pack marker survived a hand-rolled publish")
	}
	// The asset bucket had a manifest of its own (uploaded by the packed publish
	// above in spirit): the page reads that one to name the profile, so it has to
	// go too, or a hand-rolled pack keeps being called Fabulously Optimized.
	if _, ok := st.objects[key("pack.json")]; ok {
		t.Fatal("a stale asset manifest survived a hand-rolled publish")
	}
	if _, ok := st.worlds[key("world.tar.gz")]; !ok {
		t.Fatal("a hand-rolled publish removed the world")
	}

	// And the next fetched publish cannot trust a marker that is gone.
	pack := writeModpack(t, testPack())
	_, err := Run(context.Background(), st, Options{Dir: pack, AssetBucket: "mc-assets", WorldBucket: "mc-worlds"})
	if err == nil || !strings.Contains(err.Error(), "--new-world") {
		t.Fatalf("want the guard to ask, got %v", err)
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

	// A hand-rolled directory has no manifest, and must not gain an empty one.
	plain := writePack(t)
	objects, _, err = Build(context.Background(), Options{Dir: plain, Profile: "default"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, o := range objects {
		if strings.HasSuffix(o.Key, "/pack.json") {
			t.Fatalf("a hand-rolled pack grew a manifest: %s", o.Key)
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
	pk := VanillaPack()
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
	if !pk.SamePack(VanillaPack()) {
		t.Fatal("a vanilla world is not claimed by a vanilla pack")
	}
	if pk.SamePack(Pack{Source: "modrinth", Slug: "cobblemon-fabric"}) {
		t.Fatal("a vanilla pack claims a Modrinth pack's world")
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
