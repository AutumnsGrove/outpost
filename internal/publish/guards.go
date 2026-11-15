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

// worldKeys are one profile's world and its pack marker. publish reads both and
// writes the marker; the VPS writes the world itself with rclone at shutdown,
// which is why the marker sits next to it rather than inside it.
//
// Scoped by profile because the world is: two profiles are two games, and the
// question "was this world made by this pack" is now only ever asked inside one
// profile that both belong to.
func worldKeys(profile string) (world, marker string) {
	base := profilePrefix(profile)
	return base + "world.tar.gz", base + "pack.json"
}

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

	var (
		tooSmall  []string
		unchecked []string
		fits      bool
	)
	for _, name := range names {
		region := regions[name]
		gb, known := hetzner.PlanRAM(region.ServerType)
		if !known {
			// A plan this table does not have is not a refusal, and it is not
			// silence either: the table is a snapshot, Hetzner renames plans, and a
			// region that might fit must not be counted as one that does not.
			unchecked = append(unchecked, fmt.Sprintf("%s: %s is not a plan this knows", name, region.ServerType))
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

	if len(tooSmall) == 0 && len(unchecked) == 0 {
		return nil, nil
	}

	// A configured region that is too small is no longer the ceiling: the page's
	// picker rents any plan in the table, so the honest question is whether any
	// rentable plan holds the pack. The region's own list is what will be tried
	// first, which is worth saying out loud rather than refusing over.
	if !fits && len(unchecked) == 0 {
		if best, ok := hetzner.SmallestFit(p.MemoryGB); ok {
			tooSmall = append(tooSmall, fmt.Sprintf("%s can hold it (%d GB, EUR %.4f/h) — pick it from the page before starting",
				best.Type, best.RAMGB, best.HourlyEUR))
			fits = true
		}
	}

	// Refuse only when every region was checked and no plan this table has fits.
	// A plan the table does not know leaves a region unproven, and an unproven
	// region is rented, not blocked.
	if !fits && len(unchecked) == 0 {
		return nil, fmt.Errorf("publish: %s asks for %d GB and no rentable plan has that much RAM (%s); "+
			"pass a lower --memory to `outpost pack fetch` if that figure is wrong",
			p.Describe(), p.MemoryGB, strings.Join(tooSmall, "; "))
	}

	notes := tooSmall
	notes = append(notes, unchecked...)
	return []string{fmt.Sprintf("%s asks for %d GB; %s",
		p.Describe(), p.MemoryGB, strings.Join(notes, "; "))}, nil
}

// worldGuard answers the one question that decides whether a session loads the
// world it was built for: does the world in R2 belong to this pack?
//
// It only reads. The mutations (writing the marker, discarding a world) happen
// in Run after the assets are up, so a failed publish cannot leave a marker
// claiming a pack that never reached the bucket.
//
// discard reports that the caller asked for a fresh world and there is an old
// one to remove; current is the pack the saved world was made by, when the marker
// says so, which is what the page shows before someone commits to a download.
func worldGuard(ctx context.Context, st Store, opts Options, p Pack) (discard bool, current *Pack, warnings []string, err error) {
	if opts.WorldBucket == "" {
		return false, nil, nil, nil
	}
	worldKey, packMarkerKey := worldKeys(opts.ProfileOf())

	if _, err := st.Stat(ctx, opts.WorldBucket, worldKey); err != nil {
		if errors.Is(err, ErrNotFound) {
			// No world at all: nothing can be broken by this pack.
			return false, nil, nil, nil
		}
		return false, nil, nil, err
	}

	// What the world was made by, if R2 says so. A missing marker and an
	// unreadable one are the same answer here — "no idea" — because --new-world
	// has to work in both cases, and a corrupt marker is not something a person
	// can repair without a shell.
	var saved Pack
	known := false
	raw, err := st.Get(ctx, opts.WorldBucket, packMarkerKey)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &saved); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s/%s is unreadable (%v), so this pack cannot be checked against the world", opts.WorldBucket, packMarkerKey, err))
		} else if opts.NewWorld {
			// Checked first on purpose: "start a fresh world" has to mean that even
			// when the world belongs to the pack being published, because a world of
			// this build can still be the one you want to throw away.
			return true, &saved, nil, nil
		} else if saved.SamePack(p) {
			return false, &saved, nil, nil
		} else {
			known = true
		}
	case errors.Is(err, ErrNotFound):
		// A world saved before packs had a marker, or by a hand-rolled pack.
	default:
		return false, nil, nil, err
	}

	if !known {
		switch {
		case opts.NewWorld:
			return true, nil, warnings, nil
		case opts.KeepWorld:
			return false, nil, append(warnings, "keeping a world whose pack could not be identified: it may predate packs, or the marker may be corrupt"), nil
		}
		return false, nil, warnings, fmt.Errorf("publish: %s has a saved world with no readable pack marker, so this pack cannot be checked against it; "+
			"pass --new-world to discard it and start fresh, or --keep-world to load it anyway", opts.ProfileOf())
	}

	msg := fmt.Sprintf("publish: profile %q holds a world made by %s and this is %s", opts.ProfileOf(), saved.Describe(), p.Describe())
	switch {
	case opts.NewWorld:
		return true, &saved, nil, nil
	case opts.KeepWorld:
		return false, &saved, []string{msg + ": loading it anyway because of --keep-world (missing mods and block ids read as corrupted chunks)"}, nil
	}
	return false, &saved, nil, fmt.Errorf("%s; pass --new-world to start a fresh world, or --keep-world to load it anyway", msg)
}

// recordPack writes the marker after a successful publish. Without it the next
// publish cannot tell whether the world belongs to the pack it is about to
// upload.
func recordPack(ctx context.Context, st Store, opts Options, p Pack, discard bool) error {
	if discard {
		worldKey, _ := worldKeys(opts.ProfileOf())
		if err := st.Remove(ctx, opts.WorldBucket, worldKey); err != nil {
			return fmt.Errorf("discard world: %w", err)
		}
	}
	return WriteMarker(ctx, st, opts.WorldBucket, opts.ProfileOf(), p)
}

// WriteMarker writes the pack marker that sits beside a world: what made the
// world a session will find next to it. `publish` writes one after an upload and
// `outpost world import` writes one after adopting a world, and both have to
// produce the same bytes for the guard to read them the same way.
func WriteMarker(ctx context.Context, st Store, worldBucket, profile string, p Pack) error {
	_, markerKey := worldKeys(profile)
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	obj := textObject(markerKey, string(raw))
	sum, err := sum(obj)
	if err != nil {
		return err
	}
	return put(ctx, st, worldBucket, obj, sum)
}
