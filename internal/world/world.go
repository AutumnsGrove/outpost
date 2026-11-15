// Package world is the mc-worlds side of the buckets: which worlds exist, which
// profile claims one, and moving a world into a profile without ever deleting
// the only copy.
//
// A world is one tarball, `profiles/<profile>/world.tar.gz`, written by the VPS
// at shutdown with rclone. Its marker, `profiles/<profile>/pack.json`, sits
// beside it in the same bucket and says what made it, so the publish guard can
// tell "same pack, newer version" from "a different game". Neither is ever read
// whole: the world is far bigger than r2.Get's 8 MiB limit, which is why an
// import is a server-side copy and not a read and a write.
package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"outpost/internal/publish"
	"outpost/internal/r2"
	"outpost/internal/trash"
)

const (
	// ArchiveName is the one file a live world lives in.
	ArchiveName = "world.tar.gz"

	// LegacyKey is the world from before profiles existed. Nothing reads it any
	// more, and it is the only copy of six months of play, so it is imported
	// rather than moved and never removed by an import.
	LegacyKey = "current/world.tar.gz"
)

// Store is the slice of R2 a world command needs: all of it. Copy and not a read
// and a write is the point — Get caps at 8 MiB and would truncate a world that
// has been played on for more than a week.
type Store = publish.Store

// World is one archived world and who claims it.
type World struct {
	// Profile is empty for a world no profile claims: the bucket is the only
	// thing that remembers it exists, and it is the thing to import or delete.
	Profile string
	Key     string
	Size    int64
	// Pack is what the marker beside the world says made it, when there is one.
	// Nil means no marker, or one that cannot be read.
	Pack *publish.Pack
}

// Orphaned reports that no profile claims this world.
func (w World) Orphaned() bool { return w.Profile == "" }

// List returns every live world, newest concern first: the profiles' own
// archives, then whatever sits in the bucket with no profile behind it.
//
// Backups and the trash under a profile are left out on purpose. They are
// retention copies of a world that is already listed, and a list that mixes
// "what you can boot" with "what is kept in case" cannot answer which is which.
func List(ctx context.Context, st Store, bucket string) ([]World, error) {
	objs, err := st.List(ctx, bucket, "")
	if err != nil {
		return nil, err
	}

	var worlds []World
	for _, o := range objs {
		profile, rest, inside := splitKey(o.Key)
		switch {
		case inside && rest == ArchiveName:
			w := World{Profile: profile, Key: o.Key, Size: o.Size}
			if raw, err := st.Get(ctx, bucket, publish.ProfilePrefix(profile)+"pack.json"); err == nil {
				var p publish.Pack
				if json.Unmarshal(raw, &p) == nil {
					w.Pack = &p
				}
			}
			worlds = append(worlds, w)
		case !inside && strings.HasSuffix(o.Key, ".tar.gz") && !strings.HasPrefix(o.Key, trash.Prefix):
			// Not under profiles/ and not in the trash: the pre-profile world, or
			// something an older build left behind. Worth naming, because nothing
			// else will — but a trashed world is not a candidate for anything, and
			// listing it would advertise the thing just deleted. Its marker
			// sits beside it — `current/pack.json` next to `current/world.tar.gz` is
			// what publish wrote before profiles — so say what made it when that key
			// is still there.
			w := World{Key: o.Key, Size: o.Size}
			if raw, err := st.Get(ctx, bucket, markerBeside(o.Key)); err == nil {
				var p publish.Pack
				if json.Unmarshal(raw, &p) == nil {
					w.Pack = &p
				}
			}
			worlds = append(worlds, w)
		}
	}

	sort.Slice(worlds, func(i, j int) bool {
		if worlds[i].Orphaned() != worlds[j].Orphaned() {
			return !worlds[i].Orphaned()
		}
		if worlds[i].Profile != worlds[j].Profile {
			return worlds[i].Profile < worlds[j].Profile
		}
		return worlds[i].Key < worlds[j].Key
	})
	return worlds, nil
}

// splitKey answers "which profile does this key belong to". inside is false for
// a key that is not under profiles/ at all.
func splitKey(key string) (profile, rest string, inside bool) {
	after, ok := strings.CutPrefix(key, "profiles/")
	if !ok {
		return "", key, false
	}
	id, tail, ok := strings.Cut(after, "/")
	if !ok || id == "" {
		return "", key, false
	}
	return id, tail, true
}

