/**
 * Health Check Service
 * Detects and reconciles orphaned VPS/state mismatches
 */

import type { Env } from '../types/env.js';
import { getServerState, setServerState } from './database.js';
import { getServer, deleteServer } from './hetzner.js';

/**
 * Run health check to detect and fix orphaned states
 * Called by the cron trigger every 5 minutes
 */
export async function runHealthCheck(env: Env): Promise<{
  status: 'ok' | 'fixed' | 'error';
  message: string;
  details?: Record<string, unknown>;
}> {
  try {
    const state = await getServerState(env.DB);

    // If state is OFFLINE, nothing to check
    if (state.state === 'OFFLINE') {
      return { status: 'ok', message: 'Server is offline, no health check needed' };
    }

    // If we have a VPS ID, verify it exists in Hetzner
    if (state.vps_id) {
      const server = await getServer(env, state.vps_id);

      if (!server) {
        // VPS was deleted externally but state is not OFFLINE
        console.log(`Health check: VPS ${state.vps_id} not found, resetting state to OFFLINE`);

        await setServerState(env.DB, 'OFFLINE', {
          vps_id: null,
          vps_ip: null,
          region: null,
          server_type: null,
          started_at: null,
          player_count: 0,
          idle_since: null,
        });

        return {
          status: 'fixed',
          message: `Orphaned state detected and fixed. VPS ${state.vps_id} was deleted externally.`,
          details: { previousState: state.state, vpsId: state.vps_id },
        };
      }

      // VPS exists, check if it's in a bad state
      if (server.status === 'off' || server.status === 'deleting') {
        console.log(`Health check: VPS ${state.vps_id} is ${server.status}, resetting state to OFFLINE`);

        // Try to delete if it's off but not deleting
        if (server.status === 'off') {
          try {
            await deleteServer(env, state.vps_id);
          } catch (e) {
            console.error('Failed to delete off server:', e);
          }
        }

        await setServerState(env.DB, 'OFFLINE', {
          vps_id: null,
          vps_ip: null,
          region: null,
          server_type: null,
          started_at: null,
          player_count: 0,
          idle_since: null,
        });

        return {
          status: 'fixed',
          message: `VPS was ${server.status}, state reset to OFFLINE`,
          details: { vpsStatus: server.status, vpsId: state.vps_id },
        };
      }

      return {
        status: 'ok',
        message: `VPS ${state.vps_id} is ${server.status}`,
        details: { state: state.state, vpsStatus: server.status },
      };
    }

    // State is not OFFLINE but no VPS ID - this is inconsistent
    if (state.state !== 'OFFLINE' && !state.vps_id) {
      console.log(`Health check: State is ${state.state} but no VPS ID, resetting to OFFLINE`);

      await setServerState(env.DB, 'OFFLINE', {
        vps_id: null,
        vps_ip: null,
        region: null,
        server_type: null,
        started_at: null,
        player_count: 0,
        idle_since: null,
      });

      return {
        status: 'fixed',
        message: `Inconsistent state (${state.state} with no VPS ID) reset to OFFLINE`,
        details: { previousState: state.state },
      };
    }

    return { status: 'ok', message: 'Health check passed' };

  } catch (error) {
    console.error('Health check error:', error);
    return {
      status: 'error',
      message: error instanceof Error ? error.message : 'Unknown error',
    };
  }
}
