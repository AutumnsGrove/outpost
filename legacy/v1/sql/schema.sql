-- GroveMC D1 Database Schema
-- Run: wrangler d1 execute mc-state --file=./src/sql/schema.sql

-- Server state tracking
CREATE TABLE IF NOT EXISTS server_state (
    id INTEGER PRIMARY KEY DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'OFFLINE',
    vps_id TEXT,
    vps_ip TEXT,
    region TEXT,              -- 'eu' or 'us'
    started_at TEXT,
    last_heartbeat TEXT,
    dns_updated_at TEXT
);

-- Initialize with default row
INSERT OR IGNORE INTO server_state (id, state) VALUES (1, 'OFFLINE');

-- Session history (for usage stats widget)
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    duration_seconds INTEGER,
    cost_usd REAL,
    max_players INTEGER,
    world_size_bytes INTEGER,
    region TEXT,           -- 'eu' or 'us'
    server_type TEXT       -- 'cx33' or 'cpx31'
);

-- Monthly aggregates
CREATE TABLE IF NOT EXISTS monthly_summary (
    month TEXT PRIMARY KEY,  -- "2025-01"
    total_hours REAL,
    total_cost REAL,
    session_count INTEGER
);

-- Whitelist cache (source of truth is server, this is for UI)
CREATE TABLE IF NOT EXISTS whitelist_cache (
    username TEXT PRIMARY KEY,
    uuid TEXT,
    added_at TEXT,
    added_by TEXT
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at);
CREATE INDEX IF NOT EXISTS idx_sessions_region ON sessions(region);