// ImportOptions is one adoption of an existing world into a profile.
type ImportOptions struct {
	WorldBucket string
	// AssetBucket is read for the profile's pack.json: the marker written beside
	// the imported world should name the pack that will boot it.
	AssetBucket string
	Profile     string
	// From is the key to adopt. Empty means the pre-profile world, LegacyKey.
	From string
	// Now is the clock for the marker. Tests set it; empty means time.Now.
	Now func() time.Time
}

// ImportResult is what an import did, so the CLI can say it.
type ImportResult struct {
	Profile string
	From    string
	To      string
	Bytes   int64
	// AlreadyThere is an import that found a world in the profile and left it
	// alone: importing twice is not an error, and it is not an overwrite either.
	AlreadyThere bool
	// Marker is what was written beside the world, and what the guard will read
	// next time something is published into this profile.
	Marker publish.Pack
}

// Import copies a world into a profile.
//
// Copy, never move: the source is the only copy of a world until the profile has
// one, so an import that failed halfway through must leave exactly what it
// found. It is a server-side copy for the same reason it is not a read and a
// write: 8 MiB of a 12 MB world is a torn world that looks like a success.
//
// Idempotent. A profile that already holds a world is left alone — the world in
// the profile is the one a session uploads over, so it is the copy that matters,
// and overwriting it with an older import is the one mistake here that cannot be
// retried.
func Import(ctx context.Context, st Store, opts ImportOptions) (ImportResult, error) {
	if err := publish.ValidateProfile(opts.Profile); err != nil {
		return ImportResult{}, err
	}
	from := opts.From
	if from == "" {
		from = LegacyKey
	}
	res := ImportResult{
		Profile: opts.Profile,
		From:    from,
		To:      publish.ProfilePrefix(opts.Profile) + ArchiveName,
	}

	if _, err := st.Stat(ctx, opts.WorldBucket, from); err != nil {
		if errors.Is(err, r2.ErrNotFound) {
			return res, fmt.Errorf("no world at %s/%s to import", opts.WorldBucket, from)
		}
		return res, err
	}

	switch here, err := st.Stat(ctx, opts.WorldBucket, res.To); {
	case err == nil:
		res.AlreadyThere = true
		// The size of the copy that is actually in the profile, not the source's:
		// "profile X already has a world (84 MB)" is about the one being kept.
		res.Bytes = here.Size
		return res, nil
	case !errors.Is(err, r2.ErrNotFound):
		return res, err
	}

	n, err := st.Copy(ctx, opts.WorldBucket, from, res.To)
	if err != nil {
		// A copy that failed the size check — or died mid-multipart — can still
		// leave an object at the destination, and the idempotency check above would
		// then adopt that partial world for ever. A retry has to retry.
		if rmErr := st.Remove(ctx, opts.WorldBucket, res.To); rmErr != nil && !errors.Is(rmErr, r2.ErrNotFound) {
			return res, fmt.Errorf("copy %s into profile %s failed (%w) and the partial copy at %s could not be removed (%v); delete it before retrying",
				from, opts.Profile, err, res.To, rmErr)
		}
		return res, fmt.Errorf("copy %s into profile %s: %w", from, opts.Profile, err)
	}
	res.Bytes = n

	marker, err := writeMarker(ctx, st, opts)
	if err != nil {
		return res, err
	}
	res.Marker = marker
	return res, nil
}

// writeMarker records what will boot the imported world, which is what stops the
// publish guard from treating it as a world of unknown origin.
//
// The honest answer, when the profile has assets installed, is the pack those
// assets belong to: that is what a session will load this world with. Otherwise
// the marker says hand-rolled (no source, no slug), which the guard treats as
// compatible with any hand-rolled publish and as something to ask about before a
// Modrinth pack claims it.
func writeMarker(ctx context.Context, st Store, opts ImportOptions) (publish.Pack, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	marker := publish.Pack{
		Name:      "imported world",
		FetchedAt: now().UTC().Format(time.RFC3339),
	}
	if opts.AssetBucket != "" {
		if raw, err := st.Get(ctx, opts.AssetBucket, publish.ProfilePrefix(opts.Profile)+publish.PackFile); err == nil {
			var installed publish.Pack
			if json.Unmarshal(raw, &installed) == nil {
				marker = installed
			}
		}
	}
	return marker, publish.WriteMarker(ctx, st, opts.WorldBucket, opts.Profile, marker)
}

