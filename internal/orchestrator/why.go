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

	// whyTries is how many times one VPS is asked at most, per phase of its life.
	// Three, because the ask can land before the box can answer — SSH not up, the log
	// not written yet — and neither may be the last word on a session. Bounded,
	// because the caller is a loop.
	whyTries = 3

	// whyLines is how much log is asked for, and whyKeep how much of the distilled
	// wreckage is stored. The event trail is a phone screen, not a log viewer.
	whyLines = 120
	whyKeep  = 1500
)

// logLine is one line of a Minecraft log, split into the part that says how bad it
// is and the part that says what happened.
var logLine = regexp.MustCompile(`^\[(?:[^\]]*)\]\s*\[[^\]]*/([A-Z]+)\]:?\s*(.*)$`)

// exceptionLine is an exception's own line: "java.lang.NoSuchMethodError: …". It
// outranks even an ERROR, because Minecraft's error line is a headline ("Encountered
// an unexpected exception") and this is the news underneath it.
var exceptionLine = regexp.MustCompile(`^([A-Za-z_$][\w$]*\.)*[A-Za-z0-9_$]*(Exception|Error)\b`)

// severity ranks a line: an exception outranks an ERROR, which outranks any number of
// warnings, and a line with no recognisable level sits between the two — it might be
// a stack frame of the error above it, or it might be noise.
func severity(line string) (rank int) {
	if exceptionLine.MatchString(withoutLevel(line)) {
		return 4
	}
	if m := logLine.FindStringSubmatch(line); m != nil {
		switch strings.ToUpper(m[1]) {
		case "FATAL", "ERROR":
			return 3
		case "WARN", "WARNING":
			return 1
		default:
			return 0
		}
	}
	switch {
	case loadBearing.MatchString(line):
		return 2
	default:
		return 0
	}
}

// withoutLevel strips the "[12:34:56] [main/ERROR]: " prefix: it is the same on every
// line, and the message is the point.
func withoutLevel(line string) string {
	if m := logLine.FindStringSubmatch(line); m != nil && m[2] != "" {
		return strings.TrimSpace(m[2])
	}
	if i := strings.Index(line, "]: "); strings.HasPrefix(line, "[") && i > 0 && i < 60 {
		return strings.TrimSpace(line[i+3:])
	}
	return line
}

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
// It returns the one-line reason, so a caller that is about to write its own
// lastError can keep both ("provisioning timed out after 15m0s: Mod resolution
// failed") instead of overwriting the answer with the symptom.
func (o *Orchestrator) reportWhy(ctx context.Context, st store.ServerState, why string, crash bool) string {
	// "crash" is reserved for the terminal path — the timeout, the dead-RCON
	// transition — because a server that is merely slow boots twenty seconds after a
	// slow-boot report and the panel should not have said it died. Everything earlier
	// is a `why`: what the log is saying while we wait.
	kind := "why"
	if crash {
		kind = "crash"
	}
	if o.deps.Remote == nil || st.VPSID == "" {
		return ""
	}
	// Once per VPS, keyed on the id: a new rent is a new box and deserves its own
	// question. A counter would drift across sessions — the misses from the last
	// one carry into the next and the threshold is never hit at the same value again.
	// Asked once per box *per phase*: a box that was slow to boot and then crashed is
	// two different questions, and the answer to the first says nothing about the
	// second. Keying on the state also keeps the provisioning budget from being spent
	// by the time the server is up.
	key := st.VPSID + "@" + string(st.State) + "@" + kind
	if seen, _ := o.store.MetaGet("why_reported"); seen == key {
		// Already asked on this box. Answer from what was learned then, so a caller
		// that reports a later symptom (the timeout) can still carry the cause.
		summary, _ := o.store.MetaGet("why_summary")
		if summary == "" {
			return ""
		}
		return summary
	}

	// A failed attempt does not spend the question. Asking can land before the box
	// can answer — SSH still coming up, the log not written — and one of those is
	// not a reason to go silent for the rest of the session.
	// Only an answer spends the question, and only three attempts are made anyway.
	// Asking can land before the box can answer: SSH still coming up, or — the case
	// that matters most — a log that exists but has nothing in it yet, because the
	// failure has not happened. A `tail` of a quiet log exits 0, so that is not an
	// SSH error and used to mark the box as reported.
	tries, _ := o.store.MetaInt("why_tries")
	if forKey, _ := o.store.MetaGet("why_tries_for"); forKey != key {
		tries = 0
		_ = o.store.MetaSet("why_tries_for", key)
	}
	if tries >= whyTries {
		return ""
	}
	_ = o.store.MetaSet("why_tries", fmt.Sprint(tries+1))

	out, err := o.deps.Remote.Run(ctx, o.dialHost(ctx, st), whyCommand)
	if err != nil {
		o.log.Warn("could not read the server's log", "host", st.TailnetHost, "err", err)
		_ = o.store.Log("why", "%s; the log could not be read over SSH: %v", why, err)
		return ""
	}
	summary, detail := distill(out)
	if summary == "" {
		// A log with nothing interesting in it is not an answer, it is a log taken too
		// early: say so, and leave the question for the next attempt.
		_ = o.store.Log("why", "%s; the server's log says nothing about a failure yet", why)
		return ""
	}
	_ = o.store.MetaSet("why_reported", key)

	if crash {
		o.log.Error("the server says why", "host", st.TailnetHost, "why", summary)
	} else {
		o.log.Warn("the server's log says", "host", st.TailnetHost, "why", summary)
	}
	_ = o.store.Log(kind, "%s: %s\n%s", why, summary, detail)
	_ = o.store.MetaSet("why_summary", summary)
	return summary
}

// distill turns a log tail into one line and a short excerpt.
//
// The first interesting line is usually the load-bearing one ("Mod resolution
// failed", "Incompatible mods found!", "java.lang.NoSuchMethodError: ..."), and the
// excerpt keeps the stack frames that name a mod so the culprit is visible without
// a second SSH session.
func distill(raw string) (summary, detail string) {
	var kept []string
	best, bestRank := "", 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !interesting.MatchString(line) {
			continue
		}
		rank := severity(line)
		trimmed = withoutLevel(trimmed)
		// The summary is the most severe line, not the first one that matched a word:
		// a benign "Mod x isn't compatible with Loader's extended semantic version
		// format" warning must not outrank the ERROR with the stack trace in it, and a
		// warning is not a reason to report a crash at all.
		if rank > bestRank && (rank >= 3 || (rank == 2 && loadBearing.MatchString(trimmed))) {
			best, bestRank = trimmed, rank
		}
		kept = append(kept, trimmed)
	}
	summary = best
	if len(kept) == 0 {
		return "", ""
	}
	if summary == "" {
		// Nothing stood out: the first thing the log complained about is the best
		// available answer, and the caller decides whether it is worth an event.
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
