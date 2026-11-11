/**
 * Status Routes
 * GET /api/mc/status - Full status (admin)
 * GET /api/mc/status/public - Public status (no auth)
 */

import type { Env, StatusResponse, PublicStatusResponse } from '../types/env.js';
import { HETZNER_CONFIG } from '../types/env.js';
import {
  getServerState,
  getCurrentSession,
  getMonthlySummary,
  getLastBackup,
  getCurrentMonth,
  calculateSessionCost,
} from '../services/database.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';

const MINECRAFT_VERSION = '1.20.1';
const MAX_PLAYERS = 20;

/**
 * GET /api/mc/status/public
 * Public endpoint for mc.grove.place status page
 */
export async function handlePublicStatus(
  request: Request,
  env: Env
): Promise<Response> {
  try {
    const state = await getServerState(env.DB);

    const response: PublicStatusResponse = {
      state: state.state,
      players: {
        online: state.player_count || 0,
        max: MAX_PLAYERS,
      },
      version: MINECRAFT_VERSION,
    };

    return new Response(JSON.stringify(response), {
      headers: {
        'Content-Type': 'application/json',
        'Access-Control-Allow-Origin': '*',
        'Cache-Control': 'public, max-age=10',
      },
    });
  } catch (error) {
    console.error('Error getting public status:', error);

    // Return offline state on error
    const response: PublicStatusResponse = {
      state: 'OFFLINE',
      players: { online: 0, max: MAX_PLAYERS },
      version: MINECRAFT_VERSION,
    };

    return new Response(JSON.stringify(response), {
      headers: {
        'Content-Type': 'application/json',
        'Access-Control-Allow-Origin': '*',
      },
    });
  }
}

/**
 * GET /api/mc/status
 * Full status for admin dashboard
 */
export async function handleStatus(
  request: Request,
  env: Env
): Promise<Response> {
  // Verify admin auth
  const auth = await verifyAdminAuth(request, env);
  if (!auth.authenticated) {
    return authErrorResponse(auth);
  }

  try {
    const state = await getServerState(env.DB);
    const currentSession = await getCurrentSession(env.DB);
    const lastBackup = await getLastBackup(env.DB);
    const monthSummary = await getMonthlySummary(env.DB, getCurrentMonth());

    // Get world size from R2
    let worldSizeBytes = 0;
    try {
      const worldObjects = await env.MC_WORLDS.list({ prefix: 'current/' });
      for (const obj of worldObjects.objects) {
        worldSizeBytes += obj.size;
      }
    } catch (e) {
      console.error('Error getting world size:', e);
    }

    // Calculate uptime and costs
    let uptime: number | null = null;
    let currentSessionCost = 0;
    let hourlyRate = 0;

    if (state.started_at && state.state !== 'OFFLINE') {
      const startedAt = new Date(state.started_at).getTime();
      uptime = Math.floor((Date.now() - startedAt) / 1000);

      if (state.region) {
        hourlyRate = HETZNER_CONFIG[state.region].hourlyRate;
        currentSessionCost = calculateSessionCost(uptime, state.region);
      }
    }

    // Calculate idle time
    let idleTime: number | null = null;
    if (state.idle_since) {
      const idleSince = new Date(state.idle_since).getTime();
      idleTime = Math.floor((Date.now() - idleSince) / 1000);
    }

    // Calculate TTL (time until auto-shutdown)
    let ttl: number | null = null;
    if (state.state === 'IDLE' && idleTime !== null) {
      ttl = Math.max(0, 15 * 60 - idleTime); // 15 min idle timeout
    } else if (state.state === 'SUSPENDED' && state.idle_since) {
      const suspendedAt = new Date(state.idle_since).getTime();
      const suspendedTime = Math.floor((Date.now() - suspendedAt) / 1000);
      ttl = Math.max(0, 45 * 60 - suspendedTime); // 45 min suspend timeout
    }

    const response = {
      state: state.state,
      region: state.region,
      serverType: state.server_type,
      serverId: state.vps_id,
      serverIp: state.vps_ip,
      players: {
        online: state.player_count || 0,
        max: MAX_PLAYERS,
        // TODO: Add player list from VPS
      },
      uptime,
      idleTime,
      ttl,
      lastWorldSync: lastBackup?.timestamp || null,
      worldSizeBytes,
      costs: {
        currentSession: currentSessionCost,
        hourlyRate,
        thisMonth: monthSummary?.total_cost || 0,
        thisMonthByRegion: {
          eu: monthSummary?.eu_cost || 0,
          us: monthSummary?.us_cost || 0,
        },
      },
    };

    return new Response(JSON.stringify(response), {
      headers: {
        'Content-Type': 'application/json',
        'Access-Control-Allow-Origin': '*',
      },
    });
  } catch (error) {
    console.error('Error getting status:', error);

    return new Response(
      JSON.stringify({
        error: 'internal_error',
        error_description: error instanceof Error ? error.message : 'Unknown error',
      }),
      {
        status: 500,
        headers: { 'Content-Type': 'application/json' },
      }
    );
  }
}
