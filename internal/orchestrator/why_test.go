package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"outpost/internal/store"
)

// The log that cost a session: a client mod registering a server command against
// an API that no longer exists, on the first player's login.
const e4mcLog = `[22:02:34] [Server thread/INFO]: AutumnsAdventure[/172.56.67.100:62228] logged in with entity id 15
[22:02:34] [Server thread/ERROR]: Encountered an unexpected exception
java.lang.NoSuchMethodError: 'boolean net.minecraft.commands.CommandSourceStack.hasPermission(int)'
	at knot//link.e4mc.E4mcClient.lambda$registerCommands$0(E4mcClient.java:46)
	at knot//com.mojang.brigadier.tree.CommandNode.canUse(CommandNode.java:65)
	at net.minecraft.server.players.PlayerList.sendPlayerPermissionLevel(PlayerList.java:495)
[22:02:34] [Server thread/INFO]: Stopping server
`

// live rents a server and gets it to RUNNING, so a test can then break it.
func (h *harness) live(t *testing.T) {
	t.Helper()
	if _, err := h.o.Start(context.Background(), "eu", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.probeErr = nil
	h.tick(t) // provisioned and answering: RUNNING
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state = %s, want RUNNING to break", got)
	}
}

// eventsOfKind returns the messages of a kind, newest first.
func eventsOfKind(t *testing.T, h *harness, kind string) []string {
	t.Helper()
	events, err := h.store.RecentEvents(50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e.Message)
		}
	}
	return out
}

func TestDistillFindsTheExceptionAndTheModThatCausedIt(t *testing.T) {
	summary, detail := distill(e4mcLog)
	if !strings.Contains(summary, "NoSuchMethodError") {
		t.Fatalf("summary = %q, want the exception", summary)
	}
	if !strings.Contains(detail, "e4mc") {
		t.Fatalf("detail should name the mod:\n%s", detail)
	}
	if strings.Contains(detail, "logged in with entity id") {
		t.Fatal("an INFO line about logging in is not why the server died")
	}
}

func TestDistillSaysNothingWhenTheLogSaysNothing(t *testing.T) {
	summary, detail := distill("[22:00:00] [main/INFO]: Preparing spawn area: 12%\n")
	if summary != "" || detail != "" {
		t.Fatalf("invented a reason: %q / %q", summary, detail)
	}
}

// A live server that stops answering gets asked once, and the answer lands where
// somebody is already looking: the event trail and the state row's error.
func TestACrashedServerSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	h.probeErr = errors.New("connection refused")
	h.remote.out = e4mcLog

	for i := 0; i < whyAfterFailures; i++ {
		h.tick(t)
	}

	reported := eventsOfKind(t, h, "why")
	if len(reported) == 0 {
		t.Fatal("a server that stopped answering said nothing")
	}
	// A live miss is a warning, not a death: the terminal transition says crash.
	if msg := reported[0]; !strings.Contains(msg, "NoSuchMethodError") || !strings.Contains(msg, "e4mc") {
		t.Fatalf("the report does not name the cause: %q", msg)
	}
	if crashes := eventsOfKind(t, h, "crash"); len(crashes) != 0 {
		t.Fatalf("three missed probes logged a crash: %q", crashes[0])
	}

	st, err := h.store.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.LastError, "NoSuchMethodError") {
		t.Fatalf("lastError = %q, want the exception", st.LastError)
	}

	// Asking is once per VPS: the caller is a loop and a crash loop must not write
	// the same forty lines every twenty seconds.
	before := 0
	for _, c := range h.remote.calls {
		if strings.Contains(c, "latest.log") {
			before++
		}
	}
	h.tick(t)
	h.tick(t)
	after := 0
	for _, c := range h.remote.calls {
		if strings.Contains(c, "latest.log") {
			after++
		}
	}
	if after != before {
		t.Fatalf("the log was asked for %d more times", after-before)
	}
	if before != 1 {
		t.Fatalf("asked %d times, want once", before)
	}
}

