package world

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"outpost/internal/publish"
	"outpost/internal/r2"
)

const (
	assetBucket = "mc-assets"
	worldBucket = "mc-worlds"
)

// fakeStore is bucket-aware: `data` is keyed by bucket so a test can see which
// one a key went to. It also records what was copied and what was read, because
// the point of an import is that the world is copied and never read.
type fakeStore struct {
	data     map[string]map[string][]byte
	copies   []string
	gets     []string
	failCopy bool
	// partialCopy writes a truncated object at the destination and then fails, the
	// way a multipart copy that lost a part does.
	partialCopy bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: map[string]map[string][]byte{}}
}

func (f *fakeStore) bucket(name string) map[string][]byte {
	if f.data[name] == nil {
		f.data[name] = map[string][]byte{}
	}
	return f.data[name]
}

func (f *fakeStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	f.gets = append(f.gets, bucket+"/"+key)
	b, ok := f.bucket(bucket)[key]
	if !ok {
		return nil, r2.ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) Put(_ context.Context, bucket, key string, r io.Reader, size int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return io.ErrUnexpectedEOF
	}
	f.bucket(bucket)[key] = b
	return nil
}

func (f *fakeStore) Stat(_ context.Context, bucket, key string) (r2.Info, error) {
	b, ok := f.bucket(bucket)[key]
	if !ok {
		return r2.Info{}, r2.ErrNotFound
	}
	return r2.Info{Size: int64(len(b))}, nil
}

