/**
 * Webhook Routes
 * POST /api/mc/webhook/ready - VPS ready notification
 * POST /api/mc/webhook/heartbeat - Periodic heartbeat
 * POST /api/mc/webhook/state-change - State change notification
 * POST /api/mc/webhook/backup-complete - Backup completion notification
 */

import type { Env, WebhookPayload, ServerState } from '../types/env.js';
import { verifyWebhookAuth } from '../middleware/auth.js';
import {
  getServerState,
  setServerState,
  updateServerState,
  getCurrentSession,
  updateSessionMaxPlayers,
  recordBackup,
} from '../services/database.js';
import { updateMcRecord } from '../services/dns.js';

/**
 * Verify webhook and return error if invalid
 */
function webhookError(message: string): Response {
  return new Response(
    JSON.stringify({ error: 'unauthorized', error_description: message }),
    { status: 401, headers: { 'Content-Type': 'application/json' } }
  );
}

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * POST /api/mc/webhook/ready
 * Called by cloud-init when VPS is ready
 */
export async function handleWebhookReady(
  request: Request,
  env: Env
): Promise<Response> {
  if (!verifyWebhookAuth(request, env)) {
    return webhookError('Invalid webhook token');
  }

  try {
    const payload = await request.json() as WebhookPayload;
    const { serverId, ip, region } = payload;

    if (!serverId || !ip) {
      return jsonResponse({ error: 'Missing serverId or ip' }, 400);
    }

    console.log(`Server ready: ${serverId} at ${ip} (${region})`);

    // Update DNS record
    await updateMcRecord(env, ip);

    // Update server state
    await setServerState(env.DB, 'RUNNING', {
      vps_id: serverId,
      vps_ip: ip,
      dns_updated_at: new Date().toISOString(),
      last_heartbeat: new Date().toISOString(),
    });

    return jsonResponse({ success: true, message: 'Server registered and DNS updated' });
  } catch (error) {
    console.error('Webhook ready error:', error);
    return jsonResponse(
      { error: 'internal_error', message: error instanceof Error ? error.message : 'Unknown error' },
      500
    );
  }
}

/**
 * POST /api/mc/webhook/heartbeat
 * Called periodically by watchdog
 */
export async function handleWebhookHeartbeat(
  request: Request,
  env: Env
): Promise<Response> {
  if (!verifyWebhookAuth(request, env)) {
    return webhookError('Invalid webhook token');
  }

  try {
    const payload = await request.json() as WebhookPayload;
    const { state, players, idleSeconds, lastBackup } = payload;

    const currentState = await getServerState(env.DB);

    // Update state based on heartbeat data
    const updates: Partial<{
      state: ServerState;
      player_count: number;
      idle_since: string | null;
      last_heartbeat: string;
    }> = {
      last_heartbeat: new Date().toISOString(),
    };

    if (players !== undefined) {
      updates.player_count = players;

      // Update session max players
      const session = await getCurrentSession(env.DB);
      if (session) {
        await updateSessionMaxPlayers(env.DB, session.id, players);
      }
    }

    // Handle state transitions
    if (state && state !== currentState.state) {
      updates.state = state as ServerState;

      // Track when we became idle
      if (state === 'IDLE' || state === 'SUSPENDED') {
        updates.idle_since = new Date().toISOString();
      } else if (state === 'RUNNING') {
        updates.idle_since = null;
      }
    }

    await updateServerState(env.DB, updates);

    return jsonResponse({
      success: true,
      currentState: updates.state || currentState.state,
    });
  } catch (error) {
    console.error('Webhook heartbeat error:', error);
    return jsonResponse(
      { error: 'internal_error', message: error instanceof Error ? error.message : 'Unknown error' },
      500
    );
  }
}

/**
 * POST /api/mc/webhook/state-change
 * Called when watchdog detects a state change
 */
export async function handleWebhookStateChange(
  request: Request,
  env: Env
): Promise<Response> {
  if (!verifyWebhookAuth(request, env)) {
    return webhookError('Invalid webhook token');
  }

  try {
    const payload = await request.json() as WebhookPayload;
    const { state, timestamp } = payload;

    if (!state) {
      return jsonResponse({ error: 'Missing state' }, 400);
    }

    console.log(`State change: ${state} at ${timestamp}`);

    const updates: Partial<{
      state: ServerState;
      idle_since: string | null;
      last_heartbeat: string;
    }> = {
      state: state as ServerState,
      last_heartbeat: new Date().toISOString(),
    };

    // Track idle/suspend time
    if (state === 'IDLE' || state === 'SUSPENDED') {
      updates.idle_since = timestamp || new Date().toISOString();
    } else if (state === 'RUNNING') {
      updates.idle_since = null;
    }

    await updateServerState(env.DB, updates);

    // If transitioning to TERMINATING, the VPS will self-terminate
    // The worker will clean up state when it can't reach the VPS

    return jsonResponse({ success: true, state });
  } catch (error) {
    console.error('Webhook state-change error:', error);
    return jsonResponse(
      { error: 'internal_error', message: error instanceof Error ? error.message : 'Unknown error' },
      500
    );
  }
}

/**
 * POST /api/mc/webhook/backup-complete
 * Called when world backup is complete
 */
export async function handleWebhookBackupComplete(
  request: Request,
  env: Env
): Promise<Response> {
  if (!verifyWebhookAuth(request, env)) {
    return webhookError('Invalid webhook token');
  }

  try {
    const payload = await request.json() as WebhookPayload;
    const { timestamp, sizeBytes } = payload;

    console.log(`Backup complete: ${sizeBytes} bytes at ${timestamp}`);

    // Record the backup
    const session = await getCurrentSession(env.DB);
    await recordBackup(env.DB, sizeBytes, session?.id, 'auto');

    return jsonResponse({ success: true, timestamp });
  } catch (error) {
    console.error('Webhook backup-complete error:', error);
    return jsonResponse(
      { error: 'internal_error', message: error instanceof Error ? error.message : 'Unknown error' },
      500
    );
  }
}

/**
 * Route webhook requests to appropriate handler
 */
export async function handleWebhook(
  request: Request,
  env: Env,
  webhookType: string
): Promise<Response> {
  switch (webhookType) {
    case 'ready':
      return handleWebhookReady(request, env);
    case 'heartbeat':
      return handleWebhookHeartbeat(request, env);
    case 'state-change':
      return handleWebhookStateChange(request, env);
    case 'backup-complete':
      return handleWebhookBackupComplete(request, env);
    default:
      return jsonResponse({ error: 'Unknown webhook type' }, 404);
  }
}