// A server that never comes up is the same silence for fifteen minutes, which is
// what a pack that cannot load looked like.
func TestAServerThatNeverBootsSaysWhy(t *testing.T) {
	h := newHarness(t)
	if _, err := h.o.Start(context.Background(), "eu", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.probeErr = errors.New("never comes up")
	h.remote.out = "Mod resolution failed\nImmediate reason: [HARD_DEP_NO_CANDIDATE chipped 3.0.7 {depends athena}]\n"

	for i := 0; i < 6; i++ {
		h.tick(t)
	}

	reported := eventsOfKind(t, h, "why")
	if len(reported) == 0 {
		t.Fatal("a server that never boots said nothing about why")
	}
	if !strings.Contains(reported[0], "Mod resolution failed") {
		t.Fatalf("the report does not name the cause: %q", reported[0])
	}
	if got := h.mustState(t); got != store.Provisioning {
		t.Fatalf("state = %s; reporting is not giving up", got)
	}
}

// And a server that cannot be asked still says something, rather than going quiet.
func TestUnreadableLogIsReportedAsSuch(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	h.probeErr = errors.New("connection refused")
	h.remote.err = errors.New("dial tcp: connection refused")

	for i := 0; i < whyAfterFailures; i++ {
		h.tick(t)
	}
	events, err := h.store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	var why string
	for _, e := range events {
		if e.Kind == "why" {
			why = e.Message
		}
	}
	if !strings.Contains(why, "could not be read") {
		t.Fatalf("silence is not an answer: %q", why)
	}
}

// A session that never booted ends in a timeout, and the *next* one is a different
// VPS with its own reason to give: the misses counter used to carry over, so the
// second session of the night climbed past the threshold without ever asking.
func TestASecondSessionThatNeverBootsStillSaysWhy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.remote.out = "Mod resolution failed\nImmediate reason: [HARD_DEP_NO_CANDIDATE chipped 3.0.7]\n"
	h.probeErr = errors.New("never comes up")

	logReads := func() int {
		n := 0
		for _, c := range h.remote.calls {
			if strings.Contains(c, "latest.log") {
				n++
			}
		}
		return n
	}

	// Either kind counts as "it said something": provisioning reports as `why` and
	// only the timeout says `crash`.
	reports := func() int { return len(eventsOfKind(t, h, "why")) + len(eventsOfKind(t, h, "crash")) }

	if _, err := h.o.Start(ctx, "eu", ""); err != nil {
		t.Fatalf("first start: %v", err)
	}
	for i := 0; i < 6; i++ {
		h.tick(t)
	}
	if got := reports(); got != 1 {
		t.Fatalf("first session reported %d times, want 1", got)
	}

	// Let it run past the provisioning timeout, which is also a reason to report —
	// and the reason has to survive into lastError, not be overwritten by the
	// symptom.
	h.clock = h.clock.Add(16 * time.Minute)
	h.tick(t)
	st, err := h.store.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.LastError, "Mod resolution failed") {
		t.Fatalf("the timeout threw away the reason: lastError = %q", st.LastError)
	}
	h.tick(t) // tear down
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state = %s, want OFFLINE", got)
	}

	// A new rent, a new box, and a different failure: it gets asked again, for its
	// own reason, and not on the first tick — when cloud-init has not written a log
	// yet and an early empty answer would suppress the question that matters.
	if _, err := h.o.Start(ctx, "eu", ""); err != nil {
		t.Fatalf("second start: %v", err)
	}
	h.remote.out = "Incompatible mods found!\n - Mod 'Chipped' requires athena\n"
	readsBefore := logReads()
	h.tick(t)
	if got := logReads(); got != readsBefore {
		t.Fatalf("asked for the log on the first missed probe")
	}
	for i := 0; i < 5; i++ {
		h.tick(t)
	}
	if got := reports(); got < 2 {
		t.Fatalf("the second session never asked why: %d report(s)", got)
	}
	events, err := h.store.RecentEvents(50)
	if err != nil {
		t.Fatal(err)
	}
	var latest string
	var latestID int64
	for _, e := range events {
		if (e.Kind == "crash" || e.Kind == "why") && e.ID > latestID {
			latest, latestID = e.Message, e.ID
		}
	}
	if !strings.Contains(latest, "Incompatible mods found") {
		t.Fatalf("the second session reported the first box's reason: %q", latest)
	}
}

// And a box whose log says nothing must not inherit the previous box's summary.
func TestANewSecondsReasonIsNotTheLastBoxes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.probeErr = errors.New("never comes up")
	h.remote.out = "java.lang.IllegalStateException: first box problem\n"

	if _, err := h.o.Start(ctx, "eu", ""); err != nil {
		t.Fatalf("first start: %v", err)
	}
	for i := 0; i < 6; i++ {
		h.tick(t)
	}
	h.clock = h.clock.Add(16 * time.Minute)
	h.tick(t) // times out, reporting the first box's problem
	h.tick(t) // tears down

	// Second box: the log cannot be read at all.
	h.remote.out = ""
	h.remote.err = errors.New("ssh: no route to host")
	if _, err := h.o.Start(ctx, "eu", ""); err != nil {
		t.Fatalf("second start: %v", err)
	}
	for i := 0; i < 6; i++ {
		h.tick(t)
	}
	h.clock = h.clock.Add(16 * time.Minute)
	h.tick(t)

	st, err := h.store.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.LastError, "first box problem") {
		t.Fatalf("the second box inherited the first one's cause: %q", st.LastError)
	}
	if !strings.Contains(st.LastError, "timed out") {
		t.Fatalf("lastError = %q, want the timeout", st.LastError)
	}
}

