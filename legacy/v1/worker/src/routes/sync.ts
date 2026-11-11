/**
 * Sync Route
 * POST /api/mc/sync - Trigger manual world backup
 */

import type { Env } from '../types/env.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';
import { getServerState, getCurrentSession, recordBackup } from '../services/database.js';

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * POST /api/mc/sync
 * Trigger a manual world backup
 */
export async function handleSync(
  request: Request,
  env: Env
): Promise<Response> {
  // Verify admin auth
  const auth = await verifyAdminAuth(request, env);
  if (!auth.authenticated) {
    return authErrorResponse(auth);
  }

  try {
    const serverState = await getServerState(env.DB);

    // Check if server is running
    if (serverState.state !== 'RUNNING' && serverState.state !== 'IDLE') {
      return jsonResponse(
        {
          error: 'server_not_running',
          error_description: `Cannot sync when server is ${serverState.state}`,
        },
        400
      );
    }

    const vpsIp = serverState.vps_ip;

    if (!vpsIp) {
      return jsonResponse(
        {
          error: 'no_server_ip',
          error_description: 'Server IP not available',
        },
        500
      );
    }

    // TODO: Trigger backup on VPS via SSH or API call
    // For now, the watchdog handles periodic backups
    // This endpoint would SSH to the server and run sync-to-r2.sh

    console.log(`Manual sync requested for server at ${vpsIp}`);

    // Record the backup request (actual backup will be recorded by webhook)
    const session = await getCurrentSession(env.DB);

    // In a full implementation, we would:
    // 1. SSH to server or call an endpoint
    // 2. Execute /opt/minecraft/sync-to-r2.sh
    // 3. Wait for backup-complete webhook

    // For now, just log and return pending status
    return jsonResponse({
      status: 'pending',
      message: 'Backup request received. Watchdog will perform sync on next interval.',
      serverIp: vpsIp,
      sessionId: session?.id,
    });
  } catch (error) {
    console.error('Sync error:', error);
    return jsonResponse(
      {
        error: 'internal_error',
        error_description: error instanceof Error ? error.message : 'Unknown error',
      },
      500
    );
  }
}
