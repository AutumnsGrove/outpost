// Package store is Outpost's memory: one singleton server_state row, a session
// ledger, a backup log, and an event trail.
//
// All timestamps are stored as RFC3339 UTC strings. Empty string means "never".
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// State is the lifecycle position of the one server Outpost manages.
type State string

const (
	Offline      State = "OFFLINE"
	Provisioning State = "PROVISIONING"
	Running      State = "RUNNING"
	Idle         State = "IDLE"
	Terminating  State = "TERMINATING"
	Error        State = "ERROR"
)

// ServerState is the singleton row.
type ServerState struct {
	State        State
	VPSID        string
	VPSIP        string
	TailnetHost  string
	Region       string
	ServerType   string
	RCONPassword string

	StartedAt time.Time
	IdleSince time.Time

	// Profile is the pack+world pair this session booted, recorded at rent time so
	// the shutdown upload lands in the same place the boot pulled from even if the
	// active profile is switched an hour later.
	Profile string

	Players   int
	DNSValue  string
	LastError string
	UpdatedAt time.Time
}

// Session is one billed stretch of server.
type Session struct {
	ID              int64
	StartedAt       time.Time
	EndedAt         time.Time
	DurationSeconds int64
	CostUSD         float64
	MaxPlayers      int
	Region          string
	ServerType      string
	// HourlyRate is what this session was billed at, in EUR per hour, captured at
	// rent time. Zero for a row written before the column existed.
	HourlyRate float64
	VPSID      string
}

// Backup is one recorded world upload.
type Backup struct {
	ID        int64
	At        time.Time
	SizeBytes int64
	SessionID int64
	Kind      string
}

// Event is one line of the audit trail.
type Event struct {
	ID      int64
	At      time.Time
	Kind    string
	Message string
}

// Store wraps the SQLite handle.
type Store struct {
	path string
	db   *sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One writer, no surprises. SQLite plus a single tick loop does not need more.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{path: path, db: db}, nil
}

// addedColumns are columns that arrived after the first real session.
//
// schema.sql is written as though a database is being created from nothing, and
// CREATE TABLE IF NOT EXISTS does nothing to the outpost.db that already holds
// sessions on the Potato. So the two are kept in step here instead: SQLite's
// ADD COLUMN with a non-null default is instant and rewrites no rows.
var addedColumns = []struct{ table, column, ddl string }{
	{"server_state", "profile", `ALTER TABLE server_state ADD COLUMN profile TEXT NOT NULL DEFAULT ''`},
	{"sessions", "hourly_rate", `ALTER TABLE sessions ADD COLUMN hourly_rate REAL NOT NULL DEFAULT 0`},
}

