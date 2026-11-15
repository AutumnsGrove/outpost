// Package trash is where a world waits after it has been deleted.
//
// Three commands remove a world and none of them removes it in one step: the
// copy goes to `trash/<name>-<stamp>.tar.gz`, the destination is verified, and
// only then is the live key removed. `outpost world delete` and `outpost pack
// delete` do that, and so does `publish --new-world` — the page's "replace the
// world" checkbox is the same discard, and it is the one a person reaches for
// casually.
//
// The naming lives here rather than in either caller because two packages write
// these keys: `world` deletes, and `publish` discards. If they drifted,
// `world list` would stop recognising the entries it has to skip and the sweep
// would skip keys it cannot date — a trash that silently never empties.
//
// The trash expires rather than growing for ever. It is swept lazily, on the
// next delete or discard, because that is exactly when it grows; the age comes
// from the key, so an entry this code did not write is left alone rather than
// guessed at.
package trash

import (
	"context"
	"strings"
	"time"

	"outpost/internal/r2"
)

const (
	// Prefix is where trashed worlds live, at the top level of the world bucket.
	Prefix = "trash/"

	// RetentionDays is how long a trashed world can be brought back. It matches
	// the `--min-age 14d` the shutdown upload uses for `backups/`.
	RetentionDays = 14

	// Retention is RetentionDays as a duration.
	Retention = time.Duration(RetentionDays) * 24 * time.Hour

	// stamp is the time format in a trash key. Fixed-width and UTC, so the keys
	// sort and parse without asking R2 for a modification time.
	stamp = "20060102T150405Z"

	// archiveSuffix is what a world key ends in. A trash key keeps it so the same
	// tools can unpack either.
	archiveSuffix = ".tar.gz"
)

// Key names the trash copy of a world key: `profiles/cobblemon/world.tar.gz`
// becomes `trash/cobblemon-20261115T120000Z.tar.gz`. A profile's live world
// reduces to the profile name; anything else keeps enough of its path to say
// which copy it was.
func Key(worldKey string, at time.Time) string {
	return Prefix + name(worldKey) + "-" + at.UTC().Format(stamp) + archiveSuffix
}

// Expires is when a trash key was written, plus the retention.
func Expires(at time.Time) time.Time { return at.Add(Retention) }

// name turns a world key into the readable half of a trash key.
//
// It never returns empty, because a key that starts with the dash before the
// stamp is a key Time refuses to date — a trash entry that would never be swept.
// `outpost world delete --key` accepts any archive, and a path with no name left
// in it after the profile is still a world.
func name(worldKey string) string {
	out := readable(worldKey)
	if out == "" {
		return "world"
	}
	return out
}

// readable is the name part, which may be empty for a degenerate path.
func readable(worldKey string) string {
	trimmed := strings.TrimSuffix(worldKey, archiveSuffix)
	after, inside := strings.CutPrefix(trimmed, "profiles/")
	if !inside {
		return strings.ReplaceAll(trimmed, "/", "-")
	}
	profile, rest, found := strings.Cut(after, "/")
	if !found {
		return profile
	}
	// profiles/<id>/world.tar.gz is the live world: the profile name says it all.
	if rest == "world" {
		return profile
	}
	return strings.ReplaceAll(profile+"-"+rest, "/", "-")
}

// Store is the slice of R2 sweeping needs.
type Store interface {
	List(ctx context.Context, bucket, prefix string) ([]r2.Object, error)
	Remove(ctx context.Context, bucket, key string) error
}

// Sweep removes the entries that have outlived their retention and reports what
// went. An entry whose name does not parse was not written by this code and is
// left alone: guessing at the age of somebody else's object would delete it.
func Sweep(ctx context.Context, st Store, bucket string, now time.Time) ([]string, error) {
	objects, err := st.List(ctx, bucket, Prefix)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, o := range objects {
		at, ok := Time(o.Key)
		if !ok || now.Sub(at) < Retention {
			continue
		}
		if err := st.Remove(ctx, bucket, o.Key); err != nil {
			return removed, err
		}
		removed = append(removed, o.Key)
	}
	return removed, nil
}

// Time reads the stamp out of a trash key.
func Time(key string) (time.Time, bool) {
	base := strings.TrimSuffix(strings.TrimPrefix(key, Prefix), archiveSuffix)
	dash := strings.LastIndex(base, "-")
	// A name, a dash, a stamp: `trash/-20261115T120000Z.tar.gz` names nothing and
	// `trash/whatever.tar.gz` is not dated. Neither is ours to delete.
	if dash <= 0 || dash == len(base)-1 {
		return time.Time{}, false
	}
	at, err := time.Parse(stamp, base[dash+1:])
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}
