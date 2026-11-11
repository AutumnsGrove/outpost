CREATE TABLE IF NOT EXISTS server_state (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    state         TEXT    NOT NULL DEFAULT 'OFFLINE',
    vps_id        TEXT    NOT NULL DEFAULT '',
    vps_ip        TEXT    NOT NULL DEFAULT '',
    tailnet_host  TEXT    NOT NULL DEFAULT '',
    region        TEXT    NOT NULL DEFAULT '',
    server_type   TEXT    NOT NULL DEFAULT '',
    rcon_password TEXT    NOT NULL DEFAULT '',
    started_at    TEXT    NOT NULL DEFAULT '',
    idle_since    TEXT    NOT NULL DEFAULT '',
    players       INTEGER NOT NULL DEFAULT 0,
    dns_value     TEXT    NOT NULL DEFAULT '',
    last_error    TEXT    NOT NULL DEFAULT '',
    updated_at    TEXT    NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO server_state (id) VALUES (1);

CREATE TABLE IF NOT EXISTS sessions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at       TEXT    NOT NULL,
    ended_at         TEXT    NOT NULL DEFAULT '',
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    cost_usd         REAL    NOT NULL DEFAULT 0,
    max_players      INTEGER NOT NULL DEFAULT 0,
    region           TEXT    NOT NULL DEFAULT '',
    server_type      TEXT    NOT NULL DEFAULT '',
    vps_id           TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS backups (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    at         TEXT    NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    session_id INTEGER NOT NULL DEFAULT 0,
    kind       TEXT    NOT NULL DEFAULT 'shutdown'
);

CREATE TABLE IF NOT EXISTS events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      TEXT    NOT NULL,
    kind    TEXT    NOT NULL,
    message TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at);
CREATE INDEX IF NOT EXISTS idx_events_at ON events(at);
