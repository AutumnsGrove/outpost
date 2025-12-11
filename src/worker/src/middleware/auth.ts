/**
 * Authentication Middleware
 * Verifies admin access using GroveAuth JWT or simple Bearer token
 */

import type { Env } from '../types/env.js';
import { ADMIN_EMAILS } from '../types/env.js';

export interface AuthResult {
  authenticated: boolean;
  userId?: string;
  email?: string;
  error?: string;
}

/**
 * Verify webhook requests from VPS
 */
export function verifyWebhookAuth(request: Request, env: Env): boolean {
  const authHeader = request.headers.get('Authorization');
  if (!authHeader?.startsWith('Bearer ')) {
    return false;
  }

  const token = authHeader.substring(7);
  return token === env.WEBHOOK_SECRET;
}

/**
 * Verify admin access using either:
 * 1. GroveAuth JWT token (calls /verify endpoint)
 * 2. Simple ADMIN_AUTH_SECRET Bearer token (fallback)
 */
export async function verifyAdminAuth(
  request: Request,
  env: Env
): Promise<AuthResult> {
  const authHeader = request.headers.get('Authorization');

  if (!authHeader?.startsWith('Bearer ')) {
    return {
      authenticated: false,
      error: 'Missing or invalid Authorization header',
    };
  }

  const token = authHeader.substring(7);

  // Option 1: Simple secret token check (development/fallback)
  if (token === env.ADMIN_AUTH_SECRET) {
    return {
      authenticated: true,
      userId: 'admin',
      email: ADMIN_EMAILS[0],
    };
  }

  // Option 2: Verify via GroveAuth /verify endpoint
  try {
    const verifyResponse = await fetch('https://auth.grove.place/verify', {
      method: 'GET',
      headers: {
        'Authorization': `Bearer ${token}`,
      },
    });

    if (!verifyResponse.ok) {
      return {
        authenticated: false,
        error: 'Invalid or expired token',
      };
    }

    const userData = await verifyResponse.json() as {
      sub?: string;
      email?: string;
      is_admin?: boolean;
    };

    // Check if user is admin
    const email = userData.email;
    if (!email || !ADMIN_EMAILS.includes(email)) {
      return {
        authenticated: false,
        error: 'User is not authorized as admin',
      };
    }

    return {
      authenticated: true,
      userId: userData.sub,
      email: email,
    };
  } catch (error) {
    // GroveAuth might not be reachable, fall through to error
    return {
      authenticated: false,
      error: `Auth verification failed: ${error instanceof Error ? error.message : 'Unknown error'}`,
    };
  }
}

/**
 * Create JSON error response for auth failures
 */
export function authErrorResponse(result: AuthResult): Response {
  return new Response(
    JSON.stringify({
      error: 'unauthorized',
      error_description: result.error || 'Authentication required',
    }),
    {
      status: 401,
      headers: {
        'Content-Type': 'application/json',
        'WWW-Authenticate': 'Bearer realm="GroveMC Admin API"',
      },
    }
  );
}

/**
 * Create JSON forbidden response
 */
export function forbiddenResponse(message = 'Forbidden'): Response {
  return new Response(
    JSON.stringify({
      error: 'forbidden',
      error_description: message,
    }),
    {
      status: 403,
      headers: { 'Content-Type': 'application/json' },
    }
  );
}

/**
 * Middleware wrapper for admin-only routes
 */
export async function requireAdmin(
  request: Request,
  env: Env,
  handler: (request: Request, env: Env, auth: AuthResult) => Promise<Response>
): Promise<Response> {
  const authResult = await verifyAdminAuth(request, env);

  if (!authResult.authenticated) {
    return authErrorResponse(authResult);
  }

  return handler(request, env, authResult);
}

/**
 * Middleware wrapper for webhook routes
 */
export function requireWebhook(
  request: Request,
  env: Env,
  handler: (request: Request, env: Env) => Promise<Response>
): Promise<Response> {
  if (!verifyWebhookAuth(request, env)) {
    return Promise.resolve(
      new Response(
        JSON.stringify({
          error: 'unauthorized',
          error_description: 'Invalid webhook token',
        }),
        {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );
  }

  return handler(request, env);
}

/**
 * Extract bearer token from request
 */
export function extractBearerToken(request: Request): string | null {
  const authHeader = request.headers.get('Authorization');
  if (!authHeader?.startsWith('Bearer ')) {
    return null;
  }
  return authHeader.substring(7);
}
