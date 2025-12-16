/**
 * World Management Routes
 * World info, reset, backups, and restore
 */

import type { Env } from '../types/env.js';
import { requireAdmin } from '../middleware/auth.js';
import { getServerState, getRecentBackups, getLastBackup } from '../services/database.js';

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

export interface WorldInfo {
  size: number;
  sizeFormatted: string;
  lastBackup: string | null;
  backupCount: number;
  hasWorld: boolean;
}

export interface BackupInfo {
  id: string;
  filename: string;
  size: number;
  sizeFormatted: string;
  created: string;
}

/**
 * GET /api/mc/world - Get world info
 */
export async function handleGetWorld(
  request: Request,
  env: Env
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      // Get world size from R2
      let worldSize = 0;
      let hasWorld = false;
      let cursor: string | undefined;

      do {
        const list = await env.MC_WORLDS.list({
          prefix: 'current/',
          cursor,
        });

        for (const obj of list.objects) {
          if (obj.key === 'current/') continue;
          worldSize += obj.size;
          hasWorld = true;
        }

        cursor = list.truncated ? list.cursor : undefined;
      } while (cursor);

      // Get backup info from D1
      const lastBackup = await getLastBackup(env.DB);
      const backups = await getRecentBackups(env.DB, 100);

      return jsonResponse({
        size: worldSize,
        sizeFormatted: formatBytes(worldSize),
        lastBackup: lastBackup?.timestamp || null,
        backupCount: backups.length,
        hasWorld,
      });
    } catch (error) {
      console.error('Error getting world info:', error);
      return jsonResponse(
        { error: 'Failed to get world info', details: String(error) },
        500
      );
    }
  });
}

/**
 * DELETE /api/mc/world - Reset world (delete current world data)
 */
export async function handleDeleteWorld(
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
            error_description: 'Server must be offline to reset world',
            currentState: state.state,
          },
          400
        );
      }

      // Require confirmation header
      const confirm = req.headers.get('X-Confirm-Delete');
      if (confirm !== 'RESET_WORLD') {
        return jsonResponse(
          {
            error: 'confirmation_required',
            error_description: 'Set header X-Confirm-Delete: RESET_WORLD to confirm',
          },
          400
        );
      }

      // List and delete all files in current/
      let deleted = 0;
      let freedBytes = 0;
      let cursor: string | undefined;

      do {
        const list = await env.MC_WORLDS.list({
          prefix: 'current/',
          cursor,
        });

        for (const obj of list.objects) {
          if (obj.key === 'current/') continue;
          freedBytes += obj.size;
          await env.MC_WORLDS.delete(obj.key);
          deleted++;
        }

        cursor = list.truncated ? list.cursor : undefined;
      } while (cursor);

      console.log(`Deleted world data: ${deleted} files, freed ${formatBytes(freedBytes)}`);

      return jsonResponse({
        success: true,
        message: 'World reset. A fresh world will be generated on next server start.',
        deletedFiles: deleted,
        freedBytes,
        freedFormatted: formatBytes(freedBytes),
      });
    } catch (error) {
      console.error('Error resetting world:', error);
      return jsonResponse(
        { error: 'Failed to reset world', details: String(error) },
        500
      );
    }
  });
}

/**
 * GET /api/mc/backups - List available backups
 */
export async function handleListBackups(
  request: Request,
  env: Env
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      const url = new URL(req.url);
      const limit = parseInt(url.searchParams.get('limit') || '20');

      // Get backups from R2
      const r2Backups: BackupInfo[] = [];
      let cursor: string | undefined;

      do {
        const list = await env.MC_WORLDS.list({
          prefix: 'backups/',
          cursor,
        });

        for (const obj of list.objects) {
          if (obj.key === 'backups/') continue;
          const filename = obj.key.replace('backups/', '');
          r2Backups.push({
            id: filename.replace('.tar.gz', ''),
            filename,
            size: obj.size,
            sizeFormatted: formatBytes(obj.size),
            created: obj.uploaded.toISOString(),
          });
        }

        cursor = list.truncated ? list.cursor : undefined;
      } while (cursor);

      // Sort by date descending
      r2Backups.sort((a, b) =>
        new Date(b.created).getTime() - new Date(a.created).getTime()
      );

      // Limit results
      const backups = r2Backups.slice(0, limit);

      // Also get D1 backup records for additional metadata
      const dbBackups = await getRecentBackups(env.DB, limit);

      return jsonResponse({
        backups,
        count: backups.length,
        totalAvailable: r2Backups.length,
        dbRecords: dbBackups.length,
      });
    } catch (error) {
      console.error('Error listing backups:', error);
      return jsonResponse(
        { error: 'Failed to list backups', details: String(error) },
        500
      );
    }
  });
}

