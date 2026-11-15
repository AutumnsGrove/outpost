package trash

import (
	"context"
	"strings"
	"testing"
	"time"

	"outpost/internal/r2"
)

func TestKeyNamesAWorldRecognisably(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		// The live world: the profile name says everything.
		"profiles/cobblemon/world.tar.gz":            "trash/cobblemon-20261115T120000Z.tar.gz",
		"profiles/fabulously-optimized/world.tar.gz": "trash/fabulously-optimized-20261115T120000Z.tar.gz",
		// An orphan keeps its path, flattened, so it is still recognisable.
		"current/world.tar.gz": "trash/current-world-20261115T120000Z.tar.gz",
		// A backup under a profile keeps enough to say which copy it was.
		"profiles/a/backups/world-2026.tar.gz": "trash/a-backups-world-2026-20261115T120000Z.tar.gz",
	}
	for worldKey, want := range cases {
		if got := Key(worldKey, at); got != want {
			t.Errorf("Key(%q) = %q, want %q", worldKey, got, want)
		}
	}
}

func TestTimeReadsBackWhatKeyWrote(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	key := Key("profiles/cobblemon/world.tar.gz", at)

	got, ok := Time(key)
	if !ok || !got.Equal(at) {
		t.Fatalf("Time(%q) = %s, %v", key, got, ok)
	}
	if want := at.Add(Retention); !Expires(at).Equal(want) {
		t.Fatalf("Expires = %s, want %s", Expires(at), want)
	}

	// Names this code did not write are left undated, not guessed at.
	for _, key := range []string{"trash/whatever.tar.gz", "trash/-20261115T120000Z.tar.gz", "trash/notatime.tar.gz", "trash/"} {
		if _, ok := Time(key); ok {
			t.Errorf("Time(%q) claimed to know the date", key)
		}
	}
}

// sweepStore is the two calls Sweep needs.
type sweepStore struct {
	objects map[string][]byte
	removed []string
}

func (s *sweepStore) List(_ context.Context, _, prefix string) ([]r2.Object, error) {
	var out []r2.Object
	for k, v := range s.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, r2.Object{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func (s *sweepStore) Remove(_ context.Context, _, key string) error {
	delete(s.objects, key)
	s.removed = append(s.removed, key)
	return nil
}

func TestSweepRemovesExactlyWhatHasExpired(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	st := &sweepStore{objects: map[string][]byte{
		// Exactly at the boundary: 14 days old is out.
		Prefix + "old-20261101T120000Z.tar.gz":          []byte("expired"),
		Prefix + "still-wanted-20261114T120000Z.tar.gz": []byte("a day old"),
		Prefix + "somebody-elses.tar.gz":                []byte("no stamp"),
		// Not in the trash at all.
		"profiles/fo/world.tar.gz": []byte("a live world"),
	}}

	removed, err := Sweep(context.Background(), st, "mc-worlds", now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(removed) != 1 || !strings.Contains(removed[0], "old-") {
		t.Fatalf("removed = %v", removed)
	}
	for _, keep := range []string{
		Prefix + "still-wanted-20261114T120000Z.tar.gz",
		Prefix + "somebody-elses.tar.gz",
		"profiles/fo/world.tar.gz",
	} {
		if _, ok := st.objects[keep]; !ok {
			t.Fatalf("%s was swept", keep)
		}
	}
}
