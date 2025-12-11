/**
 * mc-control Worker
 * Main entry point for GroveMC server orchestration
 */

import type { Env } from './types/env.js';
import { handleStatus, handlePublicStatus } from './routes/status.js';
import { handleStart } from './routes/start.js';
import { handleStop } from './routes/stop.js';
import { handleWhitelist } from './routes/whitelist.js';
import { handleCommand } from './routes/command.js';
import { handleSync } from './routes/sync.js';
import { handleHistory } from './routes/history.js';
import { handleWebhook } from './routes/webhooks.js';

// CORS headers for cross-origin requests
const corsHeaders = {
  'Access-Control-Allow-Origin': '*',
  'Access-Control-Allow-Methods': 'GET, POST, PUT, DELETE, OPTIONS',
  'Access-Control-Allow-Headers': 'Content-Type, Authorization',
  'Access-Control-Max-Age': '86400',
};

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: {
      'Content-Type': 'application/json',
      ...corsHeaders,
    },
  });
}

function methodNotAllowed(): Response {
  return jsonResponse({ error: 'Method not allowed' }, 405);
}

function notFound(): Response {
  return jsonResponse({ error: 'Not found' }, 404);
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    const path = url.pathname;
    const method = request.method;

    // Handle CORS preflight
    if (method === 'OPTIONS') {
      return new Response(null, {
        status: 204,
        headers: corsHeaders,
      });
    }

    // Health check
    if (path === '/health' || path === '/') {
      if (method !== 'GET') return methodNotAllowed();
      return new Response('GroveMC Control API - OK', {
        status: 200,
        headers: corsHeaders,
      });
    }

    // =========================================================================
    // API Routes: /api/mc/*
    // =========================================================================

    if (path.startsWith('/api/mc/')) {
      try {
        // Public status (no auth required)
        if (path === '/api/mc/status/public') {
          if (method !== 'GET') return methodNotAllowed();
          return handlePublicStatus(request, env);
        }

        // Full status (admin auth)
        if (path === '/api/mc/status') {
          if (method !== 'GET') return methodNotAllowed();
          return handleStatus(request, env);
        }

        // Start server
        if (path === '/api/mc/start') {
          if (method !== 'POST') return methodNotAllowed();
          return handleStart(request, env);
        }

        // Stop server
        if (path === '/api/mc/stop') {
          if (method !== 'POST') return methodNotAllowed();
          return handleStop(request, env);
        }

        // Whitelist management
        if (path === '/api/mc/whitelist') {
          if (method !== 'GET' && method !== 'POST') return methodNotAllowed();
          return handleWhitelist(request, env);
        }

        // Send command
        if (path === '/api/mc/command') {
          if (method !== 'POST') return methodNotAllowed();
          return handleCommand(request, env);
        }

        // Manual sync/backup
        if (path === '/api/mc/sync') {
          if (method !== 'POST') return methodNotAllowed();
          return handleSync(request, env);
        }

        // Session history
        if (path === '/api/mc/history') {
          if (method !== 'GET') return methodNotAllowed();
          return handleHistory(request, env);
        }

        // Webhook endpoints (from VPS)
        if (path.startsWith('/api/mc/webhook/')) {
          if (method !== 'POST') return methodNotAllowed();
          const webhookType = path.replace('/api/mc/webhook/', '');
          return handleWebhook(request, env, webhookType);
        }

        // Unknown API route
        return notFound();
      } catch (error) {
        console.error('API error:', error);
        return jsonResponse(
          {
            error: 'internal_error',
            error_description: error instanceof Error ? error.message : 'Unknown error',
          },
          500
        );
      }
    }

    // Not an API route
    return notFound();
  },
} satisfies ExportedHandler<Env>;
