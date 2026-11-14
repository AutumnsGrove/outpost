package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"outpost/internal/config"
	"outpost/internal/store"
)

// live brings the harness to a RUNNING server with nobody on it, which is the
// state a hold is asked for in.
func (h *harness) live(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.players = 0
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state = %s, want RUNNING", got)
	}
}

func TestHoldKeepsTheServerUpWithNobodyOn(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	ctx := context.Background()

	if _, err := h.o.Hold(ctx, 3*time.Hour, "test"); err != nil {
		t.Fatalf("hold: %v", err)
	}

	// Two hours with nobody on, well past grace + timeout, which would normally
	// be TERMINATING by now.
	h.clock = h.clock.Add(2 * time.Hour)
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state while held = %s, want RUNNING", got)
	}

	held, err := h.o.HoldState()
	if err != nil {
		t.Fatalf("hold state: %v", err)
	}
	if !held.Active || held.Left <= 0 {
		t.Fatalf("hold should still be active, got %+v", held)
	}
}

func TestHoldExpiresIntoTheOrdinaryGraceNotAnInstantDelete(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	ctx := context.Background()

	if _, err := h.o.Hold(ctx, time.Hour, "test"); err != nil {
		t.Fatalf("hold: %v", err)
	}

	// The hold runs out. The very next pass must NOT terminate: the clock starts
	// from zero so the usual grace still applies.
	h.clock = h.clock.Add(time.Hour + time.Minute)
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state just after expiry = %s, want RUNNING", got)
	}
	st, _ := h.store.GetState()
	if !st.HoldUntil.IsZero() {
		t.Fatalf("expired hold should be cleared, got %s", st.HoldUntil)
	}

	// Ten minutes of grace, then IDLE.
	h.clock = h.clock.Add(10 * time.Minute)
	h.tick(t)
	if got := h.mustState(t); got != store.Idle {
		t.Fatalf("state after grace = %s, want IDLE", got)
	}

	// And twenty more ends it, exactly like a normal session.
	h.clock = h.clock.Add(21 * time.Minute)
	h.tick(t)
	if got := h.mustState(t); got != store.Terminating {
		t.Fatalf("state past the limit = %s, want TERMINATING", got)
	}
}

func TestHoldSurvivesADaemonRestart(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	ctx := context.Background()

	if _, err := h.o.Hold(ctx, 12*time.Hour, "test"); err != nil {
		t.Fatalf("hold: %v", err)
	}

	// A new Orchestrator over the same database is what a restart looks like:
	// nothing lives in memory, so the hold has to be in the state row.
	restarted := NewWith(h.o.cfg, h.store, h.o.log, h.o.deps)
	h.o = restarted

	h.clock = h.clock.Add(6 * time.Hour)
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state after restart while held = %s, want RUNNING", got)
	}
	held, _ := h.o.HoldState()
	if !held.Active {
		t.Fatal("the hold did not survive the restart")
	}
}

func TestHoldIsCappedAndOnlyAppliesToALiveServer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Nothing running: there is no server to hold.
	if _, err := h.o.Hold(ctx, time.Hour, "test"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("hold while offline = %v, want ErrNotRunning", err)
	}

	h.live(t)
	h.o.cfg.Server.MaxHold = config.Duration(72 * time.Hour)
	if _, err := h.o.Hold(ctx, 73*time.Hour, "test"); !errors.Is(err, ErrHoldTooLong) {
		t.Fatalf("hold past the cap = %v, want ErrHoldTooLong", err)
	}
	st, _ := h.store.GetState()
	if !st.HoldUntil.IsZero() {
		t.Fatal("a refused hold must not be recorded")
	}
}

func TestCancelHoldStartsTheClockFromZero(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	ctx := context.Background()

	// Five minutes into the idle clock, then a hold.
	h.clock = h.clock.Add(5 * time.Minute)
	h.tick(t)
	if _, err := h.o.Hold(ctx, time.Hour, "test"); err != nil {
		t.Fatalf("hold: %v", err)
	}

	// An hour held, then dropped: the grace starts now, not where it was.
	h.clock = h.clock.Add(time.Hour)
	if _, err := h.o.CancelHold(ctx, "test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state after cancel = %s, want RUNNING", got)
	}
	st, _ := h.store.GetState()
	if !st.HoldUntil.IsZero() {
		t.Fatal("hold not cleared")
	}
	if st.IdleSince.IsZero() {
		t.Fatal("cancel should restart the idle clock, not leave it paused")
	}

	if _, err := h.o.CancelHold(ctx, "test"); !errors.Is(err, ErrNoHold) {
		t.Fatalf("cancel with no hold = %v, want ErrNoHold", err)
	}
}

func TestEveryHoldLeavesAReceipt(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	ctx := context.Background()

	if _, err := h.o.Hold(ctx, time.Hour, "game:AutumnsAdventure"); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := h.o.CancelHold(ctx, "game:AutumnsAdventure"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	events, err := h.store.RecentEvents(20)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	seen := map[string]string{}
	for _, e := range events {
		seen[e.Kind] = e.Message
	}
	for _, kind := range []string{"hold", "hold-cancelled"} {
		if _, ok := seen[kind]; !ok {
			t.Errorf("no %q event in the audit trail", kind)
		}
	}
}

func TestCostCountsTheMonthAndTheLiveSession(t *testing.T) {
	h := newHarness(t)
	h.live(t)
	ctx := context.Background()

	// A closed session earlier this month: 2h at 10/h = 20.
	if _, err := h.store.StartSession(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), "eu", "cx32", "old"); err != nil {
		t.Fatalf("start session: %v", err)
	}
	sess, _ := h.store.CurrentSession()
	if err := h.store.EndSession(sess.ID, 7200, 20, 1); err != nil {
		t.Fatalf("end session: %v", err)
	}

	// And one last month, which must not be in this month's total.
	if _, err := h.store.StartSession(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), "eu", "cx32", "older"); err != nil {
		t.Fatalf("start session: %v", err)
	}
	sess, _ = h.store.CurrentSession()
	if err := h.store.EndSession(sess.ID, 3600, 10, 1); err != nil {
		t.Fatalf("end session: %v", err)
	}

	h.clock = h.clock.Add(30 * time.Minute)
	c, err := h.o.Cost(ctx)
	if err != nil {
		t.Fatalf("cost: %v", err)
	}
	if !c.Live {
		t.Fatal("the running session should count as live")
	}
	if got, want := c.SessionHours, 0.5; got != want {
		t.Errorf("session hours = %v, want %v", got, want)
	}
	if got, want := c.SessionCost, 5.0; got != want {
		t.Errorf("session cost = %v, want %v", got, want)
	}
	// 2h closed + 0.5h live = 2.5h; 20 + 5 = 25.
	if got, want := c.MonthHours, 2.5; got != want {
		t.Errorf("month hours = %v, want %v", got, want)
	}
	if got, want := c.MonthCost, 25.0; got != want {
		t.Errorf("month cost = %v, want %v", got, want)
	}
	if got, want := c.MonthSessions, 2; got != want {
		t.Errorf("month sessions = %v, want %v", got, want)
	}
}

func TestShortDurationReadsLikeTheCommandItAccepts(t *testing.T) {
	cases := map[time.Duration]string{
		48 * time.Hour:               "48h",
		90 * time.Minute:             "1h30m",
		2*time.Hour + 30*time.Minute: "2h30m",
		0:                            "0m",
		-time.Hour:                   "0m",
	}
	for in, want := range cases {
		if got := ShortDuration(in); got != want {
			t.Errorf("ShortDuration(%v) = %q, want %q", in, got, want)
		}
	}
}
