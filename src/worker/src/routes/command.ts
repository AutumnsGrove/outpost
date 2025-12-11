/**
 * Command Route
 * POST /api/mc/command - Send console command to server
 */

import type { Env } from '../types/env.js';
import { verifyAdminAuth, authErrorResponse } from '../middleware/auth.js';
import { getServerState } from '../services/database.js';

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface CommandRequest {
  command: string;
}

// Commands that are blocked for safety
const BLOCKED_COMMANDS = [
  'stop',
  'restart',
  'shutdown',
  'op',
  'deop',
  'ban',
  'ban-ip',
  'pardon',
  'pardon-ip',
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

    if (!vpsIp) {
      return jsonResponse(
        {
          error: 'no_server_ip',
          error_description: 'Server IP not available',
        },
        500
      );
    }

    // TODO: Send command via RCON or SSH
    // For a full implementation, we would:
    // 1. Connect to RCON on port 25575
    // 2. Send the command
    // 3. Return the response

    console.log(`Command requested: '${command}' for server at ${vpsIp}`);

    // For now, return a pending status
    // The real implementation would use RCON or screen/tmux commands
    return jsonResponse({
      status: 'pending',
      message: 'Command execution not yet implemented. Would send to server.',
      command: command.trim(),
      serverIp: vpsIp,
      note: 'RCON or SSH command execution to be implemented',
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
