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
