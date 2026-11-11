/**
 * Stop Route
 * POST /api/mc/stop - Stop the Minecraft server
 */

import type { Env, StopRequest } from '../types/env.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';
import {
  getServerState,
  setServerState,
  getCurrentSession,
  endSession,
  updateMonthlySummary,
  recordBackup,
  getCurrentMonth,
  calculateSessionCost,
} from '../services/database.js';
import { deleteServer, shutdownServer, getServer } from '../services/hetzner.js';

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * POST /api/mc/stop
 * Gracefully stop the Minecraft server and terminate VPS
 */
export async function handleStop(
  request: Request,
  env: Env
): Promise<Response> {
  // Verify admin auth
  const auth = await verifyAdminAuth(request, env);
  if (!auth.authenticated) {
    return authErrorResponse(auth);
  }

  try {
    // Parse request body
    let body: StopRequest = {};
    try {
      body = await request.json() as StopRequest;
    } catch {
      // Empty body is OK
    }

    const { force = false } = body;

    // Get current state
    const currentState = await getServerState(env.DB);

    if (currentState.state === 'OFFLINE') {
      return jsonResponse(
        {
          error: 'server_offline',
          error_description: 'Server is already offline',
        },
        400
      );
    }

    if (currentState.state === 'TERMINATING') {
      return jsonResponse(
        {
          error: 'already_stopping',
          error_description: 'Server is already shutting down',
        },
        409
      );
    }

    const vpsId = currentState.vps_id;

    if (!vpsId) {
      // No VPS ID, just reset state
      await setServerState(env.DB, 'OFFLINE', {
        vps_id: null,
        vps_ip: null,
        region: null,
        server_type: null,
        started_at: null,
        idle_since: null,
        player_count: 0,
      });

      return jsonResponse({
        status: 'offline',
        message: 'Server state reset (no VPS was running)',
      });
    }

    // Set state to TERMINATING
    await setServerState(env.DB, 'TERMINATING');

    console.log(`Stopping server ${vpsId} (force: ${force})`);

    // End the current session
    const session = await getCurrentSession(env.DB);
    if (session && currentState.started_at && currentState.region) {
      const startedAt = new Date(currentState.started_at).getTime();
      const durationSeconds = Math.floor((Date.now() - startedAt) / 1000);
      const costUsd = calculateSessionCost(durationSeconds, currentState.region);

      await endSession(
        env.DB,
        session.id,
        durationSeconds,
        costUsd,
        session.max_players
      );

      // Update monthly summary
      const hours = durationSeconds / 3600;
      await updateMonthlySummary(
        env.DB,
        getCurrentMonth(),
        currentState.region,
        hours,
        costUsd
      );

      // Record shutdown backup
      await recordBackup(env.DB, undefined, session.id, 'shutdown');
    }

    // Delete the Hetzner server
    try {
      if (!force) {
        // Try graceful shutdown first (ACPI)
        try {
          await shutdownServer(env, vpsId);
          // Wait a bit for graceful shutdown
          await new Promise(resolve => setTimeout(resolve, 30000));
        } catch (e) {
          console.log('Graceful shutdown failed, proceeding with delete:', e);
        }
      }

      // Delete the server
      await deleteServer(env, vpsId);

      console.log(`Deleted server ${vpsId}`);
    } catch (error) {
      // Check if server still exists
      const server = await getServer(env, vpsId);
      if (server) {
        console.error('Failed to delete server:', error);
        // Revert state
        await setServerState(env.DB, currentState.state);
        return jsonResponse(
          {
            error: 'delete_failed',
            error_description: error instanceof Error ? error.message : 'Unknown error',
          },
          500
        );
      }
      // Server doesn't exist, continue with cleanup
      console.log('Server already deleted or not found');
    }

    // Reset state to OFFLINE
    await setServerState(env.DB, 'OFFLINE', {
      vps_id: null,
      vps_ip: null,
      region: null,
      server_type: null,
      started_at: null,
      idle_since: null,
      player_count: 0,
    });

    return jsonResponse({
      status: 'offline',
      message: force ? 'Server force stopped' : 'Server stopped gracefully',
      sessionEnded: session?.id,
    });
  } catch (error) {
    console.error('Stop error:', error);
    return jsonResponse(
      {
        error: 'internal_error',
        error_description: error instanceof Error ? error.message : 'Unknown error',
      },
      500
    );
  }
}
