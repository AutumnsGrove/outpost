/**
 * Start Route
 * POST /api/mc/start - Start the Minecraft server
 */

import type { Env, StartRequest, StartResponse, Region } from '../types/env.js';
import { HETZNER_CONFIG } from '../types/env.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';
import {
  getServerState,
  setServerState,
  createSession,
} from '../services/database.js';
import { createServer, getHourlyRate, getServerType } from '../services/hetzner.js';
import { generateCloudInit } from '../services/cloudinit.js';

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * POST /api/mc/start
 * Provision a new Hetzner VPS for Minecraft
 */
export async function handleStart(
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
    let body: StartRequest;
    try {
      body = await request.json() as StartRequest;
    } catch {
      return jsonResponse({ error: 'Invalid JSON body' }, 400);
    }

    const { region } = body;

    // Validate region
    if (!region || !['eu', 'us'].includes(region)) {
      return jsonResponse(
        { error: 'Invalid region', error_description: 'Region must be "eu" or "us"' },
        400
      );
    }

    // Check current state
    const currentState = await getServerState(env.DB);

    if (currentState.state !== 'OFFLINE') {
      return jsonResponse(
        {
          error: 'server_not_offline',
          error_description: `Server is currently ${currentState.state}. Stop the server first.`,
          currentState: currentState.state,
        },
        409
      );
    }

    // Set state to PROVISIONING
    await setServerState(env.DB, 'PROVISIONING', {
      region: region as Region,
      server_type: getServerType(region as Region),
      started_at: new Date().toISOString(),
      vps_id: null,
      vps_ip: null,
      player_count: 0,
      idle_since: null,
    });

    // Generate cloud-init with secrets
    const webhookUrl = 'https://mc-control.grove.place/api/mc/webhook';
    const cloudInit = generateCloudInit(env, {
      region: region as Region,
      webhookUrl,
      tunnelToken: env.CF_TUNNEL_TOKEN,
    });

    // Create Hetzner server
    let server: { id: string; ip: string; serverType: string };
    try {
      server = await createServer(env, region as Region, cloudInit);
    } catch (error) {
      // Rollback state on failure
      await setServerState(env.DB, 'OFFLINE', {
        region: null,
        server_type: null,
        started_at: null,
      });

      console.error('Failed to create server:', error);
      return jsonResponse(
        {
          error: 'provisioning_failed',
          error_description: error instanceof Error ? error.message : 'Unknown error',
        },
        500
      );
    }

    // Update state with server info
    await setServerState(env.DB, 'PROVISIONING', {
      vps_id: server.id,
      vps_ip: server.ip,
    });

    // Create session record
    await createSession(
      env.DB,
      region as Region,
      server.serverType,
      server.id
    );

    // Estimate ready time (2-3 minutes for cloud-init)
    const estimatedReadyTime = new Date(Date.now() + 3 * 60 * 1000).toISOString();

    const response: StartResponse = {
      status: 'provisioning',
      region: region as Region,
      serverType: server.serverType,
      estimatedReadyTime,
      serverId: server.id,
      hourlyRate: getHourlyRate(region as Region),
    };

    console.log(`Started server ${server.id} in ${region} (${server.serverType})`);

    return jsonResponse(response);
  } catch (error) {
    console.error('Start error:', error);
    return jsonResponse(
      {
        error: 'internal_error',
        error_description: error instanceof Error ? error.message : 'Unknown error',
      },
      500
    );
  }
}
