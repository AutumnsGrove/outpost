/**
 * Cloudflare DNS Service
 * Handles dynamic DNS updates for mc.grove.place
 */

import type { Env } from '../types/env.js';

const CLOUDFLARE_API_BASE = 'https://api.cloudflare.com/client/v4';

interface CloudflareResponse<T> {
  success: boolean;
  errors: Array<{ code: number; message: string }>;
  messages: string[];
  result: T;
}

interface DnsRecord {
  id: string;
  type: string;
  name: string;
  content: string;
  ttl: number;
  proxied: boolean;
  created_on: string;
  modified_on: string;
}

async function cloudflareRequest<T>(
  env: Env,
  method: string,
  endpoint: string,
  body?: unknown
): Promise<T> {
  const url = `${CLOUDFLARE_API_BASE}${endpoint}`;

  const response = await fetch(url, {
    method,
    headers: {
      'Authorization': `Bearer ${env.CF_API_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: body ? JSON.stringify(body) : undefined,
  });

  const data = await response.json() as CloudflareResponse<T>;

  if (!data.success) {
    const errorMessage = data.errors.map(e => e.message).join(', ');
    throw new Error(`Cloudflare API error: ${errorMessage}`);
  }

  return data.result;
}

/**
 * Update the mc.grove.place A record with a new IP address
 */
export async function updateMcRecord(env: Env, ip: string): Promise<void> {
  await cloudflareRequest<DnsRecord>(
    env,
    'PATCH',
    `/zones/${env.CF_ZONE_ID}/dns_records/${env.CF_MC_RECORD_ID}`,
    {
      type: 'A',
      name: 'mc',
      content: ip,
      ttl: 60,  // Low TTL for quick updates
      proxied: false,  // DNS only, no Cloudflare proxy for Minecraft
    }
  );
}

/**
 * Get the current IP address of mc.grove.place
 */
export async function getMcRecordIp(env: Env): Promise<string | null> {
  try {
    const record = await cloudflareRequest<DnsRecord>(
      env,
      'GET',
      `/zones/${env.CF_ZONE_ID}/dns_records/${env.CF_MC_RECORD_ID}`
    );
    return record.content;
  } catch {
    return null;
  }
}

/**
 * Get all DNS records for the zone (useful for debugging)
 */
export async function listDnsRecords(env: Env): Promise<DnsRecord[]> {
  const records = await cloudflareRequest<DnsRecord[]>(
    env,
    'GET',
    `/zones/${env.CF_ZONE_ID}/dns_records`
  );
  return records;
}

/**
 * Get DNS record by name
 */
export async function getDnsRecordByName(
  env: Env,
  name: string
): Promise<DnsRecord | null> {
  const records = await cloudflareRequest<DnsRecord[]>(
    env,
    'GET',
    `/zones/${env.CF_ZONE_ID}/dns_records?name=${encodeURIComponent(name)}`
  );
  return records.length > 0 ? records[0] : null;
}

/**
 * Create a new DNS record
 */
export async function createDnsRecord(
  env: Env,
  type: string,
  name: string,
  content: string,
  options?: {
    ttl?: number;
    proxied?: boolean;
    priority?: number;
  }
): Promise<DnsRecord> {
  return cloudflareRequest<DnsRecord>(
    env,
    'POST',
    `/zones/${env.CF_ZONE_ID}/dns_records`,
    {
      type,
      name,
      content,
      ttl: options?.ttl ?? 300,
      proxied: options?.proxied ?? false,
      priority: options?.priority,
    }
  );
}

/**
 * Delete a DNS record
 */
export async function deleteDnsRecord(env: Env, recordId: string): Promise<void> {
  await cloudflareRequest<{ id: string }>(
    env,
    'DELETE',
    `/zones/${env.CF_ZONE_ID}/dns_records/${recordId}`
  );
}

/**
 * Purge Cloudflare cache for the zone (optional, for web assets)
 */
export async function purgeCache(env: Env, urls?: string[]): Promise<void> {
  const body = urls ? { files: urls } : { purge_everything: true };

  await cloudflareRequest<{ id: string }>(
    env,
    'POST',
    `/zones/${env.CF_ZONE_ID}/purge_cache`,
    body
  );
}
