package pack

// Choosing a version. A modpack publishes one release per Minecraft version, so
// "Fabulously Optimized" is 481 versions across a dozen game versions, and the
// newest one is almost never the one this server can boot. The panel shows the
// list, says which ones are runnable and why the rest are not, and which one the
// profile already holds — because the alternative is what happened the first
// time: a pack silently installed nine major versions behind its own latest.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"outpost/internal/publish"
)

// How many rows the picker shows. Modrinth keeps every version ever published —
// 481 for Fabulously Optimized — and the interesting ones are not the newest
// forty: FO's newest Fabric build for 1.20.1 sits at index 278, with 278 newer
// versions (26.x, 25.x, …) that this server cannot run. So the list is every
// runnable version, plus the newest of each game version it cannot run, which is
// what makes the ceiling visible without burying the usable rows.
const (
	maxBootable = 40
	maxOther    = 12
)

// VersionChoice is one published version of a pack, as the picker shows it.
type VersionChoice struct {
	ID           string   `json:"id"`
	Number       string   `json:"number"`
	Name         string   `json:"name"`
	Type         string   `json:"type"` // release | beta | alpha
	GameVersions []string `json:"gameVersions"`
	Loaders      []string `json:"loaders"`
	PublishedAt  string   `json:"publishedAt"`
	Size         int64    `json:"size"`

	// Bootable says this server can run it: a Fabric build for the Minecraft
	// version the image's JDK supports.
	Bootable bool `json:"bootable"`
	// Installed says this is the version the profile already holds, which is what
	// makes "the newest is 5.4.1 and you have 5.2.8" sayable.
	Installed bool `json:"installed"`
	// Reason explains a bootable=false, in the same words the refusal uses.
	Reason string `json:"reason,omitempty"`
}

// Versions is a pack's version list, scoped to the profile it would land in.
type Versions struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Profile string `json:"profile"`
	// Installed is the version id the profile holds now, empty when it holds none
	// or holds a hand-rolled pack.
	Installed string          `json:"installed,omitempty"`
	Choices   []VersionChoice `json:"choices"`
	Total     int             `json:"total"`
	// Skipped is how many versions the list leaves out, so the page can say
	// "showing 40 of 481" without pretending the pack has 40 versions.
	Skipped int `json:"skipped"`
	// Newer is the newest bootable version, for "an update is available" without
	// anyone pressing anything.
	Newer string `json:"newer,omitempty"`
}

// ErrNoVersions is returned for a source that names exactly one version: a
// .mrpack link or file has nothing to choose between.
var ErrNoVersions = errors.New("a .mrpack link names one version; there is nothing to choose — paste the Modrinth project instead")

// Versions lists a pack's versions, newest first, with what this server can run.
func (m *Manager) Versions(ctx context.Context, source string) (Versions, error) {
	src, err := parseSource(source)
	if err != nil {
		return Versions{}, err
	}
	if src.kind != sourceProject {
		return Versions{}, ErrNoVersions
	}

	project, err := fetchProject(ctx, src.slug)
	if err != nil {
		return Versions{}, err
	}
	all, err := fetchVersions(ctx, src.slug)
	if err != nil {
		return Versions{}, err
	}

	slug := project.Slug
	if slug == "" {
		slug = src.slug
	}
	// The profile has to come from the same place install will: deriving it from
	// the project's slug here while `profileFor` derives it from the source is how
	// the picker ends up reading a different profile's manifest and reporting the
	// wrong "installed" marker.
	profile, err := m.profileFor(source, "")
	if err != nil {
		return Versions{}, err
	}
	out := Versions{
		Slug:    slug,
		Name:    project.Title,
		Profile: profile,
		Total:   len(all),
	}

	// What the profile already holds, so the picker can mark it and the page can
	// say "an update is available" on load.
	if raw, err := m.Store.Get(ctx, m.AssetBucket, "profiles/"+out.Profile+"/pack.json"); err == nil {
		var held publish.Pack
		if json.Unmarshal(raw, &held) == nil {
			out.Installed = held.VersionID
		}
	}

	// One pass over everything first: `Newer` and the "is this the newest runnable
	// one" question are about the whole list, and answering them from a truncated
	// one is how the picker ends up offering an older version as an update.
	seenInstalled := false
	otherGameVersions := map[string]bool{}
	for _, v := range all {
		run, _ := bootable(v)
		installed := v.ID != "" && v.ID == out.Installed
		if installed {
			seenInstalled = true
		}
		if run && !installed && !seenInstalled && out.Newer == "" {
			// Newest-first order, so the first runnable version before the installed
			// one is the update. Anything after it is a downgrade.
			out.Newer = v.VersionNumber
		}

		key := strings.Join(v.GameVersions, "/")
		include := run || (!otherGameVersions[key] && !installed)
		if !include || (run && countBootable(out.Choices) >= maxBootable) || (!run && len(otherGameVersions) >= maxOther) {
			out.Skipped++
			continue
		}
		if !run {
			otherGameVersions[key] = true
		}

		choice := VersionChoice{
			ID:           v.ID,
			Number:       v.VersionNumber,
			Name:         v.Name,
			Type:         v.VersionType,
			GameVersions: v.GameVersions,
			Loaders:      v.Loaders,
			PublishedAt:  v.PublishedAt,
			Installed:    installed,
		}
		if file, err := v.Mrpack(); err == nil {
			choice.Size = file.Size
		}
		choice.Bootable, choice.Reason = bootable(v)
		out.Choices = append(out.Choices, choice)
	}
	return out, nil
}

func countBootable(choices []VersionChoice) int {
	n := 0
	for _, c := range choices {
		if c.Bootable {
			n++
		}
	}
	return n
}

// bootable answers "can this server run this version", in the same terms
// publish.CheckPack and pack.pickVersion use.
func bootable(v Version) (bool, string) {
	if !contains(v.Loaders, "fabric") {
		if len(v.Loaders) == 0 {
			return false, "the pack names no loader for this version"
		}
		return false, "no Fabric build (" + strings.Join(v.Loaders, ", ") + ")"
	}
	for _, mc := range v.GameVersions {
		if strings.EqualFold(mc, publish.SupportedMC) {
			return true, ""
		}
	}
	if len(v.GameVersions) == 0 {
		return false, "the pack names no Minecraft version"
	}
	return false, fmt.Sprintf("minecraft %s — this server installs Java 17, which stops at %s",
		strings.Join(v.GameVersions, ", "), publish.SupportedMC)
}
