/**
 * mc-control Worker
 * Main entry point for GroveMC server orchestration
 */

import type { Env } from './types/env';

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    const path = url.pathname;

    // Health check
    if (path === '/health') {
      return new Response('OK', { status: 200 });
    }

    // API routes
    if (path.startsWith('/api/mc/')) {
      // TODO: Implement API routes
      // - POST /api/mc/start
      // - POST /api/mc/stop
      // - GET /api/mc/status
      // - GET /api/mc/status/public
      // - GET/POST /api/mc/whitelist
      // - POST /api/mc/command
      // - POST /api/mc/sync
      // - GET /api/mc/history
      // - POST /api/mc/webhook/*

      return new Response(JSON.stringify({ error: 'Not implemented' }), {
        status: 501,
        headers: { 'Content-Type': 'application/json' },
      });
    }

    return new Response('GroveMC Control API', { status: 200 });
  },
} satisfies ExportedHandler<Env>;
