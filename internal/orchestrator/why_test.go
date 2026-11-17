package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

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

	events, err := h.store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	var crash string
	for _, e := range events {
		if e.Kind == "crash" {
			crash = e.Message
		}
	}
	if !strings.Contains(crash, "NoSuchMethodError") || !strings.Contains(crash, "e4mc") {
		t.Fatalf("no crash event naming the cause: %q", crash)
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

	events, err := h.store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	var why string
	for _, e := range events {
		if e.Kind == "crash" {
			why = e.Message
		}
	}
	if !strings.Contains(why, "Mod resolution failed") {
		t.Fatalf("a server that never boots said nothing about why: %q", why)
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
