/**
 * Hetzner Cloud API Service
 * Handles VPS provisioning and management
 */

import type { Env, Region } from '../types/env.js';

const HETZNER_API_BASE = 'https://api.hetzner.cloud/v1';

// Server configurations by region
export const HETZNER_CONFIG = {
  eu: {
    location: 'fsn1',      // Falkenstein, Germany
    serverType: 'cx33',    // 4 vCPU, 8GB RAM, 80GB NVMe
    hourlyRate: 0.0085,
  },
  us: {
    location: 'ash',       // Ashburn, Virginia
    serverType: 'cpx31',   // 4 vCPU, 8GB RAM, 160GB NVMe
    hourlyRate: 0.028,
  },
} as const;

export interface HetznerServer {
  id: number;
  name: string;
  status: 'initializing' | 'starting' | 'running' | 'stopping' | 'off' | 'deleting' | 'rebuilding' | 'migrating' | 'unknown';
  public_net: {
    ipv4: {
      ip: string;
    };
    ipv6: {
      ip: string;
    };
  };
  server_type: {
    name: string;
    description: string;
  };
  datacenter: {
    name: string;
    location: {
      name: string;
      city: string;
      country: string;
    };
  };
  created: string;
}

export interface CreateServerResponse {
  server: HetznerServer;
  action: {
    id: number;
    status: string;
  };
  root_password: string | null;
}

export interface HetznerError {
  code: string;
  message: string;
}

async function hetznerRequest<T>(
  env: Env,
  method: string,
  endpoint: string,
  body?: unknown
): Promise<T> {
  const url = `${HETZNER_API_BASE}${endpoint}`;

  const response = await fetch(url, {
    method,
    headers: {
      'Authorization': `Bearer ${env.HETZNER_API_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: body ? JSON.stringify(body) : undefined,
  });

  if (!response.ok) {
    const error = await response.json().catch(() => ({})) as { error?: HetznerError };
    throw new Error(
      `Hetzner API error: ${response.status} ${error.error?.message || response.statusText}`
    );
  }

  // Handle 204 No Content
  if (response.status === 204) {
    return {} as T;
  }

  return response.json() as Promise<T>;
}

/**
 * Create a new Hetzner server for Minecraft
 */
export async function createServer(
  env: Env,
  region: Region,
  cloudInitYaml: string
): Promise<{ id: string; ip: string; serverType: string }> {
  const config = HETZNER_CONFIG[region];
  const serverName = `grovemc-${region}-${Date.now()}`;

  const response = await hetznerRequest<CreateServerResponse>(env, 'POST', '/servers', {
    name: serverName,
    server_type: config.serverType,
    location: config.location,
    image: 'ubuntu-24.04',
    ssh_keys: [env.HETZNER_SSH_KEY_ID],
    user_data: cloudInitYaml,
    labels: {
      project: 'grovemc',
      region: region,
      managed: 'true',
    },
    start_after_create: true,
  });

  return {
    id: String(response.server.id),
    ip: response.server.public_net.ipv4.ip,
    serverType: config.serverType,
  };
}

/**
 * Delete a Hetzner server
 */
export async function deleteServer(env: Env, serverId: string): Promise<void> {
  await hetznerRequest(env, 'DELETE', `/servers/${serverId}`);
}

/**
 * Get server information
 */
export async function getServer(
  env: Env,
  serverId: string
): Promise<HetznerServer | null> {
  try {
    const response = await hetznerRequest<{ server: HetznerServer }>(
      env,
      'GET',
      `/servers/${serverId}`
    );
    return response.server;
  } catch (error) {
    // Server not found
    if (error instanceof Error && error.message.includes('404')) {
      return null;
    }
    throw error;
  }
}

/**
 * List all GroveMC servers
 */
export async function listServers(env: Env): Promise<HetznerServer[]> {
  const response = await hetznerRequest<{ servers: HetznerServer[] }>(
    env,
    'GET',
    '/servers?label_selector=project=grovemc'
  );
  return response.servers;
}

/**
 * Power on a server
 */
export async function powerOnServer(env: Env, serverId: string): Promise<void> {
  await hetznerRequest(env, 'POST', `/servers/${serverId}/actions/poweron`);
}

/**
 * Power off a server (hard shutdown)
 */
export async function powerOffServer(env: Env, serverId: string): Promise<void> {
  await hetznerRequest(env, 'POST', `/servers/${serverId}/actions/poweroff`);
}

/**
 * Graceful shutdown via ACPI
 */
export async function shutdownServer(env: Env, serverId: string): Promise<void> {
  await hetznerRequest(env, 'POST', `/servers/${serverId}/actions/shutdown`);
}

/**
 * Reboot a server
 */
export async function rebootServer(env: Env, serverId: string): Promise<void> {
  await hetznerRequest(env, 'POST', `/servers/${serverId}/actions/reboot`);
}

/**
 * Get server metrics
 */
export interface ServerMetrics {
  cpu: number;
  disk_read: number;
  disk_write: number;
  network_in: number;
  network_out: number;
}

export async function getServerMetrics(
  env: Env,
  serverId: string
): Promise<ServerMetrics | null> {
  try {
    const end = new Date().toISOString();
    const start = new Date(Date.now() - 5 * 60 * 1000).toISOString(); // Last 5 minutes

    const response = await hetznerRequest<{
      metrics: {
        time_series: {
          cpu: { values: [string, string][] };
          disk_read?: { values: [string, string][] };
          disk_write?: { values: [string, string][] };
          network_in?: { values: [string, string][] };
          network_out?: { values: [string, string][] };
        };
      };
    }>(env, 'GET', `/servers/${serverId}/metrics?type=cpu,disk,network&start=${start}&end=${end}`);

    const ts = response.metrics.time_series;
    const getLatestValue = (series?: { values: [string, string][] }) =>
      series?.values?.length ? parseFloat(series.values[series.values.length - 1][1]) : 0;

    return {
      cpu: getLatestValue(ts.cpu),
      disk_read: getLatestValue(ts.disk_read),
      disk_write: getLatestValue(ts.disk_write),
      network_in: getLatestValue(ts.network_in),
      network_out: getLatestValue(ts.network_out),
    };
  } catch {
    return null;
  }
}

/**
 * Wait for server to reach a specific status
 */
export async function waitForServerStatus(
  env: Env,
  serverId: string,
  targetStatus: HetznerServer['status'],
  timeoutMs = 300000, // 5 minutes
  pollIntervalMs = 5000
): Promise<HetznerServer> {
  const startTime = Date.now();

  while (Date.now() - startTime < timeoutMs) {
    const server = await getServer(env, serverId);

    if (!server) {
      throw new Error(`Server ${serverId} not found`);
    }

    if (server.status === targetStatus) {
      return server;
    }

    await new Promise(resolve => setTimeout(resolve, pollIntervalMs));
  }

  throw new Error(`Timeout waiting for server ${serverId} to reach status ${targetStatus}`);
}

/**
 * Get hourly rate for a region
 */
export function getHourlyRate(region: Region): number {
  return HETZNER_CONFIG[region].hourlyRate;
}

/**
 * Get server type for a region
 */
export function getServerType(region: Region): string {
  return HETZNER_CONFIG[region].serverType;
}
