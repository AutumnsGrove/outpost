/**
 * D1 Database Service
 * Handles all database operations for GroveMC
 */

import type { Env, ServerState, Region } from '../types/env.js';

// ============================================================================
// Server State Operations
// ============================================================================

export interface ServerStateRow {
  id: number;
  state: ServerState;
  vps_id: string | null;
  vps_ip: string | null;
  region: Region | null;
  server_type: string | null;
  started_at: string | null;
  last_heartbeat: string | null;
  dns_updated_at: string | null;
  player_count: number;
  idle_since: string | null;
}

export async function getServerState(db: D1Database): Promise<ServerStateRow> {
  const result = await db
    .prepare('SELECT * FROM server_state WHERE id = 1')
    .first<ServerStateRow>();

  if (!result) {
    throw new Error('Server state row not found - database may not be initialized');
  }

  return result;
}

export async function updateServerState(
  db: D1Database,
  updates: Partial<Omit<ServerStateRow, 'id'>>
): Promise<void> {
  const fields: string[] = [];
  const values: unknown[] = [];

  for (const [key, value] of Object.entries(updates)) {
    fields.push(`${key} = ?`);
    values.push(value);
  }

  if (fields.length === 0) return;

  await db
    .prepare(`UPDATE server_state SET ${fields.join(', ')} WHERE id = 1`)
    .bind(...values)
    .run();
}

export async function setServerState(
  db: D1Database,
  state: ServerState,
  additionalUpdates?: Partial<Omit<ServerStateRow, 'id' | 'state'>>
): Promise<void> {
  await updateServerState(db, { state, ...additionalUpdates });
}

// ============================================================================
// Session Operations
// ============================================================================

export interface SessionRow {
  id: number;
  started_at: string;
  ended_at: string | null;
  duration_seconds: number | null;
  cost_usd: number | null;
  max_players: number;
  world_size_bytes: number | null;
  region: Region;
  server_type: string;
  vps_id: string | null;
}

export async function createSession(
  db: D1Database,
  region: Region,
  serverType: string,
  vpsId: string
): Promise<number> {
  const result = await db
    .prepare(
      `INSERT INTO sessions (started_at, region, server_type, vps_id)
       VALUES (datetime('now'), ?, ?, ?)
       RETURNING id`
    )
    .bind(region, serverType, vpsId)
    .first<{ id: number }>();

  return result?.id ?? 0;
}

export async function endSession(
  db: D1Database,
  sessionId: number,
  durationSeconds: number,
  costUsd: number,
  maxPlayers: number,
  worldSizeBytes?: number
): Promise<void> {
  await db
    .prepare(
      `UPDATE sessions SET
         ended_at = datetime('now'),
         duration_seconds = ?,
         cost_usd = ?,
         max_players = ?,
         world_size_bytes = ?
       WHERE id = ?`
    )
    .bind(durationSeconds, costUsd, maxPlayers, worldSizeBytes ?? null, sessionId)
    .run();
}

export async function getCurrentSession(db: D1Database): Promise<SessionRow | null> {
  return db
    .prepare('SELECT * FROM sessions WHERE ended_at IS NULL ORDER BY id DESC LIMIT 1')
    .first<SessionRow>();
}

export async function getSessionHistory(
  db: D1Database,
  limit = 50,
  offset = 0
): Promise<SessionRow[]> {
  const result = await db
    .prepare(
      `SELECT * FROM sessions
       ORDER BY started_at DESC
       LIMIT ? OFFSET ?`
    )
    .bind(limit, offset)
    .all<SessionRow>();

  return result.results;
}

export async function updateSessionMaxPlayers(
  db: D1Database,
  sessionId: number,
  playerCount: number
): Promise<void> {
  await db
    .prepare(
      `UPDATE sessions SET max_players = MAX(max_players, ?) WHERE id = ?`
    )
    .bind(playerCount, sessionId)
    .run();
}

// ============================================================================
// Monthly Summary Operations
// ============================================================================

export interface MonthlySummaryRow {
  month: string;
  total_hours: number;
  total_cost: number;
  session_count: number;
  eu_hours: number;
  eu_cost: number;
  us_hours: number;
  us_cost: number;
}

export async function getMonthlySummary(
  db: D1Database,
  month: string
): Promise<MonthlySummaryRow | null> {
  return db
    .prepare('SELECT * FROM monthly_summary WHERE month = ?')
    .bind(month)
    .first<MonthlySummaryRow>();
}

