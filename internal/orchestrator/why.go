package orchestrator

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"outpost/internal/store"
)

// The daemon's whole view of a broken server is "RCON did not answer", and that is
// the same observation for a mod crash, a missing JDK, a full disk, a Fabric
// mod-resolution failure and a typo in cloud-init. Everything that actually says
// what happened is in a log on a box that gets deleted minutes later.
//
// So when a server stops answering, ask it once. The log is reachable: the daemon
// already speaks SSH to this VPS to save the world.

const (
	// whyAfterFailures is how many consecutive missed probes mean "this is not a
	// hiccup". Two ticks is forty seconds — long enough that a GC pause or a save
	// stall is not reported as a crash, short enough to be before the terminal
	// failure count.
	whyAfterFailures = 3

	// whyLines is how much log is asked for, and whyKeep how much of the distilled
	// wreckage is stored. The event trail is a phone screen, not a log viewer.
	whyLines = 120
	whyKeep  = 1500
)

// loadBearing is the line that answers "why" by itself: an exception class, a mod
// resolution failure, a refusal. The first of these is the summary, because
// "Encountered an unexpected exception" is a headline and the next line is the news.
var loadBearing = regexp.MustCompile(`(?i)` +
	`(java\.[a-z0-9_.]*(exception|error):|nosuchmethod|noclassdef|unsupported class file|` +
	`mod resolution failed|incompatible mods found|requires version|no candidate|` +
	`out of memory|no installation candidate|no space left|caused by:)`)

// interesting is the part of a Minecraft/Fabric log that answers "why". Everything
// else — the mod list, the recipe loading, the "Preparing spawn area" progress — is
// noise at this moment.
var interesting = regexp.MustCompile(`(?i)` +
	`(exception|error|fatal|crash|incompatible|requires version|no candidate|` +
	`nosuchmethod|noclassdef|unsupported class file|could not|failed to|` +
	`out of memory|no installation candidate|permission denied|no space left|` +
	`^\s*at (knot|net\.minecraft|java\.)|caused by:|mod resolution)`)

// whyCommand is what to run on the VPS. One round trip: the tail of the log, and
// the head of the newest crash report when Minecraft wrote one. `exit 0` because a
// missing log or a missing crash-reports directory is not an SSH failure.
var whyCommand = fmt.Sprintf(`tail -n %[1]d /opt/minecraft/logs/latest.log 2>/dev/null; `+
	`f=$(ls -t /opt/minecraft/crash-reports/*.txt 2>/dev/null | head -1); `+
	`[ -n "$f" ] && { echo "=== crash report: $(basename "$f") ==="; head -n 40 "$f"; }; `+
	`exit 0`, whyLines)

// reportWhy records what a dying server is complaining about, once per VPS.
//
// Once, because the caller is a loop: a crash loop would otherwise write the same
// forty lines every twenty seconds, and the event trail exists to be readable.
func (o *Orchestrator) reportWhy(ctx context.Context, st store.ServerState, why string) {
	if o.deps.Remote == nil || st.VPSID == "" {
		return
	}
	if seen, _ := o.store.MetaGet("why_reported"); seen == st.VPSID {
		return
	}
	_ = o.store.MetaSet("why_reported", st.VPSID)

	out, err := o.deps.Remote.Run(ctx, o.dialHost(ctx, st), whyCommand)
	if err != nil {
		o.log.Warn("could not read the server's log", "host", st.TailnetHost, "err", err)
		_ = o.store.Log("why", "%s; the log could not be read over SSH: %v", why, err)
		return
	}

	summary, detail := distill(out)
	if summary == "" {
		// A log with nothing interesting in it is still an answer: the server did
		// not fail, it is not there yet.
		_ = o.store.Log("why", "%s; the server's log says nothing about a failure", why)
		return
	}

	o.log.Error("the server says why", "host", st.TailnetHost, "why", summary)
	_ = o.store.Log("crash", "%s: %s\n%s", why, summary, detail)

	st.LastError = truncate(why+": "+summary, 240)
	_ = o.store.SetState(st)
}

// distill turns a log tail into one line and a short excerpt.
//
// The first interesting line is usually the load-bearing one ("Mod resolution
// failed", "Incompatible mods found!", "java.lang.NoSuchMethodError: ..."), and the
// excerpt keeps the stack frames that name a mod so the culprit is visible without
// a second SSH session.
func distill(raw string) (summary, detail string) {
	var kept []string
	summary = ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !interesting.MatchString(line) {
			continue
		}
		// Strip the "[20:26:49] [main/ERROR]: " prefix Minecraft puts in front of
		// everything: it is the same on every line and the message is the point.
		if i := strings.Index(trimmed, "]: "); strings.HasPrefix(trimmed, "[") && i > 0 && i < 60 {
			trimmed = strings.TrimSpace(trimmed[i+3:])
		}
		if summary == "" && loadBearing.MatchString(trimmed) {
			summary = trimmed
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == 0 {
		return "", ""
	}
	if summary == "" {
		// Nothing stood out: the first thing the log complained about is the best
		// available answer.
		summary = kept[0]
	}
	detail = truncate(strings.Join(kept, "\n"), whyKeep)
	return summary, detail
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
