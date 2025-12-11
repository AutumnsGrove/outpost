/**
 * Worker environment bindings
 */
export interface Env {
  // R2 Buckets
  MC_ASSETS: R2Bucket;
  MC_WORLDS: R2Bucket;

  // D1 Database
  DB: D1Database;

  // Environment variables
  ENVIRONMENT: string;

  // Secrets (set via wrangler secret put)
  CF_ACCOUNT_ID: string;
  CF_API_TOKEN: string;
  CF_ZONE_ID: string;
  CF_MC_RECORD_ID: string;
  HETZNER_API_TOKEN: string;
  HETZNER_SSH_KEY_ID: string;
  WEBHOOK_SECRET: string;
  ADMIN_AUTH_SECRET: string;
  R2_ACCESS_KEY: string;
  R2_SECRET_KEY: string;
  CF_TUNNEL_TOKEN?: string;  // Optional: For Dynmap tunnel
  GROVEAUTH_PUBLIC_KEY?: string;  // Optional: For JWT verification
}

/**
 * Server state enum matching D1 schema
 */
export type ServerState =
  | 'OFFLINE'
  | 'PROVISIONING'
  | 'RUNNING'
  | 'IDLE'
  | 'SUSPENDED'
  | 'TERMINATING';

/**
 * Region options
 */
export type Region = 'eu' | 'us';

/**
 * Hetzner server types by region
 */
export const HETZNER_CONFIG = {
  eu: {
    location: 'fsn1', // Falkenstein, Germany
    serverType: 'cx33', // 4 vCPU, 8GB, 80GB
    hourlyRate: 0.0085,
  },
  us: {
    location: 'ash', // Ashburn, Virginia
    serverType: 'cpx31', // 4 vCPU, 8GB, 160GB
    hourlyRate: 0.028,
  },
} as const;

/**
 * Admin emails allowed to control the server
 */
export const ADMIN_EMAILS = ['autumn@grove.place', 'autumnbrown23@pm.me'];

/**
 * API Response types
 */
export interface ApiResponse<T = unknown> {
  success: boolean;
  data?: T;
  error?: string;
  error_description?: string;
}

export interface StatusResponse {
  state: ServerState;
  region: Region | null;
  serverType: string | null;
  serverId: string | null;
  serverIp: string | null;
  players: {
    online: number;
    max: number;
    list?: string[];
  };
  uptime: number | null;
  idleTime: number | null;
  ttl: number | null;
  lastWorldSync: string | null;
  costs: {
    currentSession: number;
    hourlyRate: number;
    thisMonth: number;
    thisMonthByRegion: {
      eu: number;
      us: number;
    };
  };
}

export interface PublicStatusResponse {
  state: ServerState;
  players: {
    online: number;
    max: number;
  };
  version: string;
}

export interface StartRequest {
  region: Region;
}

export interface StartResponse {
  status: 'provisioning';
  region: Region;
  serverType: string;
  estimatedReadyTime: string;
  serverId: string;
  hourlyRate: number;
}

export interface StopRequest {
  force?: boolean;
}

export interface WhitelistEntry {
  name: string;
  uuid?: string;
  added_at?: string;
  added_by?: string;
}

export interface WebhookPayload {
  serverId?: string;
  ip?: string;
  region?: string;
  state?: ServerState;
  players?: number;
  idleSeconds?: number;
  lastBackup?: string;
  timestamp?: string;
  sizeBytes?: number;
}
