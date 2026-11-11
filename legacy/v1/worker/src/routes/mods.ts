/**
 * Mod Management Routes
 * List, delete, and upload mods to R2
 */

import type { Env } from '../types/env.js';
import { requireAdmin, authErrorResponse, forbiddenResponse } from '../middleware/auth.js';
import { getServerState } from '../services/database.js';

const corsHeaders = {
  'Access-Control-Allow-Origin': '*',
  'Content-Type': 'application/json',
};

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: corsHeaders,
  });
}

export interface ModInfo {
  filename: string;
  size: number;
  uploaded: string;
}

/**
 * GET /api/mc/mods - List all mods
 */
export async function handleListMods(
  request: Request,
  env: Env
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      const mods: ModInfo[] = [];
      let cursor: string | undefined;
      let totalSize = 0;

      // Paginate through all objects in mods/
      do {
        const list = await env.MC_ASSETS.list({
          prefix: 'mods/',
          cursor,
        });

        for (const obj of list.objects) {
          // Skip the directory marker if present
          if (obj.key === 'mods/') continue;

          const filename = obj.key.replace('mods/', '');
          mods.push({
            filename,
            size: obj.size,
            uploaded: obj.uploaded.toISOString(),
          });
          totalSize += obj.size;
        }

        cursor = list.truncated ? list.cursor : undefined;
      } while (cursor);

      // Sort by filename
      mods.sort((a, b) => a.filename.localeCompare(b.filename));

      return jsonResponse({
        mods,
        count: mods.length,
        totalSize,
        totalSizeFormatted: formatBytes(totalSize),
      });
    } catch (error) {
      console.error('Error listing mods:', error);
      return jsonResponse(
        { error: 'Failed to list mods', details: String(error) },
        500
      );
    }
  });
}

/**
 * DELETE /api/mc/mods - Delete all mods (requires confirmation header)
 */
export async function handleDeleteAllMods(
  request: Request,
  env: Env
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      // Check server is offline
      const state = await getServerState(env.DB);
      if (state.state !== 'OFFLINE') {
        return jsonResponse(
          {
            error: 'server_running',
            error_description: 'Server must be offline to delete mods',
            currentState: state.state,
          },
          400
        );
      }

      // Require confirmation header
      const confirm = req.headers.get('X-Confirm-Delete');
      if (confirm !== 'DELETE_ALL_MODS') {
        return jsonResponse(
          {
            error: 'confirmation_required',
            error_description: 'Set header X-Confirm-Delete: DELETE_ALL_MODS to confirm',
          },
          400
        );
      }

      // List and delete all mods
      let deleted = 0;
      let freedBytes = 0;
      let cursor: string | undefined;

      do {
        const list = await env.MC_ASSETS.list({
          prefix: 'mods/',
          cursor,
        });

        for (const obj of list.objects) {
          if (obj.key === 'mods/') continue;
          freedBytes += obj.size;
          await env.MC_ASSETS.delete(obj.key);
          deleted++;
        }

        cursor = list.truncated ? list.cursor : undefined;
      } while (cursor);

      console.log(`Deleted ${deleted} mods, freed ${formatBytes(freedBytes)}`);

      return jsonResponse({
        success: true,
        deleted,
        freedBytes,
        freedFormatted: formatBytes(freedBytes),
      });
    } catch (error) {
      console.error('Error deleting mods:', error);
      return jsonResponse(
        { error: 'Failed to delete mods', details: String(error) },
        500
      );
    }
  });
}

/**
 * DELETE /api/mc/mods/:filename - Delete a specific mod
 */
export async function handleDeleteMod(
  request: Request,
  env: Env,
  filename: string
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      // Check server is offline
      const state = await getServerState(env.DB);
      if (state.state !== 'OFFLINE') {
        return jsonResponse(
          {
            error: 'server_running',
            error_description: 'Server must be offline to delete mods',
          },
          400
        );
      }

      // Decode filename (might be URL encoded)
      const decodedFilename = decodeURIComponent(filename);
      const key = `mods/${decodedFilename}`;

      // Check if mod exists
      const obj = await env.MC_ASSETS.head(key);
      if (!obj) {
        return jsonResponse(
          { error: 'not_found', error_description: `Mod not found: ${decodedFilename}` },
          404
        );
      }

      // Delete it
      await env.MC_ASSETS.delete(key);

      console.log(`Deleted mod: ${decodedFilename}`);

      return jsonResponse({
        success: true,
        deleted: decodedFilename,
        freedBytes: obj.size,
      });
    } catch (error) {
      console.error('Error deleting mod:', error);
      return jsonResponse(
        { error: 'Failed to delete mod', details: String(error) },
        500
      );
    }
  });
}

/**
 * POST /api/mc/mods/upload - Upload a mod file
 * Expects multipart/form-data with 'file' field
 */
export async function handleUploadMod(
  request: Request,
  env: Env
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      // Check server is offline
      const state = await getServerState(env.DB);
      if (state.state !== 'OFFLINE') {
        return jsonResponse(
          {
            error: 'server_running',
            error_description: 'Server must be offline to upload mods',
          },
          400
        );
      }

      const contentType = req.headers.get('Content-Type') || '';

      if (contentType.includes('multipart/form-data')) {
        // Handle multipart upload
        const formData = await req.formData();
        const file = formData.get('file') as File | null;

        if (!file) {
          return jsonResponse(
            { error: 'No file provided in form data' },
            400
          );
        }

        // Validate file extension
        if (!file.name.endsWith('.jar')) {
          return jsonResponse(
            { error: 'Only .jar files are allowed' },
            400
          );
        }

        // Upload to R2
        const key = `mods/${file.name}`;
        await env.MC_ASSETS.put(key, file.stream(), {
          httpMetadata: {
            contentType: 'application/java-archive',
          },
        });

        console.log(`Uploaded mod: ${file.name} (${formatBytes(file.size)})`);

        return jsonResponse({
          success: true,
          filename: file.name,
          size: file.size,
          sizeFormatted: formatBytes(file.size),
        });
      } else {
        // Handle raw binary upload with filename in header
        const filename = req.headers.get('X-Filename');
        if (!filename) {
          return jsonResponse(
            { error: 'X-Filename header required for raw upload' },
            400
          );
        }

        if (!filename.endsWith('.jar')) {
          return jsonResponse(
            { error: 'Only .jar files are allowed' },
            400
          );
        }

        const body = await req.arrayBuffer();
        const key = `mods/${filename}`;

        await env.MC_ASSETS.put(key, body, {
          httpMetadata: {
            contentType: 'application/java-archive',
          },
        });

        console.log(`Uploaded mod: ${filename} (${formatBytes(body.byteLength)})`);

        return jsonResponse({
          success: true,
          filename,
          size: body.byteLength,
          sizeFormatted: formatBytes(body.byteLength),
        });
      }
    } catch (error) {
      console.error('Error uploading mod:', error);
      return jsonResponse(
        { error: 'Failed to upload mod', details: String(error) },
        500
      );
    }
  });
}

/**
 * Format bytes to human readable string
 */
function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
}