// markerBeside is where the marker for a world key lives: the same path with
// pack.json in place of the archive. That is the profile layout and the
// pre-profile one alike, because both put the two files side by side.
func markerBeside(key string) string {
	return strings.TrimSuffix(key, ArchiveName) + publish.PackFile
}

// DeleteOptions is one world going away, and it has to be named either by the
// profile it belongs to or by the key `outpost world list` printed.
type DeleteOptions struct {
	WorldBucket string
	// Profile deletes profiles/<Profile>/world.tar.gz.
	Profile string
	// Key deletes an explicit world key instead: an orphan, or a backup. The
	// profile is the common case and the key is the way out of it.
	Key string
	// Now is the clock for the trash name and the retention sweep.
	Now func() time.Time
}

// DeleteResult is what a delete did, so the CLI can say it and the page can show
// a size in the confirmation before it happens.
type DeleteResult struct {
	Key   string
	Trash string
	Bytes int64
	// Expires is when the trash copy is swept: the answer to "can I still get it
	// back", which is the question anyone deleting a world is asking.
	Expires time.Time
	// Missing is a delete of something already gone. It is a message, not an
	// error: deleting twice should be safe to repeat.
	Missing bool
	// Swept are expired trash entries removed on the way past.
	Swept []string
}

// Target is the key a DeleteOptions names, before any R2 call.
func (o DeleteOptions) Target() (string, error) {
	if o.Key != "" {
		if o.Profile != "" {
			return "", fmt.Errorf("name either a profile or a key, not both")
		}
		if !strings.HasSuffix(o.Key, ".tar.gz") {
			return "", fmt.Errorf("%q is not a world archive (a world key ends in .tar.gz)", o.Key)
		}
		return o.Key, nil
	}
	if err := publish.ValidateProfile(o.Profile); err != nil {
		return "", err
	}
	return publish.ProfilePrefix(o.Profile) + ArchiveName, nil
}

// Delete moves a world to the trash and then removes it from where it was.
//
// The order is the whole rule: copy to trash, verify the copy, and only then
// remove the live key. A failed copy aborts the delete with the world exactly
// where it was — the same promise the shutdown path makes ("never delete a world
// you could not back up"), applied to the one operation that cannot be retried.
//
// The trash expires (TrashRetention), swept lazily on the next delete. Nothing
// else may remove it: it is the answer to "I deleted the wrong world".
func Delete(ctx context.Context, st Store, opts DeleteOptions) (DeleteResult, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	at := now().UTC()

	key, err := opts.Target()
	if err != nil {
		return DeleteResult{}, err
	}
	res := DeleteResult{Key: key}

	// Sweep first, so a delete that fails halfway still leaves the trash tidy and
	// a delete that succeeds does not add to a pile nobody empties.
	swept, err := trash.Sweep(ctx, st, opts.WorldBucket, at)
	if err != nil {
		return res, err
	}
	res.Swept = swept

	src, err := st.Stat(ctx, opts.WorldBucket, key)
	if err != nil {
		if errors.Is(err, r2.ErrNotFound) {
			res.Missing = true
			return res, nil
		}
		return res, err
	}

	res.Trash = trash.Key(key, at)
	res.Expires = trash.Expires(at)
	if _, err := st.Copy(ctx, opts.WorldBucket, key, res.Trash); err != nil {
		// Copy verifies the destination size, so an error here means the trash
		// copy is not whole and the live key must stay.
		return res, fmt.Errorf("nothing was deleted: could not put a copy in %s (%w)", res.Trash, err)
	}

	if err := st.Remove(ctx, opts.WorldBucket, key); err != nil {
		return res, fmt.Errorf("the copy is safe in %s but the live key is still there: %w", res.Trash, err)
	}
	res.Bytes = src.Size
	return res, nil
}
