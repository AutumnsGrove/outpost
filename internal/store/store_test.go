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

	id, err := s.StartSession(start, "eu", "cx32", 0.0096, "1")
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
	if _, err := s.StartSession(start, "eu", "cx32", 0.0096, "2"); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if _, _, count, _ := s.Totals(); count != 1 {
		t.Fatalf("open sessions counted in totals: %d", count)
	}
}

func TestUpdateSessionPlayersOnlyRaises(t *testing.T) {
	s := open(t)
	id, err := s.StartSession(time.Now().UTC(), "eu", "cx32", 0.0096, "1")
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

func TestOpenAddsTheHourlyRateColumnToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A sessions table from before the rate was stored with the session.
	if _, err := db.Exec(`
		CREATE TABLE sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at TEXT NOT NULL,
			ended_at TEXT NOT NULL DEFAULT '',
			duration_seconds INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0,
			max_players INTEGER NOT NULL DEFAULT 0,
			region TEXT NOT NULL DEFAULT '',
			server_type TEXT NOT NULL DEFAULT '',
			vps_id TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO sessions (started_at, region, server_type, vps_id, ended_at, cost_usd)
		VALUES ('2026-11-01T10:00:00Z', 'eu', 'cx23', '7', '2026-11-01T11:00:00Z', 0.0096);
	`); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open old database: %v", err)
	}
	defer s.Close()

	sess, err := s.CurrentSession()
	if err != nil || sess == nil {
		t.Fatalf("session: %v", err)
	}
	// An old row keeps its recorded cost and reports an unknown rate rather than a
	// wrong one; the caller falls back to the plan table.
	if sess.HourlyRate != 0 {
		t.Fatalf("hourly rate = %v, want the zero an old row has to report", sess.HourlyRate)
	}
	if sess.CostUSD != 0.0096 {
		t.Fatalf("cost = %v", sess.CostUSD)
	}

	// And new rows carry it.
	if _, err := s.StartSession(time.Now().UTC(), "eu", "cx33", 0.0144, "8"); err != nil {
		t.Fatalf("start session: %v", err)
	}
	latest, err := s.CurrentSession()
	if err != nil {
		t.Fatal(err)
	}
	if latest.HourlyRate != 0.0144 {
		t.Fatalf("new row rate = %v", latest.HourlyRate)
	}
}
