/**
 * Command Route
 * POST /api/mc/command - Send console command to server via RCON
 */

import type { Env } from '../types/env.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';
import { getServerState } from '../services/database.js';
import { sendRconCommand } from '../services/rcon.js';

const RCON_PORT = 25575;

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface CommandRequest {
  command: string;
}

// Commands that are blocked for safety (server control should use API)
const BLOCKED_COMMANDS = [
  'stop',
  'restart',
  'shutdown',
];

// Commands that require special handling
const SPECIAL_COMMANDS = [
  'whitelist',  // Use /api/mc/whitelist instead
  'save-all',   // Use /api/mc/sync instead
  'save-off',
  'save-on',
];

/**
 * POST /api/mc/command
 * Send a command to the Minecraft server console
 */
export async function handleCommand(
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
    let body: CommandRequest;
    try {
      body = await request.json() as CommandRequest;
    } catch {
      return jsonResponse({ error: 'Invalid JSON body' }, 400);
    }

    const { command } = body;

    // Validate command
    if (!command || typeof command !== 'string') {
      return jsonResponse(
        { error: 'Invalid command', error_description: 'Command must be a non-empty string' },
        400
      );
    }

    // Clean and validate command
    const cleanCommand = command.trim().toLowerCase();
    const firstWord = cleanCommand.split(/\s+/)[0];

    // Check for blocked commands
    if (BLOCKED_COMMANDS.includes(firstWord)) {
      return jsonResponse(
        {
          error: 'blocked_command',
          error_description: `Command '${firstWord}' is not allowed via API. Use the appropriate API endpoint instead.`,
        },
        403
      );
    }

    // Warn about special commands
    if (SPECIAL_COMMANDS.includes(firstWord)) {
      return jsonResponse(
        {
          error: 'special_command',
          error_description: `Command '${firstWord}' should be handled via specific API endpoints for safety.`,
          suggestion: firstWord === 'whitelist' ? '/api/mc/whitelist' : '/api/mc/sync',
        },
        400
      );
    }

    // Check server state
    const serverState = await getServerState(env.DB);

    if (serverState.state !== 'RUNNING' && serverState.state !== 'IDLE') {
      return jsonResponse(
        {
          error: 'server_not_running',
          error_description: `Cannot send commands when server is ${serverState.state}`,
        },
        400
      );
    }

    const vpsIp = serverState.vps_ip;
    const rconPassword = serverState.rcon_password;

    if (!vpsIp) {
      return jsonResponse(
        {
          error: 'no_server_ip',
          error_description: 'Server IP not available',
        },
        500
      );
    }

    if (!rconPassword) {
      return jsonResponse(
        {
          error: 'rcon_not_configured',
          error_description: 'RCON password not available. Server may need to be restarted.',
        },
        500
      );
    }

    console.log(`Sending command via RCON: '${command}' to ${vpsIp}`);

    // Send command via RCON
    const result = await sendRconCommand(vpsIp, RCON_PORT, rconPassword, command.trim());

    if (!result.success) {
      console.error(`RCON command failed: ${result.error}`);
      return jsonResponse(
        {
          error: 'rcon_error',
          error_description: result.error || 'Failed to send command',
        },
        500
      );
    }

    console.log(`RCON command success, response: ${result.response}`);

    return jsonResponse({
      success: true,
      command: command.trim(),
      response: result.response || '',
    });
  } catch (error) {
    console.error('Command error:', error);
    return jsonResponse(
      {
        error: 'internal_error',
        error_description: error instanceof Error ? error.message : 'Unknown error',
      },
      500
    );
  }
}