export async function updateMonthlySummary(
  db: D1Database,
  month: string,
  region: Region,
  hours: number,
  cost: number
): Promise<void> {
  const euHours = region === 'eu' ? hours : 0;
  const euCost = region === 'eu' ? cost : 0;
  const usHours = region === 'us' ? hours : 0;
  const usCost = region === 'us' ? cost : 0;

  await db
    .prepare(
      `INSERT INTO monthly_summary (month, total_hours, total_cost, session_count, eu_hours, eu_cost, us_hours, us_cost)
       VALUES (?, ?, ?, 1, ?, ?, ?, ?)
       ON CONFLICT(month) DO UPDATE SET
         total_hours = total_hours + excluded.total_hours,
         total_cost = total_cost + excluded.total_cost,
         session_count = session_count + 1,
         eu_hours = eu_hours + excluded.eu_hours,
         eu_cost = eu_cost + excluded.eu_cost,
         us_hours = us_hours + excluded.us_hours,
         us_cost = us_cost + excluded.us_cost`
    )
    .bind(month, hours, cost, euHours, euCost, usHours, usCost)
    .run();
}

export async function getRecentMonthlySummaries(
  db: D1Database,
  count = 12
): Promise<MonthlySummaryRow[]> {
  const result = await db
    .prepare(
      `SELECT * FROM monthly_summary
       ORDER BY month DESC
       LIMIT ?`
    )
    .bind(count)
    .all<MonthlySummaryRow>();

  return result.results;
}

// ============================================================================
// Whitelist Operations
// ============================================================================

export interface WhitelistEntry {
  username: string;
  uuid: string | null;
  added_at: string;
  added_by: string | null;
}

export async function getWhitelist(db: D1Database): Promise<WhitelistEntry[]> {
  const result = await db
    .prepare('SELECT * FROM whitelist_cache ORDER BY added_at DESC')
    .all<WhitelistEntry>();

  return result.results;
}

export async function addToWhitelist(
  db: D1Database,
  username: string,
  uuid?: string,
  addedBy?: string
): Promise<void> {
  await db
    .prepare(
      `INSERT OR REPLACE INTO whitelist_cache (username, uuid, added_at, added_by)
       VALUES (?, ?, datetime('now'), ?)`
    )
    .bind(username, uuid ?? null, addedBy ?? null)
    .run();
}

export async function removeFromWhitelist(
  db: D1Database,
  username: string
): Promise<boolean> {
  const result = await db
    .prepare('DELETE FROM whitelist_cache WHERE username = ?')
    .bind(username)
    .run();

  return (result.meta.changes ?? 0) > 0;
}

export async function syncWhitelistCache(
  db: D1Database,
  whitelist: Array<{ name: string; uuid: string }>
): Promise<void> {
  // Clear existing cache
  await db.prepare('DELETE FROM whitelist_cache').run();

  // Insert new entries
  for (const entry of whitelist) {
    await db
      .prepare(
        `INSERT INTO whitelist_cache (username, uuid, added_at)
         VALUES (?, ?, datetime('now'))`
      )
      .bind(entry.name, entry.uuid)
      .run();
  }
}

// ============================================================================
// Backup Operations
// ============================================================================

export interface BackupRow {
  id: number;
  timestamp: string;
  size_bytes: number | null;
  session_id: number | null;
  triggered_by: string;
}

export async function recordBackup(
  db: D1Database,
  sizeBytes?: number,
  sessionId?: number,
  triggeredBy: 'auto' | 'manual' | 'shutdown' = 'auto'
): Promise<number> {
  const result = await db
    .prepare(
      `INSERT INTO backups (size_bytes, session_id, triggered_by)
       VALUES (?, ?, ?)
       RETURNING id`
    )
    .bind(sizeBytes ?? null, sessionId ?? null, triggeredBy)
    .first<{ id: number }>();

  return result?.id ?? 0;
}

export async function getRecentBackups(
  db: D1Database,
  limit = 10
): Promise<BackupRow[]> {
  const result = await db
    .prepare(
      `SELECT * FROM backups
       ORDER BY timestamp DESC
       LIMIT ?`
    )
    .bind(limit)
    .all<BackupRow>();

  return result.results;
}

export async function getLastBackup(db: D1Database): Promise<BackupRow | null> {
  return db
    .prepare('SELECT * FROM backups ORDER BY timestamp DESC LIMIT 1')
    .first<BackupRow>();
}

// ============================================================================
// Utility Functions
// ============================================================================

export function getCurrentMonth(): string {
  const now = new Date();
  return `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`;
}

export function calculateSessionCost(
  durationSeconds: number,
  region: Region
): number {
  const hourlyRates = {
    eu: 0.0085,
    us: 0.028,
  };

  const hours = durationSeconds / 3600;
  return Number((hours * hourlyRates[region]).toFixed(4));
}
