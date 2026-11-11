package store

import (
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "outpost.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStateRoundTrip(t *testing.T) {
	s := open(t)

	st, err := s.GetState()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if st.State != Offline {
		t.Fatalf("fresh database should be OFFLINE, got %s", st.State)
	}

	started := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	want := ServerState{
		State:        Running,
		VPSID:        "42",
		VPSIP:        "203.0.113.10",
		TailnetHost:  "outpost-eu-abc",
		Region:       "eu",
		ServerType:   "cx32",
		RCONPassword: "secret",
		StartedAt:    started,
		Players:      2,
	}
	if err := s.SetState(want); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := s.GetState()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != want.State || got.VPSID != want.VPSID || got.TailnetHost != want.TailnetHost {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if !got.StartedAt.Equal(started) {
		t.Fatalf("started_at = %s, want %s", got.StartedAt, started)
	}
}

func TestSessionsAndTotals(t *testing.T) {
	s := open(t)
	start := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

	id, err := s.StartSession(start, "eu", "cx32", "1")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := s.EndSession(id, 7200, 0.02, 3); err != nil {
		t.Fatalf("end session: %v", err)
	}

	sess, err := s.CurrentSession()
	if err != nil || sess == nil {
		t.Fatalf("current session: %v", err)
	}
	if sess.DurationSeconds != 7200 || sess.MaxPlayers != 3 {
		t.Fatalf("session not saved: %+v", sess)
	}

	hours, cost, count, err := s.Totals()
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if count != 1 || hours != 2 || cost != 0.02 {
		t.Fatalf("totals = %v h, %v usd, %v sessions", hours, cost, count)
	}

	// An open session must not count toward lifetime totals, or the dashboard
	// would bill you for a server you are still using.
	if _, err := s.StartSession(start, "eu", "cx32", "2"); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if _, _, count, _ := s.Totals(); count != 1 {
		t.Fatalf("open sessions counted in totals: %d", count)
	}
}

func TestUpdateSessionPlayersOnlyRaises(t *testing.T) {
	s := open(t)
	id, err := s.StartSession(time.Now().UTC(), "eu", "cx32", "1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := s.UpdateSessionPlayers(id, 3); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.UpdateSessionPlayers(id, 1); err != nil {
		t.Fatalf("update: %v", err)
	}

	sess, _ := s.CurrentSession()
	if sess.MaxPlayers != 3 {
		t.Fatalf("max players = %d, want 3 (should be a high-water mark)", sess.MaxPlayers)
	}
}

func TestMeta(t *testing.T) {
	s := open(t)

	if v, _ := s.MetaGet("nope"); v != "" {
		t.Fatalf("missing key should be empty, got %q", v)
	}
	if err := s.MetaSet("world_saved", "42"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, _ := s.MetaGet("world_saved"); v != "42" {
		t.Fatalf("meta = %q, want 42", v)
	}
	if err := s.MetaSet("world_saved", "43"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if v, _ := s.MetaGet("world_saved"); v != "43" {
		t.Fatalf("meta = %q, want 43", v)
	}

	if n, _ := s.MetaInt("missing"); n != 0 {
		t.Fatalf("missing int = %d", n)
	}
}

func TestEvents(t *testing.T) {
	s := open(t)
	if err := s.Log("start", "rented %s in %s", "outpost-eu-abc", "nbg1"); err != nil {
		t.Fatalf("log: %v", err)
	}

	events, err := s.RecentEvents(10)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "start" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Message != "rented outpost-eu-abc in nbg1" {
		t.Fatalf("message = %q", events[0].Message)
	}
}