func (f *fakeStore) List(_ context.Context, bucket, prefix string) ([]r2.Object, error) {
	var out []r2.Object
	for k, v := range f.bucket(bucket) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, r2.Object{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func (f *fakeStore) Remove(_ context.Context, bucket, key string) error {
	delete(f.bucket(bucket), key)
	return nil
}

func (f *fakeStore) Copy(_ context.Context, bucket, srcKey, dstKey string) (int64, error) {
	if f.partialCopy {
		f.bucket(bucket)[dstKey] = []byte("half a wo")
		return 0, errors.New("the multipart copy failed")
	}
	if f.failCopy {
		return 0, errors.New("the multipart copy failed")
	}
	b, ok := f.bucket(bucket)[srcKey]
	if !ok {
		return 0, r2.ErrNotFound
	}
	f.copies = append(f.copies, srcKey+" -> "+dstKey)
	f.bucket(bucket)[dstKey] = append([]byte(nil), b...)
	return int64(len(b)), nil
}

// wrote reports whether any key under prefix exists in a bucket.
func (f *fakeStore) wrote(bucket, prefix string) bool {
	for k := range f.bucket(bucket) {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func testClock() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }

func TestImportCopiesTheLegacyWorldAndNeverReadsIt(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)[LegacyKey] = []byte("eleven megabytes of terrain, honestly")

	res, err := Import(context.Background(), st, ImportOptions{
		WorldBucket: worldBucket,
		AssetBucket: assetBucket,
		Profile:     "vanilla",
		Now:         testClock,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if got := string(st.bucket(worldBucket)["profiles/vanilla/world.tar.gz"]); got != "eleven megabytes of terrain, honestly" {
		t.Fatalf("the world did not arrive intact: %q", got)
	}
	// Copy, not move: the source is the only copy until the profile has one.
	if _, ok := st.bucket(worldBucket)[LegacyKey]; !ok {
		t.Fatal("the import deleted the source world")
	}
	if len(st.copies) != 1 {
		t.Fatalf("want one server-side copy, got %v", st.copies)
	}
	// The 8 MiB read limit in r2.Get is why this matters: a read-and-write copy
	// would report success on a truncated world.
	for _, g := range st.gets {
		if strings.HasSuffix(g, LegacyKey) {
			t.Fatalf("the world was read through Get: %v", st.gets)
		}
	}
	if res.Bytes != int64(len("eleven megabytes of terrain, honestly")) {
		t.Fatalf("bytes = %d", res.Bytes)
	}
	if res.AlreadyThere {
		t.Fatal("a first import is not already-there")
	}
}

func TestImportMarksAWorldWithNoPackAsHandRolled(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)[LegacyKey] = []byte("world")

	if _, err := Import(context.Background(), st, ImportOptions{
		WorldBucket: worldBucket,
		AssetBucket: assetBucket,
		Profile:     "vanilla",
		Now:         testClock,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	raw, ok := st.bucket(worldBucket)["profiles/vanilla/pack.json"]
	if !ok {
		t.Fatal("no marker was written beside the imported world")
	}
	marker := string(raw)
	// Hand-rolled means no source and no slug, which the publish guard reads as
	// "compatible with any hand-rolled publish, ask before a Modrinth pack takes
	// it". Claiming a pack nobody checked would be worse than saying nothing.
	if strings.Contains(marker, "modrinth") {
		t.Fatalf("the marker claimed a pack: %s", marker)
	}
	if !strings.Contains(marker, "imported world") {
		t.Fatalf("marker = %s", marker)
	}
	if !strings.Contains(marker, "2026-11-15T12:00:00Z") {
		t.Fatalf("marker has no timestamp: %s", marker)
	}
}

func TestImportUsesTheMarkersOfThePackThatWillBootIt(t *testing.T) {
	installed := publish.Pack{
		Source: "modrinth", Slug: "fabulously-optimized", Name: "Fabulously Optimized",
		VersionID: "ver1", VersionNumber: "5.2.8", MC: "1.20.1", Loader: "fabric",
	}
	raw := mustJSON(t, installed)

	st := newFakeStore()
	st.bucket(worldBucket)[LegacyKey] = []byte("world")
	st.bucket(assetBucket)["profiles/fo/pack.json"] = raw

	res, err := Import(context.Background(), st, ImportOptions{
		WorldBucket: worldBucket,
		AssetBucket: assetBucket,
		Profile:     "fo",
		Now:         testClock,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Marker.Slug != "fabulously-optimized" || res.Marker.VersionID != "ver1" {
		t.Fatalf("the marker does not name the installed pack: %+v", res.Marker)
	}
	// Written in publish's indented form, so compare what it says, not its bytes.
	var written publish.Pack
	if err := json.Unmarshal(st.bucket(worldBucket)["profiles/fo/pack.json"], &written); err != nil {
		t.Fatalf("marker is not a pack: %v", err)
	}
	if written.Slug != installed.Slug || written.VersionID != installed.VersionID || written.MC != installed.MC {
		t.Fatalf("marker = %+v, want %+v", written, installed)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)[LegacyKey] = []byte("older save")
	st.bucket(worldBucket)["profiles/vanilla/world.tar.gz"] = []byte("the world being played on")

	opts := ImportOptions{WorldBucket: worldBucket, AssetBucket: assetBucket, Profile: "vanilla", Now: testClock}
	res, err := Import(context.Background(), st, opts)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !res.AlreadyThere {
		t.Fatal("importing over an existing world should say so")
	}
	if kept := string(st.bucket(worldBucket)["profiles/vanilla/world.tar.gz"]); kept != "the world being played on" {
		t.Fatalf("the existing world was overwritten: %q", kept)
	}
	if len(st.copies) != 0 {
		t.Fatalf("an already-there import still copied: %v", st.copies)
	}
	// The line says "profile X already has a world (<size>)", and that size is the
	// world being kept, not the one that was offered.
	if res.Bytes != int64(len("the world being played on")) {
		t.Fatalf("bytes = %d, want the existing world's size", res.Bytes)
	}
	if st.wrote(worldBucket, "profiles/vanilla/pack.json") {
		t.Fatal("an already-there import rewrote the marker")
	}
}

func TestImportRefusesWhenTheCopyFailsAndWritesNoMarker(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)[LegacyKey] = []byte("world")
	st.failCopy = true

	if _, err := Import(context.Background(), st, ImportOptions{
		WorldBucket: worldBucket, AssetBucket: assetBucket, Profile: "vanilla", Now: testClock,
	}); err == nil {
		t.Fatal("a failed copy must fail the import")
	}
	// A marker with no world beside it would tell the next publish the profile is
	// occupied when it is empty.
	if st.wrote(worldBucket, "profiles/vanilla/pack.json") {
		t.Fatal("a failed import wrote a marker")
	}
}

func TestImportSaysWhatIsMissingWhenThereIsNothingToImport(t *testing.T) {
	_, err := Import(context.Background(), newFakeStore(), ImportOptions{
		WorldBucket: worldBucket, AssetBucket: assetBucket, Profile: "vanilla",
	})
	if err == nil || !strings.Contains(err.Error(), LegacyKey) {
		t.Fatalf("want a refusal naming the key, got %v", err)
	}
}

func TestImportFromAnotherKey(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)["current/world-old.tar.gz"] = []byte("an older archive")

	if _, err := Import(context.Background(), st, ImportOptions{
		WorldBucket: worldBucket, AssetBucket: assetBucket, Profile: "vanilla",
		From: "current/world-old.tar.gz", Now: testClock,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := string(st.bucket(worldBucket)["profiles/vanilla/world.tar.gz"]); got != "an older archive" {
		t.Fatalf("got %q", got)
	}
}

func TestImportRefusesANameThatCouldNotBeAKey(t *testing.T) {
	_, err := Import(context.Background(), newFakeStore(), ImportOptions{
		WorldBucket: worldBucket, AssetBucket: assetBucket, Profile: "Vanilla World",
	})
	if err == nil || !strings.Contains(err.Error(), "not a name") {
		t.Fatalf("want a bad-profile refusal, got %v", err)
	}
}

func TestListShowsProfilesWorldsAndOrphans(t *testing.T) {
	st := newFakeStore()
	pk := mustJSON(t, publish.Pack{Source: "modrinth", Slug: "fo", Name: "Fabulously Optimized", VersionNumber: "5.2.8"})
	st.bucket(worldBucket)["profiles/fo/world.tar.gz"] = []byte("sixteen bytes ok")
	st.bucket(worldBucket)["profiles/fo/pack.json"] = pk
	// Retention copies and the trash are not worlds you can boot, so they are not
	// in the list of worlds you can boot.
	st.bucket(worldBucket)["profiles/fo/backups/world-2026-11-01.tar.gz"] = []byte("old")
	st.bucket(worldBucket)["profiles/fo/trash/world-2026-11-02.tar.gz"] = []byte("binned")
	// The pre-profile world: nobody claims it, and nothing else will mention it.
	st.bucket(worldBucket)[LegacyKey] = []byte("the lost world")
	st.bucket(worldBucket)["profiles/fo/server/server.properties"] = []byte("not a world")

	worlds, err := List(context.Background(), st, worldBucket)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(worlds) != 2 {
		t.Fatalf("want 2 worlds, got %d: %+v", len(worlds), worlds)
	}
	if worlds[0].Profile != "fo" || worlds[0].Orphaned() {
		t.Fatalf("the profile's world should come first: %+v", worlds[0])
	}
	if worlds[0].Pack == nil || worlds[0].Pack.Slug != "fo" {
		t.Fatalf("the marker was not read: %+v", worlds[0].Pack)
	}
	if worlds[1].Key != LegacyKey || !worlds[1].Orphaned() {
		t.Fatalf("the orphan should be last and unclaimed: %+v", worlds[1])
	}
	if worlds[1].Pack != nil {
		t.Fatalf("an orphan with no marker claimed a pack: %+v", worlds[1].Pack)
	}
	if worlds[1].Size != int64(len("the lost world")) {
		t.Fatalf("size = %d", worlds[1].Size)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestImportCleansUpAPartialCopySoARetryRetries(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)[LegacyKey] = []byte("the whole world, eventually")
	st.partialCopy = true

	opts := ImportOptions{WorldBucket: worldBucket, AssetBucket: assetBucket, Profile: "vanilla", Now: testClock}
	if _, err := Import(context.Background(), st, opts); err == nil {
		t.Fatal("a failed copy must fail the import")
	}
	// A destination left behind would be adopted by the next run's idempotency
	// check, and no command in the tool can replace it afterwards.
	if _, ok := st.bucket(worldBucket)["profiles/vanilla/world.tar.gz"]; ok {
		t.Fatal("a partial copy was left where the profile's world goes")
	}

	st.partialCopy = false
	res, err := Import(context.Background(), st, opts)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.AlreadyThere {
		t.Fatal("the retry saw a partial world and called it the profile's")
	}
	if got := string(st.bucket(worldBucket)["profiles/vanilla/world.tar.gz"]); got != "the whole world, eventually" {
		t.Fatalf("retry wrote %q", got)
	}
}

func TestListReadsAMarkerThatSitsBesideAnOrphan(t *testing.T) {
	st := newFakeStore()
	// What publish wrote before profiles: the world and its marker side by side.
	st.bucket(worldBucket)[LegacyKey] = []byte("the lost world")
	st.bucket(worldBucket)["current/pack.json"] = mustJSON(t, publish.Pack{Source: "modrinth", Slug: "fabulously-optimized", Name: "Fabulously Optimized"})

	worlds, err := List(context.Background(), st, worldBucket)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(worlds) != 1 || !worlds[0].Orphaned() {
		t.Fatalf("worlds = %+v", worlds)
	}
	if worlds[0].Pack == nil || worlds[0].Pack.Slug != "fabulously-optimized" {
		t.Fatalf("the marker beside the orphan was not read: %+v", worlds[0].Pack)
	}
}

func TestDeleteTrashesTheWorldBeforeItRemovesIt(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)["profiles/fo/world.tar.gz"] = []byte("the only copy")
	st.bucket(worldBucket)["profiles/fo/pack.json"] = []byte(`{"slug":"fo"}`)

	res, err := Delete(context.Background(), st, DeleteOptions{
		WorldBucket: worldBucket, Profile: "fo", Now: testClock,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := st.bucket(worldBucket)["profiles/fo/world.tar.gz"]; ok {
		t.Fatal("the live world is still there")
	}
	if got := string(st.bucket(worldBucket)[res.Trash]); got != "the only copy" {
		t.Fatalf("the trash copy is %q", got)
	}
	if res.Trash != "trash/fo-20261115T120000Z.tar.gz" {
		t.Fatalf("trash key = %q", res.Trash)
	}
	want := time.Date(2026, 11, 29, 12, 0, 0, 0, time.UTC)
	if !res.Expires.Equal(want) {
		t.Fatalf("expires = %s, want %s", res.Expires, want)
	}
	if res.Bytes != int64(len("the only copy")) {
		t.Fatalf("bytes = %d", res.Bytes)
	}
	// The marker describes what will boot the *next* world in this profile, and
	// deleting the world does not change which pack that is.
	if _, ok := st.bucket(worldBucket)["profiles/fo/pack.json"]; !ok {
		t.Fatal("deleting a world deleted the profile's marker")
	}
}

func TestDeleteStopsWhenTheTrashCopyFails(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)["profiles/fo/world.tar.gz"] = []byte("the only copy")
	st.failCopy = true

	_, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Profile: "fo", Now: testClock})
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("want a refusal that says nothing was deleted, got %v", err)
	}
	if _, ok := st.bucket(worldBucket)["profiles/fo/world.tar.gz"]; !ok {
		t.Fatal("a failed copy removed the world anyway")
	}
}

func TestDeleteOfSomethingGoneIsAMessageNotAnError(t *testing.T) {
	st := newFakeStore()
	res, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Profile: "fo", Now: testClock})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !res.Missing || res.Trash != "" {
		t.Fatalf("res = %+v", res)
	}

	// And a second delete of the same world is the same message.
	st.bucket(worldBucket)["profiles/fo/world.tar.gz"] = []byte("world")
	if _, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Profile: "fo", Now: testClock}); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	again, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Profile: "fo", Now: testClock})
	if err != nil || !again.Missing {
		t.Fatalf("second delete = %+v, %v", again, err)
	}
}

