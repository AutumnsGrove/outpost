package store

import (
	"database/sql"
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

func TestLockStartExcludesASecondProcess(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(filepath.Join(dir, "outpost.db"))
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer first.Close()

	// A second handle on the same file is the same shape as `outpost serve` and
	// an `outpost start` shell racing each other.
	second, err := Open(filepath.Join(dir, "outpost.db"))
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer second.Close()

	release, ok, err := first.TryLock()
	if err != nil || !ok {
		t.Fatalf("first lock: %v ok=%v", err, ok)
	}
	if _, ok, err := second.TryLock(); err != nil || ok {
		t.Fatal("second lock should have been refused while the first is held")
	}

	release()
	releaseAgain, ok, err := second.TryLock()
	if err != nil || !ok {
		t.Fatalf("lock should be free after release: %v ok=%v", err, ok)
	}
	releaseAgain()
}

// TestOpenAddsTheProfileColumnToAnExistingDatabase is the migration that matters:
// the Potato's outpost.db was written before profiles existed, and
// CREATE TABLE IF NOT EXISTS does nothing to a table that is already there.
func TestOpenAddsTheProfileColumnToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE server_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			state TEXT NOT NULL DEFAULT 'OFFLINE',
			vps_id TEXT NOT NULL DEFAULT '', vps_ip TEXT NOT NULL DEFAULT '',
			tailnet_host TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '',
			server_type TEXT NOT NULL DEFAULT '', rcon_password TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT '', idle_since TEXT NOT NULL DEFAULT '',
			players INTEGER NOT NULL DEFAULT 0, dns_value TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO server_state (id, state, vps_id) VALUES (1, 'OFFLINE', '42');
	`); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open old database: %v", err)
	}
	defer s.Close()

	st, err := s.GetState()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if st.VPSID != "42" {
		t.Fatalf("the existing row was lost: %+v", st)
	}
	if st.Profile != "" {
		t.Fatalf("profile = %q, want empty", st.Profile)
	}

	// And it can be written to.
	st.Profile = "homestead"
	if err := s.SetState(st); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, _ := s.GetState(); got.Profile != "homestead" {
		t.Fatalf("profile did not round-trip: %+v", got)
	}
}

func TestAnOlderDatabaseGainsTheNewColumnsWithoutLosingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// The schema as it was before hold_until and game_token existed.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	if _, err := old.Exec(`
		CREATE TABLE server_state (
			id            INTEGER PRIMARY KEY CHECK (id = 1),
			state         TEXT NOT NULL DEFAULT 'OFFLINE',
			vps_id        TEXT NOT NULL DEFAULT '',
			vps_ip        TEXT NOT NULL DEFAULT '',
			tailnet_host  TEXT NOT NULL DEFAULT '',
			region        TEXT NOT NULL DEFAULT '',
			server_type   TEXT NOT NULL DEFAULT '',
			rcon_password TEXT NOT NULL DEFAULT '',
			started_at    TEXT NOT NULL DEFAULT '',
			idle_since    TEXT NOT NULL DEFAULT '',
			players       INTEGER NOT NULL DEFAULT 0,
			dns_value     TEXT NOT NULL DEFAULT '',
			last_error    TEXT NOT NULL DEFAULT '',
			updated_at    TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO server_state (id, state, vps_id) VALUES (1, 'OFFLINE', 'kept');
		CREATE TABLE sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, started_at TEXT NOT NULL,
			ended_at TEXT NOT NULL DEFAULT '', duration_seconds INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0, max_players INTEGER NOT NULL DEFAULT 0,
			region TEXT NOT NULL DEFAULT '', server_type TEXT NOT NULL DEFAULT '',
			vps_id TEXT NOT NULL DEFAULT '');
		INSERT INTO sessions (started_at, ended_at, duration_seconds, cost_usd)
		VALUES ('2026-11-01T00:00:00Z', '2026-11-01T01:00:00Z', 3600, 0.5);
	`); err != nil {
		t.Fatalf("build old schema: %v", err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer s.Close()

	st, err := s.GetState()
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if st.VPSID != "kept" {
		t.Fatal("the old row did not survive the migration")
	}
	if !st.HoldUntil.IsZero() || st.GameToken != "" {
		t.Fatalf("new columns should be empty, got %+v", st)
	}

	// And they are writable, which is the point of adding them.
	until := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	st.HoldUntil = until
	st.GameToken = "tok"
	if err := s.SetState(st); err != nil {
		t.Fatalf("set: %v", err)
	}
	back, err := s.GetState()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !back.HoldUntil.Equal(until) || back.GameToken != "tok" {
		t.Fatalf("round trip lost the new columns: %+v", back)
	}

	// Opening twice is what every CLI invocation does; it must not try to add
	// the columns again and fail.
	s.Close()
	if _, err := Open(path); err != nil {
		t.Fatalf("second open: %v", err)
	}
}

func TestTotalsSinceCountsClosedSessionsFromTheMonthStart(t *testing.T) {
	s := open(t)

	add := func(started time.Time, dur int64, cost float64, ended bool) {
		t.Helper()
		id, err := s.StartSession(started, "eu", "cx23", "1")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if ended {
			if err := s.EndSession(id, dur, cost, 1); err != nil {
				t.Fatalf("end: %v", err)
			}
		}
	}

	add(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), 7200, 0.02, true)
	add(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), 3600, 0.01, true) // last month
	add(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), 0, 0, false)      // still open

	hours, cost, sessions, err := s.TotalsSince(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if hours != 2 || cost != 0.02 || sessions != 1 {
		t.Fatalf("totals since Nov 1 = %v h, %v, %d sessions; want 2, 0.02, 1", hours, cost, sessions)
	}

	// Lifetime is still lifetime.
	if h, _, n, _ := s.Totals(); h != 3 || n != 2 {
		t.Fatalf("lifetime = %v h over %d sessions, want 3 and 2", h, n)
	}
}