func migrate(db *sql.DB) error {
	have := map[string]map[string]bool{}
	for _, c := range addedColumns {
		if have[c.table] != nil {
			continue
		}
		columns, err := tableColumns(db, c.table)
		if err != nil {
			return err
		}
		have[c.table] = columns
	}
	for _, c := range addedColumns {
		if have[c.table][c.column] {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return fmt.Errorf("add column %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", table, err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// TryLock takes an exclusive, non-blocking lock over the state machine.
//
// It is a file lock, not a mutex, because more than one process can drive the
// machine: `outpost serve`, `outpost start`, `outpost stop`, and `outpost tick`
// from cron. They share nothing but this database. `flock` is per open file
// description, so a second Open in the same process is excluded too.
//
// Non-blocking on purpose: a caller that cannot get the lock should skip its
// pass and let the holder finish, never queue behind a fifteen-minute SSH
// timeout.
func (s *Store) TryLock() (release func(), ok bool, err error) {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false, nil
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// Lock is TryLock that waits, for the callers that must not be dropped.
//
// A skipped tick is harmless — the next one is twenty seconds away — but a
// skipped Stop is a button press that did nothing, and a Stop that runs
// unlocked can be overwritten by a tick that read the row before it landed
// (every tick writes back the whole row from its own snapshot). Those callers
// wait their turn instead, bounded by ctx so a wedged holder cannot hang a CLI.
func (s *Store) Lock(ctx context.Context) (release func(), err error) {
	for {
		release, ok, err := s.TryLock()
		if err != nil {
			return nil, err
		}
		if ok {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("another outpost process is busy: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseStamp(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v)
}

// GetState returns the singleton state row.
func (s *Store) GetState() (ServerState, error) {
	var st ServerState
	var started, idle, updated string

	err := s.db.QueryRow(`
		SELECT state, vps_id, vps_ip, tailnet_host, region, server_type,
		       rcon_password, started_at, idle_since, players, dns_value,
		       last_error, updated_at, profile
		  FROM server_state WHERE id = 1`).
		Scan(&st.State, &st.VPSID, &st.VPSIP, &st.TailnetHost, &st.Region,
			&st.ServerType, &st.RCONPassword, &started, &idle, &st.Players,
			&st.DNSValue, &st.LastError, &updated, &st.Profile)
	if err != nil {
		return st, fmt.Errorf("read state: %w", err)
	}

	if st.StartedAt, err = parseStamp(started); err != nil {
		return st, fmt.Errorf("parse started_at: %w", err)
	}
	if st.IdleSince, err = parseStamp(idle); err != nil {
		return st, fmt.Errorf("parse idle_since: %w", err)
	}
	if st.UpdatedAt, err = parseStamp(updated); err != nil {
		return st, fmt.Errorf("parse updated_at: %w", err)
	}
	return st, nil
}

// SetState writes the whole row back.
func (s *Store) SetState(st ServerState) error {
	st.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE server_state SET
			state = ?, vps_id = ?, vps_ip = ?, tailnet_host = ?, region = ?,
			server_type = ?, rcon_password = ?, started_at = ?, idle_since = ?,
			players = ?, dns_value = ?, last_error = ?, updated_at = ?,
			profile = ?
		 WHERE id = 1`,
		string(st.State), st.VPSID, st.VPSIP, st.TailnetHost, st.Region,
		st.ServerType, st.RCONPassword, stamp(st.StartedAt), stamp(st.IdleSince),
		st.Players, st.DNSValue, st.LastError, stamp(st.UpdatedAt), st.Profile)
	if err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// Reset puts the state row back to a clean OFFLINE.
func (s *Store) Reset() error {
	return s.SetState(ServerState{State: Offline})
}

// StartSession opens a session ledger row and returns its id.
func (s *Store) StartSession(startedAt time.Time, region, serverType string, hourlyRate float64, vpsID string) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO sessions (started_at, region, server_type, hourly_rate, vps_id)
		VALUES (?, ?, ?, ?, ?)`,
		stamp(startedAt), region, serverType, hourlyRate, vpsID)
	if err != nil {
		return 0, fmt.Errorf("start session: %w", err)
	}
	return res.LastInsertId()
}

// EndSession closes a session row with its duration, cost and peak players.
func (s *Store) EndSession(id int64, durationSeconds int64, costUSD float64, maxPlayers int) error {
	_, err := s.db.Exec(`
		UPDATE sessions
		   SET ended_at = ?, duration_seconds = ?, cost_usd = ?, max_players = ?
		 WHERE id = ?`,
		stamp(time.Now().UTC()), durationSeconds, costUSD, maxPlayers, id)
	if err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	return nil
}

// CurrentSession returns the most recent session, still open or not.
func (s *Store) CurrentSession() (*Session, error) {
	var sess Session
	var started, ended string

	err := s.db.QueryRow(`
		SELECT id, started_at, ended_at, duration_seconds, cost_usd, max_players,
		       region, server_type, hourly_rate, vps_id
		  FROM sessions ORDER BY id DESC LIMIT 1`).
		Scan(&sess.ID, &started, &ended, &sess.DurationSeconds, &sess.CostUSD,
			&sess.MaxPlayers, &sess.Region, &sess.ServerType, &sess.HourlyRate, &sess.VPSID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	if sess.StartedAt, err = parseStamp(started); err != nil {
		return nil, err
	}
	if sess.EndedAt, err = parseStamp(ended); err != nil {
		return nil, err
	}
	return &sess, nil
}

// RecentSessions returns the newest n sessions, newest first.
func (s *Store) RecentSessions(n int) ([]Session, error) {
	rows, err := s.db.Query(`
		SELECT id, started_at, ended_at, duration_seconds, cost_usd, max_players,
		       region, server_type, hourly_rate, vps_id
		  FROM sessions ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var sess Session
		var started, ended string
		if err := rows.Scan(&sess.ID, &started, &ended, &sess.DurationSeconds, &sess.CostUSD,
			&sess.MaxPlayers, &sess.Region, &sess.ServerType, &sess.HourlyRate, &sess.VPSID); err != nil {
			return nil, err
		}
		if sess.StartedAt, err = parseStamp(started); err != nil {
			return nil, err
		}
		if sess.EndedAt, err = parseStamp(ended); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// UpdateSessionPlayers raises the session's peak player count if n is higher.
func (s *Store) UpdateSessionPlayers(id int64, n int) error {
	_, err := s.db.Exec(`UPDATE sessions SET max_players = ? WHERE id = ? AND max_players < ?`, n, id, n)
	if err != nil {
		return fmt.Errorf("update session players: %w", err)
	}
	return nil
}

// RecordBackup appends a backup row.
func (s *Store) RecordBackup(sizeBytes, sessionID int64, kind string) error {
	_, err := s.db.Exec(`
		INSERT INTO backups (at, size_bytes, session_id, kind) VALUES (?, ?, ?, ?)`,
		stamp(time.Now().UTC()), sizeBytes, sessionID, kind)
	if err != nil {
		return fmt.Errorf("record backup: %w", err)
	}
	return nil
}

// RecentBackups returns the newest n backups, newest first.
func (s *Store) RecentBackups(n int) ([]Backup, error) {
	rows, err := s.db.Query(`
		SELECT id, at, size_bytes, session_id, kind
		  FROM backups ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()

	var out []Backup
	for rows.Next() {
		var b Backup
		var at string
		if err := rows.Scan(&b.ID, &at, &b.SizeBytes, &b.SessionID, &b.Kind); err != nil {
			return nil, err
		}
		if b.At, err = parseStamp(at); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Totals returns lifetime hours, cost and session count.
func (s *Store) Totals() (hours, cost float64, sessions int, err error) {
	row := s.db.QueryRow(`
		SELECT COALESCE(SUM(duration_seconds), 0) / 3600.0,
		       COALESCE(SUM(cost_usd), 0),
		       COUNT(*)
		  FROM sessions WHERE ended_at != ''`)
	return hours, cost, sessions, row.Scan(&hours, &cost, &sessions)
}

// Log appends an event to the audit trail.
func (s *Store) Log(kind, format string, args ...any) error {
	_, err := s.db.Exec(`INSERT INTO events (at, kind, message) VALUES (?, ?, ?)`,
		stamp(time.Now().UTC()), kind, fmt.Sprintf(format, args...))
	return err
}

// RecentEvents returns the newest n events, newest first.
func (s *Store) RecentEvents(n int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, at, kind, message FROM events ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Kind, &e.Message); err != nil {
			return nil, err
		}
		if e.At, err = parseStamp(at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MetaGet reads a key from the meta table. Missing keys return "".
func (s *Store) MetaGet(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("meta get %s: %w", key, err)
	}
	return v, nil
}

// MetaSet writes a key to the meta table.
func (s *Store) MetaSet(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("meta set %s: %w", key, err)
	}
	return nil
}

// MetaInt reads an integer from the meta table, treating missing as 0.
func (s *Store) MetaInt(key string) (int, error) {
	v, err := s.MetaGet(key)
	if err != nil || v == "" {
		return 0, err
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, nil
	}
	return n, nil
}