func TestDeleteSweepsTheTrashPastItsRetentionAndOnlyThat(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)["profiles/fo/world.tar.gz"] = []byte("world")
	// Exactly fourteen days old, a day old, and a key this code did not write.
	st.bucket(worldBucket)["trash/old-20261101T120000Z.tar.gz"] = []byte("expired")
	st.bucket(worldBucket)["trash/fresh-20261114T120000Z.tar.gz"] = []byte("still wanted")
	st.bucket(worldBucket)["trash/somebody-elses.tar.gz"] = []byte("no stamp")

	res, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Profile: "fo", Now: testClock})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(res.Swept) != 1 || res.Swept[0] != "trash/old-20261101T120000Z.tar.gz" {
		t.Fatalf("swept = %v", res.Swept)
	}
	if _, ok := st.bucket(worldBucket)["trash/fresh-20261114T120000Z.tar.gz"]; !ok {
		t.Fatal("the sweep removed a world that is still inside its retention")
	}
	// A key with no readable stamp was not written by this code, and guessing at
	// its age would delete somebody else's thing.
	if _, ok := st.bucket(worldBucket)["trash/somebody-elses.tar.gz"]; !ok {
		t.Fatal("the sweep removed a key it could not date")
	}
}

func TestDeleteByKeyAdoptsAnOrphan(t *testing.T) {
	st := newFakeStore()
	st.bucket(worldBucket)[LegacyKey] = []byte("the lost world")

	res, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Key: LegacyKey, Now: testClock})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Trash != "trash/current-world-20261115T120000Z.tar.gz" {
		t.Fatalf("trash key = %q", res.Trash)
	}
	if _, ok := st.bucket(worldBucket)[LegacyKey]; ok {
		t.Fatal("the orphan is still there")
	}
	if _, ok := st.bucket(worldBucket)[res.Trash]; !ok {
		t.Fatal("no trash copy of the orphan")
	}
}

func TestDeleteRefusesAKeyThatIsNotAWorldAndBothNamesAtOnce(t *testing.T) {
	st := newFakeStore()
	if _, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Key: "profiles/fo/pack.json"}); err == nil {
		t.Fatal("want a refusal for a key that is not an archive")
	}
	if _, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Profile: "fo", Key: LegacyKey}); err == nil {
		t.Fatal("want a refusal for naming both a profile and a key")
	}
	if _, err := Delete(context.Background(), st, DeleteOptions{WorldBucket: worldBucket, Profile: "Fo!"}); err == nil {
		t.Fatal("want a refusal for a profile that could not be a key")
	}
}
