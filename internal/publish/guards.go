package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"outpost/internal/config"
	"outpost/internal/hetzner"
)

// The world keys. publish reads both and writes the marker; the VPS writes the
// world itself with rclone at shutdown, which is why the marker sits next to it
// rather than inside it.
const (
	worldKey      = "current/world.tar.gz"
	packMarkerKey = "current/pack.json"
)

// checkPackMemory refuses a pack that no configured region can hold, and warns
// about the regions that cannot while others can.
//
// A fallback type does not save a region on its own: Hetzner only answers
// resource_unavailable when a plan is sold out, so a plan that exists but is too
// small still gets rented and the pack runs out of memory on its first save.
// That is why the message names the plan to switch to.
func checkPackMemory(p Pack, regions map[string]config.Region) (warnings []string, err error) {
	if p.MemoryGB <= 0 || len(regions) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(regions))
	for name := range regions {
		names = append(names, name)
	}
	sort.Strings(names)

	var tooSmall []string
	fits := false
	for _, name := range names {
		region := regions[name]
		gb, known := hetzner.PlanRAM(region.ServerType)
		if !known {
			// A plan this table does not have is not a refusal. Saying nothing is
			// better than refusing a rental over a stale list.
			continue
		}
		if gb >= p.MemoryGB {
			fits = true
			continue
		}

		summary := fmt.Sprintf("%s: %s has %d GB", name, region.ServerType, gb)
		var bigger []string
		for _, t := range region.FallbackTypes {
			if fg, ok := hetzner.PlanRAM(t); ok && fg >= p.MemoryGB && t != region.ServerType {
				bigger = append(bigger, fmt.Sprintf("%s (%d GB)", t, fg))
			}
		}
		if len(bigger) > 0 {
			summary += ", set server_type to " + strings.Join(bigger, " or ")
		}
		tooSmall = append(tooSmall, summary)
	}

	if len(tooSmall) == 0 {
		return nil, nil
	}
	if fits {
		return []string{fmt.Sprintf("%s asks for %d GB; %s — that region will not fit the pack",
			p.Describe(), p.MemoryGB, strings.Join(tooSmall, "; "))}, nil
	}
	return nil, fmt.Errorf("publish: %s asks for %d GB and no configured region has a plan that big (%s); "+
		"raise server_type, add a region that has one, or pass a lower --memory to `outpost pack fetch` if that figure is wrong",
		p.Describe(), p.MemoryGB, strings.Join(tooSmall, "; "))
}

// worldGuard answers the one question that decides whether a session loads the
// world it was built for: does the world in R2 belong to this pack?
//
// It only reads. The mutations (writing the marker, discarding a world) happen
// in Run after the assets are up, so a failed publish cannot leave a marker
// claiming a pack that never reached the bucket.
//
// discard reports that the caller asked for a fresh world and there is an old
// one to remove.
func worldGuard(ctx context.Context, st Store, opts Options, p Pack) (discard bool, warnings []string, err error) {
	if opts.WorldBucket == "" {
		return false, nil, nil
	}

	if _, err := st.Stat(ctx, opts.WorldBucket, worldKey); err != nil {
		if errors.Is(err, ErrNotFound) {
			// No world at all: nothing can be broken by this pack.
			return false, nil, nil
		}
		return false, nil, err
	}

	raw, err := st.Get(ctx, opts.WorldBucket, packMarkerKey)
	switch {
	case errors.Is(err, ErrNotFound):
		// A world saved before packs had a marker, or by a hand-rolled pack.
		switch {
		case opts.NewWorld:
			return true, nil, nil
		case opts.KeepWorld:
			return false, []string{"keeping a world with no pack marker: it may predate packs, so pack identity could not be checked"}, nil
		}
		return false, nil, fmt.Errorf("publish: %s has a saved world with no pack marker, so this pack cannot be checked against it; "+
			"pass --new-world to discard it and start fresh, or --keep-world to load it anyway", opts.WorldBucket)
	case err != nil:
		return false, nil, err
	}

	var current Pack
	if err := json.Unmarshal(raw, &current); err != nil {
		return false, nil, fmt.Errorf("publish: %s/%s is unreadable (%v); pass --new-world or --keep-world to decide what to do with the world", opts.WorldBucket, packMarkerKey, err)
	}
	if current.SameBuild(p) {
		return false, nil, nil
	}

	msg := fmt.Sprintf("publish: the saved world was made by %s and this is %s", current.Describe(), p.Describe())
	switch {
	case opts.NewWorld:
		return true, nil, nil
	case opts.KeepWorld:
		return false, []string{msg + ": loading it anyway because of --keep-world (missing mods and block ids read as corrupted chunks)"}, nil
	}
	return false, nil, fmt.Errorf("%s; pass --new-world to start a fresh world, or --keep-world to load it anyway", msg)
}

// recordPack writes the marker after a successful publish. Without it the next
// publish cannot tell whether the world belongs to the pack it is about to
// upload.
func recordPack(ctx context.Context, st Store, bucket string, p Pack, discard bool) error {
	if discard {
		if err := st.Remove(ctx, bucket, worldKey); err != nil {
			return fmt.Errorf("discard world: %w", err)
		}
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	obj := textObject(packMarkerKey, string(raw))
	sum, err := sum(obj)
	if err != nil {
		return err
	}
	return put(ctx, st, bucket, obj, sum)
}
