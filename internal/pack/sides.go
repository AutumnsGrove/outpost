package pack

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
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

// sideCache holds what Modrinth says about each project's sides, for the length of
// one fetch: a pack of a hundred mods is a hundred projects, and asking twice for
// the same one (or once per fetch of the same pack) is pointless load on somebody
// else's API.
var sideCache = struct {
	sync.Mutex
	known map[string]string
}{known: map[string]string{}}

// serverSide is Modrinth's own statement of whether a mod can run on a dedicated
// server: "required", "optional" or "unsupported".
//
// It is the right question to ask, and the other two sources are not. The pack's
// `env.server` is whatever the author's launcher exported — Fabulously Optimized
// marks all fifty of its files `server: required`, including the purely graphical
// ones — and a jar's `fabric.mod.json` `environment` field is the mod's opinion
// about its own code, which is often "*" for something that in practice only works
// on a client (e4mc says "*" and registers a server command that dies on the first
// player's login). Modrinth's per-project sides are curated against the mod's
// actual behaviour, which is the thing the boot cares about.
func serverSide(ctx context.Context, id string) (string, bool) {
	sideCache.Lock()
	side, ok := sideCache.known[id]
	sideCache.Unlock()
	if ok {
		return side, side != ""
	}

	var doc struct {
		ServerSide string `json:"server_side"`
	}
	if err := getJSON(ctx, "/project/"+id, &doc); err != nil {
		// No answer is not "unsupported": a pack we cannot ask about is installed as
		// the pack says, because refusing to fetch on an API hiccup is worse than a
		// mod the loader will ignore.
		sideCache.Lock()
		sideCache.known[id] = ""
		sideCache.Unlock()
		return "", false
	}

	sideCache.Lock()
	sideCache.known[id] = doc.ServerSide
	sideCache.Unlock()
	return doc.ServerSide, true
}

// sidesLookup and projectOfURL are where dropServerUnsupported asks, and seams for
// the tests: a fixture cannot host cdn.modrinth.com, and the questions being tested
// are what happens to an answer and which file it applies to, not where either came
// from. Production uses the two functions below, unchanged.
var (
	sidesLookup  = serverSide
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
// broken".
//
// Dropped files join the skipped list, so the dependency rescue still sees them: a
// "client" mod another kept mod cannot load without is put back (PLAN 40).
func dropServerUnsupported(ctx context.Context, wanted []ManifestFile, res *Result, log *slog.Logger) (kept, dropped []ManifestFile) {
	for _, f := range wanted {
		id := projectOfURL(f.Downloads[0])
		if id == "" {
			kept = append(kept, f)
			continue
		}
		side, known := sidesLookup(ctx, id)
		if known && strings.EqualFold(side, "unsupported") {
			dropped = append(dropped, f)
			res.Skipped = append(res.Skipped, f.Path)
			log.Info("not installing a client-only mod", "file", f.Path, "modrinth", "server_side: unsupported")
			continue
		}
		kept = append(kept, f)
	}
	return kept, dropped
}
