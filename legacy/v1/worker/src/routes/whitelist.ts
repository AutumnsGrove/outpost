/**
 * Whitelist Routes
 * GET /api/mc/whitelist - Get whitelist
 * POST /api/mc/whitelist - Add/remove from whitelist
 */

import type { Env, WhitelistEntry } from '../types/env.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';
import {
  getWhitelist,
  addToWhitelist,
  removeFromWhitelist,
  getServerState,
} from '../services/database.js';

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface WhitelistRequest {
  action: 'add' | 'remove';
  username: string;
}

/**
 * GET /api/mc/whitelist
 * Get current whitelist
 */
export async function handleGetWhitelist(
  request: Request,
  env: Env
): Promise<Response> {
  // Verify admin auth
  const auth = await verifyAdminAuth(request, env);
  if (!auth.authenticated) {
    return authErrorResponse(auth);
  }

  try {
    const whitelist = await getWhitelist(env.DB);

    return jsonResponse({
      whitelist: whitelist.map(entry => ({
        name: entry.username,
        uuid: entry.uuid,
        added_at: entry.added_at,
        added_by: entry.added_by,
      })),
    });
  } catch (error) {
    console.error('Get whitelist error:', error);
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
 * POST /api/mc/whitelist
 * Add or remove player from whitelist
 */
export async function handlePostWhitelist(
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
    let body: WhitelistRequest;
    try {
      body = await request.json() as WhitelistRequest;
    } catch {
      return jsonResponse({ error: 'Invalid JSON body' }, 400);
    }

    const { action, username } = body;

    // Validate input
    if (!action || !['add', 'remove'].includes(action)) {
      return jsonResponse(
        { error: 'Invalid action', error_description: 'Action must be "add" or "remove"' },
        400
      );
    }

    if (!username || typeof username !== 'string' || username.length < 3) {
      return jsonResponse(
        { error: 'Invalid username', error_description: 'Username must be at least 3 characters' },
        400
      );
    }

    // Validate Minecraft username format (3-16 chars, alphanumeric + underscore)
    if (!/^[a-zA-Z0-9_]{3,16}$/.test(username)) {
      return jsonResponse(
        {
          error: 'Invalid username format',
          error_description: 'Username must be 3-16 characters, alphanumeric and underscores only',
        },
        400
      );
    }

    const serverState = await getServerState(env.DB);

    if (action === 'add') {
      // Look up UUID from Mojang API
      let uuid: string | undefined;
      try {
        const mojangResponse = await fetch(
          `https://api.mojang.com/users/profiles/minecraft/${username}`
        );
        if (mojangResponse.ok) {
          const mojangData = await mojangResponse.json() as { id: string; name: string };
          uuid = mojangData.id;
        }
      } catch (e) {
        console.log('Could not fetch UUID from Mojang:', e);
      }

      // Add to database cache
      await addToWhitelist(env.DB, username, uuid, auth.email);

      // If server is running, send whitelist command
      if (serverState.state === 'RUNNING' && serverState.vps_ip) {
        // TODO: Send command to server via SSH or RCON
        // For now, just update the cache
        console.log(`Would send 'whitelist add ${username}' to server`);
      }

      const whitelist = await getWhitelist(env.DB);

      return jsonResponse({
        success: true,
        message: `Added ${username} to whitelist`,
        whitelist: whitelist.map(e => e.username),
      });
    } else {
      // Remove from database cache
      const removed = await removeFromWhitelist(env.DB, username);

      if (!removed) {
        return jsonResponse(
          { error: 'not_found', error_description: `${username} is not on the whitelist` },
          404
        );
      }

      // If server is running, send whitelist command
      if (serverState.state === 'RUNNING' && serverState.vps_ip) {
        // TODO: Send command to server via SSH or RCON
        console.log(`Would send 'whitelist remove ${username}' to server`);
      }

      const whitelist = await getWhitelist(env.DB);

      return jsonResponse({
        success: true,
        message: `Removed ${username} from whitelist`,
        whitelist: whitelist.map(e => e.username),
      });
    }
  } catch (error) {
    console.error('Whitelist error:', error);
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
 * Route whitelist requests
 */
export async function handleWhitelist(
  request: Request,
  env: Env
): Promise<Response> {
  if (request.method === 'GET') {
    return handleGetWhitelist(request, env);
  } else if (request.method === 'POST') {
    return handlePostWhitelist(request, env);
  }

  return jsonResponse({ error: 'Method not allowed' }, 405);
}