/**
 * POST /api/mc/backups/:id/restore - Restore a backup
 */
export async function handleRestoreBackup(
  request: Request,
  env: Env,
  backupId: string
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      // Check server is offline
      const state = await getServerState(env.DB);
      if (state.state !== 'OFFLINE') {
        return jsonResponse(
          {
            error: 'server_running',
            error_description: 'Server must be offline to restore backup',
          },
          400
        );
      }

      // Require confirmation
      const confirm = req.headers.get('X-Confirm-Restore');
      if (confirm !== 'RESTORE_BACKUP') {
        return jsonResponse(
          {
            error: 'confirmation_required',
            error_description: 'Set header X-Confirm-Restore: RESTORE_BACKUP to confirm',
          },
          400
        );
      }

      // Find the backup file
      const backupKey = `backups/${backupId}.tar.gz`;
      const backup = await env.MC_WORLDS.head(backupKey);

      if (!backup) {
        return jsonResponse(
          { error: 'not_found', error_description: `Backup not found: ${backupId}` },
          404
        );
      }

      // Delete current world
      let cursor: string | undefined;
      do {
        const list = await env.MC_WORLDS.list({
          prefix: 'current/',
          cursor,
        });

        for (const obj of list.objects) {
          if (obj.key === 'current/') continue;
          await env.MC_WORLDS.delete(obj.key);
        }

        cursor = list.truncated ? list.cursor : undefined;
      } while (cursor);

      // Copy backup to current/
      const backupData = await env.MC_WORLDS.get(backupKey);
      if (!backupData) {
        return jsonResponse(
          { error: 'Failed to read backup file' },
          500
        );
      }

      await env.MC_WORLDS.put('current/world.tar.gz', backupData.body, {
        httpMetadata: {
          contentType: 'application/gzip',
        },
      });

      console.log(`Restored backup: ${backupId}`);

      return jsonResponse({
        success: true,
        message: 'Backup restored successfully',
        backupId,
        backupSize: backup.size,
        backupSizeFormatted: formatBytes(backup.size),
      });
    } catch (error) {
      console.error('Error restoring backup:', error);
      return jsonResponse(
        { error: 'Failed to restore backup', details: String(error) },
        500
      );
    }
  });
}

/**
 * GET /api/mc/backups/:id/download - Get signed download URL for backup
 */
export async function handleDownloadBackup(
  request: Request,
  env: Env,
  backupId: string
): Promise<Response> {
  return requireAdmin(request, env, async (req, env) => {
    try {
      const backupKey = `backups/${backupId}.tar.gz`;
      const backup = await env.MC_WORLDS.head(backupKey);

      if (!backup) {
        return jsonResponse(
          { error: 'not_found', error_description: `Backup not found: ${backupId}` },
          404
        );
      }

      // For now, we'll stream the file directly
      // In a production setup, you'd generate a signed URL
      const backupData = await env.MC_WORLDS.get(backupKey);
      if (!backupData) {
        return jsonResponse(
          { error: 'Failed to read backup file' },
          500
        );
      }

      return new Response(backupData.body, {
        headers: {
          'Content-Type': 'application/gzip',
          'Content-Disposition': `attachment; filename="${backupId}.tar.gz"`,
          'Content-Length': String(backup.size),
        },
      });
    } catch (error) {
      console.error('Error downloading backup:', error);
      return jsonResponse(
        { error: 'Failed to download backup', details: String(error) },
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
