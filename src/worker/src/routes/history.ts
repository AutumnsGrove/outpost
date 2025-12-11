/**
 * History Route
 * GET /api/mc/history - Get session history and stats
 */

import type { Env } from '../types/env.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';
import {
  getSessionHistory,
  getRecentMonthlySummaries,
  getRecentBackups,
  getCurrentMonth,
} from '../services/database.js';

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface HistoryQuery {
  limit?: number;
  offset?: number;
  months?: number;
}

/**
 * GET /api/mc/history
 * Get session history, monthly summaries, and backup history
 */
export async function handleHistory(
  request: Request,
  env: Env
): Promise<Response> {
  // Verify admin auth
  const auth = await verifyAdminAuth(request, env);
  if (!auth.authenticated) {
    return authErrorResponse(auth);
  }

  try {
    // Parse query params
    const url = new URL(request.url);
    const limit = Math.min(parseInt(url.searchParams.get('limit') || '50'), 100);
    const offset = parseInt(url.searchParams.get('offset') || '0');
    const months = Math.min(parseInt(url.searchParams.get('months') || '12'), 24);

    // Fetch data
    const [sessions, monthlySummaries, backups] = await Promise.all([
      getSessionHistory(env.DB, limit, offset),
      getRecentMonthlySummaries(env.DB, months),
      getRecentBackups(env.DB, 20),
    ]);

    // Calculate totals
    const allTimeTotals = monthlySummaries.reduce(
      (acc, month) => ({
        hours: acc.hours + month.total_hours,
        cost: acc.cost + month.total_cost,
        sessions: acc.sessions + month.session_count,
      }),
      { hours: 0, cost: 0, sessions: 0 }
    );

    // Get current month summary
    const currentMonth = getCurrentMonth();
    const thisMonth = monthlySummaries.find(m => m.month === currentMonth) || {
      month: currentMonth,
      total_hours: 0,
      total_cost: 0,
      session_count: 0,
      eu_hours: 0,
      eu_cost: 0,
      us_hours: 0,
      us_cost: 0,
    };

    return jsonResponse({
      sessions: sessions.map(s => ({
        id: s.id,
        startedAt: s.started_at,
        endedAt: s.ended_at,
        durationSeconds: s.duration_seconds,
        durationFormatted: s.duration_seconds
          ? formatDuration(s.duration_seconds)
          : null,
        costUsd: s.cost_usd,
        maxPlayers: s.max_players,
        region: s.region,
        serverType: s.server_type,
      })),
      thisMonth: {
        month: thisMonth.month,
        totalHours: thisMonth.total_hours,
        totalCost: thisMonth.total_cost,
        sessionCount: thisMonth.session_count,
        byRegion: {
          eu: {
            hours: thisMonth.eu_hours,
            cost: thisMonth.eu_cost,
          },
          us: {
            hours: thisMonth.us_hours,
            cost: thisMonth.us_cost,
          },
        },
      },
      monthlySummaries: monthlySummaries.map(m => ({
        month: m.month,
        totalHours: m.total_hours,
        totalCost: m.total_cost,
        sessionCount: m.session_count,
        byRegion: {
          eu: { hours: m.eu_hours, cost: m.eu_cost },
          us: { hours: m.us_hours, cost: m.us_cost },
        },
      })),
      backups: backups.map(b => ({
        id: b.id,
        timestamp: b.timestamp,
        sizeBytes: b.size_bytes,
        sizeMb: b.size_bytes ? (b.size_bytes / 1024 / 1024).toFixed(2) : null,
        triggeredBy: b.triggered_by,
      })),
      totals: {
        allTime: {
          hours: allTimeTotals.hours,
          cost: allTimeTotals.cost,
          sessions: allTimeTotals.sessions,
        },
      },
      pagination: {
        limit,
        offset,
        hasMore: sessions.length === limit,
      },
    });
  } catch (error) {
    console.error('History error:', error);
    return jsonResponse(
      {
        error: 'internal_error',
        error_description: error instanceof Error ? error.message : 'Unknown error',
      },
      500
    );
  }
}

/**
 * Format duration in seconds to human readable string
 */
function formatDuration(seconds: number): string {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);

  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  return `${minutes}m`;
}
