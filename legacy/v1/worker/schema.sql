-- GroveMC D1 Database Schema
-- Apply with: wrangler d1 execute mc-state --file=./schema.sql

-- Server state tracking (singleton row)
CREATE TABLE IF NOT EXISTS server_state (
    id INTEGER PRIMARY KEY DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'OFFLINE',
    vps_id TEXT,
    vps_ip TEXT,
    region TEXT,
    server_type TEXT,
    started_at TEXT,
    last_heartbeat TEXT,
    dns_updated_at TEXT,
    player_count INTEGER DEFAULT 0,
    idle_since TEXT
);

-- Initialize singleton row
INSERT OR IGNORE INTO server_state (id, state) VALUES (1, 'OFFLINE');

-- Session history (for usage stats and billing)
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    duration_seconds INTEGER,
    cost_usd REAL,
    max_players INTEGER DEFAULT 0,
    world_size_bytes INTEGER,
    region TEXT NOT NULL,
    server_type TEXT NOT NULL,
    vps_id TEXT
);

-- Monthly aggregates (for dashboard stats)
CREATE TABLE IF NOT EXISTS monthly_summary (
    month TEXT PRIMARY KEY,  -- Format: "2025-01"
    total_hours REAL DEFAULT 0,
    total_cost REAL DEFAULT 0,
    session_count INTEGER DEFAULT 0,
    eu_hours REAL DEFAULT 0,
    eu_cost REAL DEFAULT 0,
    us_hours REAL DEFAULT 0,
    us_cost REAL DEFAULT 0
);

-- Whitelist cache (source of truth is MC server, this is for UI display)
CREATE TABLE IF NOT EXISTS whitelist_cache (
    username TEXT PRIMARY KEY,
    uuid TEXT,
    added_at TEXT DEFAULT (datetime('now')),
    added_by TEXT
);

-- Backup history
CREATE TABLE IF NOT EXISTS backups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL DEFAULT (datetime('now')),
    size_bytes INTEGER,
    session_id INTEGER,
    triggered_by TEXT DEFAULT 'auto',  -- 'auto', 'manual', 'shutdown'
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at);
CREATE INDEX IF NOT EXISTS idx_sessions_region ON sessions(region);
CREATE INDEX IF NOT EXISTS idx_backups_timestamp ON backups(timestamp);
