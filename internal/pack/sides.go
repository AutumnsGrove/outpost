package pack

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

// modrinthCDN matches the project id inside a Modrinth file URL:
// https://cdn.modrinth.com/data/<project>/versions/<version>/<file>
var modrinthCDN = regexp.MustCompile(`^https?://cdn\.modrinth\.com/data/([A-Za-z0-9]+)/`)

// projectID is the Modrinth project a download URL points at, if it is one. A file
// hosted anywhere else — a mod author's GitHub release, a pack's own mirror — has
// no project to ask about, and gets no opinion.
func projectID(downloadURL string) string {
	if m := modrinthCDN.FindStringSubmatch(downloadURL); m != nil {
		return m[1]
	}
	return ""
}

// maxSideBatch is how many project ids one request may carry. Modrinth's batch
// endpoint takes a thousand; five hundred is a pack and a half, and a pack that
// large is already a different problem.
const maxSideBatch = 500

// serverSides asks Modrinth which sides a batch of projects can run on: "required",
// "optional" or "unsupported", by project id.
//
// It is one request for the whole pack, because the alternative is a request per
// mod: Fabulously Optimized is a hundred files, the API allows about three hundred
// requests a minute, and a preview somebody asked for on a phone should not be a
// burst. A project the API does not answer about is absent from the map, which is
// read as "no opinion" rather than "unsupported".
//
// What this is worth, exactly: `server_side` is author-declared — the same class of
// evidence as a pack's `env.server`, not a curated fact. It is right more often only
// because it is written once per project by the person who wrote the mod, instead of
// being exported per pack by whoever assembled one. It is not an oracle. Where it
// and the code disagree and it is wrong (e4mc is `required` and dies on the first
// login), nothing in the metadata helps: the dependency rescue and the daemon's
// crash report are the answers.
func serverSides(ctx context.Context, ids []string) map[string]string {
	sides := map[string]string{}
	for start := 0; start < len(ids); start += maxSideBatch {
		end := min(start+maxSideBatch, len(ids))
		raw, err := json.Marshal(ids[start:end])
		if err != nil {
			return sides
		}
		var docs []struct {
			ID         string `json:"id"`
			ServerSide string `json:"server_side"`
		}
		if err := getJSON(ctx, "/projects?ids="+url.QueryEscape(string(raw)), &docs); err != nil {
			// No answer is not "unsupported": a pack we cannot ask about is installed
			// as the pack says, because refusing to fetch on an API hiccup is worse
			// than a mod the loader will ignore.
			return sides
		}
		for _, d := range docs {
			if d.ServerSide != "" {
				sides[d.ID] = d.ServerSide
			}
		}
	}
	return sides
}

// sidesOf and projectOfURL are seams for the tests: a fixture cannot host
// cdn.modrinth.com, so it cannot produce a URL that names a project or an API answer
// about one. Production uses the two functions above unchanged.
var (
	sidesOf      = serverSides
	projectOfURL = projectID
)

// dropServerUnsupported removes the mods Modrinth says cannot run on a server.
//
// This is the general form of a bug that has now cost two sessions. PLAN 24 trusts
// a pack's own side annotations, and 24's corollary installs everything when a pack
// marks nothing; both put client mods on a dedicated server on the reasoning that
// Fabric Loader disables them. It does — for the ones that declare themselves
// client-only. The ones that do not are the problem: at best they are dead weight
// in a 2 GB heap, and at worst they crash the server at the moment a player logs
// in, which the player experiences as being kicked and which reads as "Outpost is
// broken". Fabulously Optimized marks all fifty of its files server-required, so
// its own annotations cannot answer this; asking each project can, and does: 36 of
// those fifty are `server_side: unsupported`.
//
// Dropped files join the skipped list, so the dependency rescue still sees them: a
// "client" mod another kept mod cannot load without is put back (PLAN 40).
func dropServerUnsupported(ctx context.Context, wanted []ManifestFile, res *Result, log *slog.Logger) (kept, dropped []ManifestFile) {
	ids := make([]string, 0, len(wanted))
	seen := map[string]bool{}
	for _, f := range wanted {
		if id := projectOfURL(f.Downloads[0]); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sides := sidesOf(ctx, ids)

	for _, f := range wanted {
		id := projectOfURL(f.Downloads[0])
		if id != "" && strings.EqualFold(sides[id], "unsupported") {
			dropped = append(dropped, f)
			res.Skipped = append(res.Skipped, f.Path)
			log.Info("not installing a client-only mod", "file", f.Path, "modrinth", "server_side: unsupported")
			continue
		}
		kept = append(kept, f)
	}
	return kept, dropped
}