// One unreadable log does not spend the session's question: SSH can be down and the
// log can be unwritten, and both fix themselves.
func TestATransientSSHFailureDoesNotSpendTheQuestion(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	h.probeErr = errors.New("connection refused")
	h.remote.err = errors.New("ssh: no route to host")

	for i := 0; i < whyAfterFailures; i++ {
		h.tick(t)
	}

	// Now the box can answer.
	h.remote.err = nil
	h.remote.out = "java.lang.NoSuchMethodError: 'boolean CommandSourceStack.hasPermission(int)'\n"
	h.tick(t)

	reported := eventsOfKind(t, h, "why")
	if len(reported) == 0 || !strings.Contains(reported[0], "NoSuchMethodError") {
		t.Fatalf("a transient SSH failure was the last word: %v", reported)
	}
}

// The most likely early ask: the log exists and is quiet, because the failure has
// not happened yet. `tail` of a quiet log exits 0, so this is not an SSH error, and
// it used to spend the question — the athena shape exactly.
func TestAQuietLogIsNotTheLastWord(t *testing.T) {
	h := newHarness(t)
	if _, err := h.o.Start(context.Background(), "eu", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.probeErr = errors.New("never comes up")

	// First ask: a log with nothing wrong in it yet.
	h.remote.out = "[21:51:55] [main/INFO]: Preparing spawn area: 12%\n"
	for i := 0; i < 6; i++ {
		h.tick(t)
	}

	// The pack then fails to resolve.
	h.remote.out = "Mod resolution failed\nImmediate reason: [HARD_DEP_NO_CANDIDATE chipped 3.0.7]\n"
	for i := 0; i < 3; i++ {
		h.tick(t)
	}

	reported := eventsOfKind(t, h, "why")
	if len(reported) == 0 || !strings.Contains(reported[0], "Mod resolution failed") {
		t.Fatalf("a quiet log spent the question: %v", reported)
	}
}

// The Vanilla Perfected boot: a 47-mod pack taking three minutes, so the provisioning
// ask lands mid-load where the log is full of warnings. Nothing there is a crash, and
// the semver warning must not be the headline when an error exists.
const slowBootLog = `[01:49:02] [main/WARN]: Mod dev_isxander_sdl uses the version release-3.4.14-7 which isn't compatible with Loader's extended semantic version format (Could not parse version number component 'release'!)
[01:49:04] [main/WARN]: Reference map 'modernfix.refmap.json' for modernfix.mixins.json could not be read. If this is a development environment you can ignore this message
[01:49:30] [main/INFO]: Crash Assistant is client only mod. Mod will do nothing!
[01:51:00] [main/INFO]: Preparing spawn area: 41%
`

func TestASlowBootIsNotACrash(t *testing.T) {
	h := newHarness(t)
	if _, err := h.o.Start(context.Background(), "eu", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.remote.out = slowBootLog

	// Two minutes of missed probes: the ask fires while the pack is still loading.
	h.probeErr = errors.New("never comes up")
	for i := 0; i < 6; i++ {
		h.tick(t)
	}
	if crashes := eventsOfKind(t, h, "crash"); len(crashes) != 0 {
		t.Fatalf("a slow boot was filed as a crash: %q", crashes[0])
	}

	// And it comes up, which is the whole point: the report was a warning, not a wake-up.
	h.probeErr = nil
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state = %s, want RUNNING", got)
	}
	if crashes := eventsOfKind(t, h, "crash"); len(crashes) != 0 {
		t.Fatalf("a healthy boot left a crash behind: %q", crashes[0])
	}
}

// An exception outranks the ERROR headline, which outranks a warning: "Encountered an
// unexpected exception" is a headline and the line under it is the news.
func TestDistillRanksSeverity(t *testing.T) {
	summary, _ := distill(`[22:02:34] [main/ERROR]: Encountered an unexpected exception
java.lang.NoSuchMethodError: 'boolean net.minecraft.commands.CommandSourceStack.hasPermission(int)'
	at knot//link.e4mc.E4mcClient.lambda$registerCommands$0(E4mcClient.java:46)
`)
	if !strings.Contains(summary, "NoSuchMethodError") {
		t.Fatalf("summary = %q, want the exception, not the headline", summary)
	}

	// Warnings only: the semver noise must not be the summary when a real failure is
	// in the same log.
	summary, _ = distill(`[01:49:02] [main/WARN]: Mod x uses the version release-3.4.14-7 which isn't compatible with Loader's extended semantic version format
[01:49:40] [main/ERROR]: Failed to start the minecraft server
`)
	if !strings.Contains(summary, "Failed to start") {
		t.Fatalf("summary = %q, want the error over the warning", summary)
	}
}

// Mixin's most common line starts with the word "Error" and is a warning. It must not
// outrank the real error below it — the same "a warning is the headline" failure, one
// level down.
func TestAWarningThatStartsWithErrorIsStillAWarning(t *testing.T) {
	summary, _ := distill(`[20:30:42] [main/WARN]: Error loading class: dev/ftb/mods/ftbchunks/client/gui/LargeMapScreen (java.lang.ClassNotFoundException: dev/ftb/mods/ftbchunks/client/gui/LargeMapScreen)
[20:31:10] [main/ERROR]: Incompatible mods found!
net.fabricmc.loader.impl.FormattedException: Some of your mods are incompatible with the game or each other!
`)
	if !strings.Contains(summary, "FormattedException") && !strings.Contains(summary, "Incompatible mods found") {
		t.Fatalf("summary = %q, want the error over the mixin warning", summary)
	}
	if strings.Contains(summary, "LargeMapScreen") {
		t.Fatalf("summary = %q, a warning beat the error", summary)
	}
}

// A live session that dies ends at rcon-dead, which is the terminal path and so the one
// that may call it a crash — the most common real death should not render as a `why`.
func TestALiveDeathIsACrash(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	h.probeErr = errors.New("connection refused")
	h.remote.out = "java.lang.OutOfMemoryError: Java heap space\n"

	for i := 0; i < 20; i++ {
		h.tick(t)
		if got := h.mustState(t); got != store.Running {
			break
		}
	}

	crashes := eventsOfKind(t, h, "crash")
	if len(crashes) == 0 {
		t.Fatal("a server that died logged no crash")
	}
	if !strings.Contains(crashes[0], "OutOfMemoryError") {
		t.Fatalf("the crash does not say why: %q", crashes[0])
	}
}

// And the timeout keeps a cause the box already gave, even if the second read fails.
func TestTheTimeoutKeepsAnAnswerTheBoxAlreadyGave(t *testing.T) {
	h := newHarness(t)
	if _, err := h.o.Start(context.Background(), "eu", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.probeErr = errors.New("never comes up")

	// The slow-boot ask finds a real error.
	h.remote.out = "[01:51:00] [main/ERROR]: Failed to start the minecraft server\n"
	for i := 0; i < 6; i++ {
		h.tick(t)
	}

	// The timeout's own read fails.
	h.remote.err = errors.New("ssh: no route to host")
	h.clock = h.clock.Add(16 * time.Minute)
	h.tick(t)

	st, err := h.store.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.LastError, "Failed to start") {
		t.Fatalf("the timeout threw away a cause we already had: %q", st.LastError)
	}
}

// The leak review found: a cause found while booting must not be blamed for a death
// hours later, on the same box. Nothing about a boot warning is true of an OOM kill.
func TestABootCauseIsNotBlamedForALaterDeath(t *testing.T) {
	h := newHarness(t)
	if _, err := h.o.Start(context.Background(), "eu", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.probeErr = errors.New("never comes up")
	h.remote.out = slowBootLog
	for i := 0; i < 6; i++ {
		h.tick(t) // records the boot-time cause
	}

	// It comes up after all and somebody is playing, so the idle clock does not end
	// the session underneath the test.
	h.probeErr = nil
	h.players = 1
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state = %s, want RUNNING", got)
	}
	h.clock = h.clock.Add(2 * time.Hour)
	h.tick(t)

	// Then it dies, and the log cannot be read.
	h.probeErr = errors.New("connection refused")
	h.remote.err = errors.New("ssh: no route to host")
	for i := 0; i < 20; i++ {
		h.tick(t)
		if h.mustState(t) != store.Running {
			break
		}
	}

	st, err := h.store.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.LastError, "dev_isxander_sdl") || strings.Contains(st.LastError, "semantic version") {
		t.Fatalf("a boot warning was blamed for a death: %q", st.LastError)
	}
	// And a death is a crash even when the log could not be fetched.
	if crashes := eventsOfKind(t, h, "crash"); len(crashes) == 0 {
		t.Fatal("a death with an unreadable log logged no crash")
	}
}
