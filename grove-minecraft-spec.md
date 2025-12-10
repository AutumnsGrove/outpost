# Grove.place Minecraft Infrastructure — Project Spec

## Overview

On-demand Minecraft server with hourly billing, automated lifecycle management, and integration with existing grove.place Cloudflare stack.

**Target**: Minecraft 1.20.1 (Fabric) with friends-only whitelist

---

## Cost Comparison: EU vs US

| Location | Instance | Specs | Hourly | Monthly Cap | 100 hrs Est. | Latency to GA |
|----------|----------|-------|--------|-------------|--------------|---------------|
| **EU (Falkenstein)** | CX33 | 4 vCPU, 8GB, 80GB NVMe | €0.008 (~$0.0085) | €5.49 (~$5.80) | **~$0.85** | ~90-100ms |
| **US (Ashburn)** | CPX31 | 4 vCPU, 8GB, 160GB NVMe | €0.0265 (~$0.028) | €16.49 (~$17.40) | **~$2.80** | ~20-30ms |

**Traffic included**: EU gets 20TB, US gets 3TB (plenty for MC either way)

### Region Toggle

The admin panel includes a **region selector** that lets you choose EU or US at server start time. This allows you to make cost vs latency tradeoffs on a per-session basis:

- **Casual solo building session?** → EU (~$0.0085/hr)
- **Playing with friends, want snappy PvP?** → US (~$0.028/hr)

The region choice is stored with each session for accurate cost tracking.

### Network Approach: Direct Cloudflare DNS

For this small-scale, whitelisted friends server, we use **direct Cloudflare DNS** rather than a DDoS proxy:

- Worker updates the `mc.grove.place` A record via Cloudflare API when VPS starts
- IP changes on each cold boot (ephemeral = harder to target)
- Hetzner includes basic DDoS protection
- No extra monthly costs

**Future expansion**: If scaling to more players or 24/7 uptime, TCPShield Pro ($5/mo) can be added later. See [Future Expansion: TCPShield](#future-expansion-tcpshield-integration) section.

---

## Full Cost Breakdown (100 hrs/month)

| Component | EU Option | US Option |
|-----------|-----------|-----------|
| Hetzner VPS | ~$0.85 | ~$2.80 |
| Cloudflare R2 (~2GB world) | ~$0.03 | ~$0.03 |
| Cloudflare D1 | ~$0.00 | ~$0.00 |
| **Total** | **~$0.88** | **~$2.83** |

*R2: $0.015/GB/month for storage, negligible ops costs*

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              CLOUDFLARE                                      │
│  ┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐       │
│  │   R2: mc-assets  │    │  R2: mc-worlds   │    │   D1: mc-state   │       │
│  │   (mods, config) │    │   (snapshots)    │    │  (logs, stats)   │       │
│  └────────┬─────────┘    └────────┬─────────┘    └────────┬─────────┘       │
│           │                       │                       │                  │
│           └───────────────────────┼───────────────────────┘                  │
│                                   │                                          │
│                    ┌──────────────▼──────────────┐                          │
│                    │     Worker: mc-control      │                          │
│                    │  - Start/stop server        │                          │
│                    │  - Sync files to/from R2    │                          │
│                    │  - Monitor server state     │                          │
│                    │  - Hetzner API calls        │                          │
│                    │  - Cloudflare DNS updates   │                          │
│                    └──────────────┬──────────────┘                          │
│                                   │                                          │
│           ┌───────────────────────┼───────────────────────┐                 │
│           │                       │                       │                  │
│  ┌────────▼─────────┐   ┌────────▼─────────┐   ┌────────▼─────────┐        │
│  │  admin.grove.place│   │  mc.grove.place  │   │ map.grove.place  │        │
│  │  /minecraft tab   │   │  Pages: Status   │   │ (Proxied CDN)    │        │
│  │  + Region toggle  │   │  + Info page     │   │ Dynmap web UI    │        │
│  └───────────────────┘   └────────┬─────────┘   └────────┬─────────┘        │
│                                   │                       │                  │
│                   [DNS: A record updated on boot]         │                  │
│                                   │                       │                  │
└───────────────────────────────────┼───────────────────────┼──────────────────┘
                                    │                       │
                                    ▼                       ▼
                          ┌─────────────────────────────────────┐
                          │         Hetzner Cloud VPS           │
                          │     CX33 (EU) or CPX31 (US)         │
                          │  ┌─────────────────────────────┐    │
                          │  │     Minecraft Server        │    │
                          │  │  - Fabric 1.20.1            │    │
                          │  │  - Java 17                  │    │
                          │  │  - Mods from R2             │    │
                          │  │  - World from R2            │    │
                          │  └─────────────────────────────┘    │
                          │  ┌─────────────────────────────┐    │
                          │  │     Dynmap (port 8123)      │    │
                          │  │  - Exposed via CF tunnel    │    │
                          │  └─────────────────────────────┘    │
                          │  ┌─────────────────────────────┐    │
                          │  │     Watchdog Process        │    │
                          │  │  - Monitor player count     │    │
                          │  │  - Trigger shutdown flow    │    │
                          │  │  - Periodic R2 sync         │    │
                          │  └─────────────────────────────┘    │
                          │                                     │
                          │  Specs: 4 vCPU, 8GB RAM             │
                          │  Basic DDoS protection included     │
                          └─────────────────────────────────────┘
```

---

## mc.grove.place — Public Status Page

A lightweight SvelteKit site on Cloudflare Pages serving as the public face for the Minecraft server.

### Purpose

- Show server status (online/offline/starting)
- Display connection info when online
- Provide basic server info (modpack, version)
- Give something to see when someone visits `mc.grove.place` in a browser

### Implementation

Built using **GroveEngine** scaffolding (SvelteKit + Cloudflare Pages). The agent will have access to the GroveEngine project to explore patterns and conventions.

### Key Features

```
┌─────────────────────────────────────────────────┐
│             mc.grove.place                       │
├─────────────────────────────────────────────────┤
│                                                 │
│        🌿 Grove Minecraft Server 🌿             │
│                                                 │
│    ┌─────────────────────────────────────┐     │
│    │  Status: 🟢 ONLINE                   │     │
│    │  Players: 3/20                       │     │
│    │  Version: 1.20.1 (Fabric)           │     │
│    └─────────────────────────────────────┘     │
│                                                 │
│    Connect: grove.place                         │
│                                                 │
│    [View Live Map →]                           │
│                                                 │
│    ─────────────────────────────────────────   │
│                                                 │
│    This is a private, whitelisted server.      │
│    Contact Autumn for access.                  │
│                                                 │
└─────────────────────────────────────────────────┘
```

### Data Flow

The Pages site fetches status from the mc-control worker API:

```typescript
// In mc.grove.place SvelteKit app
const status = await fetch('https://admin.grove.place/api/mc/status/public');
// Returns: { state, players: { online, max }, version }
```

### Pages Config

- **Build**: SvelteKit adapter-cloudflare
- **Domain**: `mc.grove.place`
- **Functions**: None (static + client-side fetch)

---

## Network Flow (Player Connection)

```
Player's Minecraft Client
         │
         │ Resolves grove.place via SRV
         │ or mc.grove.place via A record
         ▼
┌─────────────────────────┐
│   Cloudflare DNS        │
│   → A record to VPS IP  │
│   (updated on boot)     │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│    Hetzner VPS          │
│  - Direct connection    │
│  - Minecraft processes  │
│    the connection       │
│  - Real IP preserved    │
└─────────────────────────┘
```

**Security note**: IP changes on each cold boot, making the server harder to persistently target. Hetzner includes basic DDoS protection. For a whitelisted friends server, this is sufficient.

---

## Server Lifecycle State Machine

```
                    ┌──────────┐
                    │  OFFLINE │ ◄────────────────────────────┐
                    └────┬─────┘                              │
                         │                                    │
                    [Start Button]                      [TTL Expired]
                         │                                    │
                         ▼                                    │
                  ┌─────────────┐                             │
                  │ PROVISIONING│                             │
                  │  (1-3 min)  │                             │
                  └──────┬──────┘                             │
                         │                                    │
                  [Setup Complete]                            │
                         │                                    │
                         ▼                                    │
                    ┌─────────┐                               │
         ┌─────────►│ RUNNING │◄──────────┐                  │
         │          └────┬────┘           │                   │
         │               │                │                   │
         │        [Last Player Left]      │                   │
         │               │           [Player Joined]          │
         │               ▼                │                   │
         │          ┌─────────┐           │                   │
         │          │  IDLE   ├───────────┘                   │
         │          │ (15 min │                               │
         │          │ timeout)│                               │
         │          └────┬────┘                               │
         │               │                                    │
         │        [Timeout Expired]                           │
         │               │                                    │
         │               ▼                                    │
         │        ┌───────────┐                               │
         │        │ SUSPENDED │                               │
         │        │  (MC off, │                               │
    [Player       │  VPS warm)│                               │
     Joined]      └─────┬─────┘                               │
         │              │                                     │
         │       [45 min timeout]                             │
         │              │                                     │
         │              ▼                                     │
         │       ┌────────────┐                               │
         └───────┤ TERMINATING├───────────────────────────────┘
                 │ (sync + del)│
                 └─────────────┘
```

**State Definitions:**

| State | VPS | MC Process | Billing | Can Rejoin |
|-------|-----|------------|---------|------------|
| OFFLINE | ❌ Deleted | ❌ | ❌ | Cold start (1-3 min) |
| PROVISIONING | 🔄 Creating | 🔄 Starting | ✅ | Wait for ready |
| RUNNING | ✅ | ✅ | ✅ | Instant |
| IDLE | ✅ | ✅ | ✅ | Instant |
| SUSPENDED | ✅ | ❌ Stopped | ✅ | Warm start (~30 sec) |
| TERMINATING | 🔄 Deleting | ❌ | ✅ | Wait for offline |

---

## Hetzner Cloud Details

### Configuration Variables

```typescript
// config.ts
export const HETZNER_CONFIG = {
  // EU Option (budget)
  location: 'fsn1',      // Falkenstein, Germany
  serverType: 'cx33',    // 4 vCPU, 8GB, 80GB
  
  // US Option (performance) - uncomment to switch
  // location: 'ash',     // Ashburn, Virginia
  // serverType: 'cpx31', // 4 vCPU, 8GB, 160GB
};
```

### Instance Comparison

| Spec | CX33 (EU) | CPX31 (US) |
|------|-----------|------------|
| vCPUs | 4 (shared) | 4 (shared) |
| RAM | 8 GB | 8 GB |
| Storage | 80 GB NVMe | 160 GB NVMe |
| Traffic | 20 TB | 3 TB |
| Hourly | €0.008 | €0.0265 |
| Monthly Cap | €5.49 | €16.49 |

---

## DNS Configuration

### Cloudflare DNS Records

| Type | Name | Content | Proxy | TTL | Notes |
|------|------|---------|-------|-----|-------|
| A | mc | `<VPS_IP>` | ❌ DNS only | 60s | Updated by worker on boot |
| SRV | _minecraft._tcp | `0 5 25565 mc.grove.place` | N/A | 300s | Clean `grove.place` connect |
| CNAME | map | *via CF Tunnel* | ✅ Proxied | Auto | Dynmap web UI |

**Player connection addresses:**
- `grove.place` — via SRV record (cleanest)
- `mc.grove.place` — direct A record

### DNS Update Flow (on server start)

```typescript
// In mc-control worker
async function updateDNS(newIp: string, region: string) {
  const zoneId = env.CF_ZONE_ID;
  const recordId = env.CF_MC_RECORD_ID;
  
  // Update A record for mc.grove.place
  await fetch(`https://api.cloudflare.com/client/v4/zones/${zoneId}/dns_records/${recordId}`, {
    method: 'PATCH',
    headers: {
      'Authorization': `Bearer ${env.CF_API_TOKEN}`,
      'Content-Type': 'application/json'
    },
    body: JSON.stringify({
      content: newIp,
      ttl: 60  // Low TTL for quick updates
    })
  });
  
  // Store in D1 for tracking
  await env.DB.prepare(
    'UPDATE server_state SET vps_ip = ?, region = ?, dns_updated_at = ? WHERE id = 1'
  ).bind(newIp, region, new Date().toISOString()).run();
}
```

---

## Future Expansion: TCPShield Integration

> **Status**: Deferred — implement when scaling to more players or 24/7 uptime

For enhanced DDoS protection, TCPShield Pro ($5/month) can be added later. This section documents the integration for future reference.

### Why TCPShield?

- 16 Tbps L4 DDoS mitigation
- L7 bot filtering
- Hides backend IP from players
- Required for larger public servers

### Prerequisites

- TCPShield Pro plan ($5/month) — free tier doesn't have API access
- Hetzner Floating IP ($3/month per region) — static IP for TCPShield backend

### API Integration (when needed)

```typescript
// Future: TCPShield backend update
async function updateTCPShieldBackend(newIp: string) {
  const networkId = env.TCPSHIELD_NETWORK_ID;
  const backendSetId = env.TCPSHIELD_BACKEND_SET_ID;
  
  await fetch(`https://api.tcpshield.com/networks/${networkId}/backendSets/${backendSetId}`, {
    method: 'PATCH',
    headers: {
      'Authorization': `Bearer ${env.TCPSHIELD_API_KEY}`,
      'Content-Type': 'application/json'
    },
    body: JSON.stringify({
      backends: [`${newIp}:25565`]
    })
  });
}
```

### DNS Changes (when enabling)

Replace the direct A record with:

| Type | Name | Content | Notes |
|------|------|---------|-------|
| TXT | _tcpshield | `tcpshield-verify=xxx` | Domain verification |
| CNAME | mc | `yournetwork.tcpshield.com` | Routes through TCPShield |

### Additional Requirements

- Install TCPShield RealIP plugin on server (extracts real player IPs)
- Configure `only-allow-proxy-connections: true` in plugin config

---

## R2 Bucket Structure

### Bucket: `mc-assets` (static, rarely updated)

```
mc-assets/
├── server/
│   ├── fabric-server-launch.jar
│   ├── server.properties
│   ├── ops.json
│   ├── whitelist.json
│   └── eula.txt
├── mods/
│   ├── fabric-api-0.x.x.jar
│   ├── lithium-0.x.x.jar
│   └── ...
├── config/
│   └── ...
├── datapacks/
│   └── ...
└── scripts/
    ├── setup.sh
    ├── start.sh
    ├── stop.sh
    ├── watchdog.sh
    └── sync-to-r2.sh
```

### Bucket: `mc-worlds` (dynamic, updated frequently)

```
mc-worlds/
├── current/
│   └── world.tar.gz
├── backups/
│   ├── world-2025-01-15-1200.tar.gz
│   └── ... (keep last 10)
└── metadata.json
```

---

## Worker API Endpoints

### `POST /api/mc/start`

```typescript
// Request
{ 
  "region": "eu" | "us"  // Required: which region to start in
}

// Response
{
  "status": "provisioning",
  "region": "eu",
  "serverType": "cx33",
  "estimatedReadyTime": "2025-01-15T12:03:00Z",
  "serverId": "12345678",
  "hourlyRate": 0.0085
}
```

### `POST /api/mc/stop`

```typescript
// Request
{ "force": false }

// Response
{
  "status": "terminating",
  "message": "World sync in progress"
}
```

### `GET /api/mc/status`

```typescript
// Response
{
  "state": "RUNNING",  // OFFLINE | PROVISIONING | RUNNING | IDLE | SUSPENDED | TERMINATING
  "region": "eu",
  "serverType": "cx33",
  "serverId": "12345678",
  "serverIp": "1.2.3.4",
  "players": {
    "online": 3,
    "max": 20,
    "list": ["Autumn", "Friend1", "Friend2"]
  },
  "uptime": 7200,
  "idleTime": 0,
  "ttl": null,
  "lastWorldSync": "2025-01-15T11:30:00Z",
  "costs": {
    "currentSession": 0.03,
    "hourlyRate": 0.0085,
    "thisMonth": 0.45,
    "thisMonthByRegion": {
      "eu": 0.35,
      "us": 0.10
    }
  }
}
```

### `GET /api/mc/status/public`

Public endpoint for mc.grove.place (no auth required, limited data):

```typescript
{
  "state": "RUNNING",
  "players": { "online": 3, "max": 20 },
  "version": "1.20.1"
}
```

### `POST /api/mc/whitelist`

```typescript
// Request
{ 
  "action": "add" | "remove",
  "username": "NewPlayer"
}

// Response
{
  "success": true,
  "whitelist": ["Autumn", "Friend1", "Friend2", "NewPlayer"]
}
```

### `GET /api/mc/whitelist`

```typescript
// Response
{
  "whitelist": [
    { "name": "Autumn", "uuid": "xxx" },
    { "name": "Friend1", "uuid": "xxx" }
  ]
}
```

### Other Endpoints

| Method | Endpoint | Purpose |
|--------|----------|---------|
| POST | `/api/mc/command` | Send console command |
| POST | `/api/mc/sync` | Manual world backup |
| WS | `/api/mc/console` | Console stream |
| GET | `/api/mc/history` | Session history for stats |

---

## Admin Dashboard Widgets

### Minecraft Tab Layout

```
┌─────────────────────────────────────────────────────────────────────────┐
│  🎮 Minecraft Server                    [EU ▾] [US]    [🟢 Online ▾]    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌─────────────────────┐  ┌─────────────────────┐  ┌─────────────────┐ │
│  │ Server Status       │  │ Players Online      │  │ Time to Live    │ │
│  │ ━━━━━━━━━━━━━━━━━━━ │  │ ━━━━━━━━━━━━━━━━━━━ │  │ ━━━━━━━━━━━━━━━ │ │
│  │                     │  │                     │  │                 │ │
│  │  🟢 RUNNING         │  │  👤 3 / 20          │  │  ∞ (players on) │ │
│  │  Region: 🇪🇺 EU     │  │                     │  │                 │ │
│  │  Uptime: 2h 15m     │  │  • Autumn           │  │  Auto-shutdown  │ │
│  │  Version: 1.20.1    │  │  • Friend1          │  │  disabled while │ │
│  │  Connect:           │  │  • Friend2          │  │  players online │ │
│  │  grove.place        │  │                     │  │                 │ │
│  └─────────────────────┘  └─────────────────────┘  └─────────────────┘ │
│                                                                         │
│  ┌─────────────────────┐  ┌─────────────────────┐  ┌─────────────────┐ │
│  │ Session Cost        │  │ Storage             │  │ Last Backup     │ │
│  │ ━━━━━━━━━━━━━━━━━━━ │  │ ━━━━━━━━━━━━━━━━━━━ │  │ ━━━━━━━━━━━━━━━ │ │
│  │                     │  │                     │  │                 │ │
│  │  💰 $0.02           │  │  📦 Assets: 245 MB  │  │  ✅ 12 min ago  │ │
│  │  Region: EU         │  │  🌍 World: 1.2 GB   │  │                 │ │
│  │  This month: $0.45  │  │                     │  │  [Backup Now]   │ │
│  │  Rate: $0.0085/hr   │  │  Total: 1.45 GB     │  │                 │ │
│  └─────────────────────┘  └─────────────────────┘  └─────────────────┘ │
│                                                                         │
│  ┌─────────────────────────────────┐  ┌─────────────────────────────┐  │
│  │ Whitelist Management            │  │ Usage History               │  │
│  │ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ │  │ ━━━━━━━━━━━━━━━━━━━━━━━━━━━ │  │
│  │                                 │  │                             │  │
│  │  Current whitelist:             │  │  This month: 12.5 hrs       │  │
│  │  • Autumn (op)                  │  │    EU: 10.2 hrs ($0.09)     │  │
│  │  • Friend1                      │  │    US: 2.3 hrs ($0.06)      │  │
│  │  • Friend2                      │  │  Last month: 45.2 hrs       │  │
│  │                                 │  │  Total sessions: 8          │  │
│  │  ┌─────────────────────┐       │  │                             │  │
│  │  │ Add player...       │ [Add] │  │  [View Full History]        │  │
│  │  └─────────────────────┘       │  │                             │  │
│  └─────────────────────────────────┘  └─────────────────────────────┘  │
│                                                                         │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │ Live Map                                               [Expand ↗]│  │
│  │ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ │  │
│  │  ┌─────────────────────────────────────────────────────────────┐ │  │
│  │  │                    [Dynmap iframe]                          │ │  │
│  │  │                    map.grove.place                          │ │  │
│  │  └─────────────────────────────────────────────────────────────┘ │  │
│  └──────────────────────────────────────────────────────────────────┘  │
│                                                                         │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │ Console                                                          │  │
│  │ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ │  │
│  │ [12:15:23] [Server thread/INFO]: Autumn joined the game         │  │
│  │ [12:15:45] [Server thread/INFO]: Friend1 joined the game        │  │
│  │ [12:16:02] [Server thread/INFO]: <Autumn> hey!                  │  │
│  │                                                                   │  │
│  │ > [Enter command...]                                    [Send]   │  │
│  └──────────────────────────────────────────────────────────────────┘  │
│                                                                         │
│                                                      [Stop Server]      │
└─────────────────────────────────────────────────────────────────────────┘
```

### Dynamic Start/Status Button (Top Right)

The button in the header changes based on server state:

| State | Button Text | Button Style | Action |
|-------|-------------|--------------|--------|
| OFFLINE | `Start Server` | Primary (green) | Triggers start |
| PROVISIONING | `Starting...` | Disabled + spinner | None |
| RUNNING | `🟢 Online ▾` | Success dropdown | Shows stop options |
| IDLE | `🟡 Idle ▾` | Warning dropdown | Shows stop options |
| SUSPENDED | `🟠 Suspended ▾` | Warning dropdown | Wake or stop |
| TERMINATING | `Stopping...` | Disabled + spinner | None |

**Dropdown menu (when online/idle/suspended):**
- View Status
- Stop Server (graceful)
- Force Kill (emergency)

### Region Toggle (Header)

Toggle buttons in the header: `[EU ▾] [US]` or `[EU] [US ▾]`

| Server State | Toggle Behavior |
|--------------|-----------------|
| OFFLINE | Selectable — choosing a region and clicking Start uses that region |
| PROVISIONING | Locked — shows which region is being provisioned |
| RUNNING/IDLE/SUSPENDED | Locked — shows current region (can't change mid-session) |
| TERMINATING | Locked — shows region being terminated |

**Visual states:**
- Selected region: Filled/highlighted button with checkmark or dropdown arrow
- Unselected region: Outline/ghost button
- Locked: Both buttons show current region, unselected is disabled

**Cost indicator**: When OFFLINE and hovering/selecting a region, show the hourly rate:
- EU: `$0.0085/hr`
- US: `$0.028/hr`

### Whitelist Widget

```typescript
// Component behavior
interface WhitelistWidget {
  // Display current whitelist
  players: WhitelistEntry[];
  
  // Add player
  onAdd: (username: string) => Promise<void>;
  
  // Remove player (with confirmation)
  onRemove: (username: string) => Promise<void>;
  
  // Shows loading state during operations
  isLoading: boolean;
}
```

---

## D1 Database Schema

```sql
-- Server state tracking
CREATE TABLE server_state (
    id INTEGER PRIMARY KEY DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'OFFLINE',
    vps_id TEXT,
    vps_ip TEXT,
    region TEXT,              -- 'eu' or 'us'
    started_at TEXT,
    last_heartbeat TEXT,
    dns_updated_at TEXT
);

-- Session history (for usage stats widget)
CREATE TABLE sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    duration_seconds INTEGER,
    cost_usd REAL,
    max_players INTEGER,
    world_size_bytes INTEGER,
    region TEXT,           -- 'eu' or 'us'
    server_type TEXT       -- 'cx33' or 'cpx31'
);

-- Monthly aggregates
CREATE TABLE monthly_summary (
    month TEXT PRIMARY KEY,  -- "2025-01"
    total_hours REAL,
    total_cost REAL,
    session_count INTEGER
);

-- Whitelist cache (source of truth is server, this is for UI)
CREATE TABLE whitelist_cache (
    username TEXT PRIMARY KEY,
    uuid TEXT,
    added_at TEXT,
    added_by TEXT
);
```

---

## VPS Setup Script (cloud-init)

```yaml
#cloud-config

package_update: true
package_upgrade: true

packages:
  - openjdk-17-jdk-headless   # Java 17 for MC 1.20.1
  - rclone
  - jq
  - tmux
  - screen

write_files:
  # Rclone config for R2
  - path: /root/.config/rclone/rclone.conf
    content: |
      [r2]
      type = s3
      provider = Cloudflare
      access_key_id = ${R2_ACCESS_KEY}
      secret_access_key = ${R2_SECRET_KEY}
      endpoint = https://${CF_ACCOUNT_ID}.r2.cloudflarestorage.com
      acl = private

  # Systemd service for Minecraft
  - path: /etc/systemd/system/minecraft.service
    content: |
      [Unit]
      Description=Minecraft Server
      After=network.target

      [Service]
      User=minecraft
      WorkingDirectory=/opt/minecraft
      ExecStart=/opt/minecraft/start.sh
      ExecStop=/opt/minecraft/stop.sh
      Restart=on-failure
      RestartSec=5

      [Install]
      WantedBy=multi-user.target

  # Systemd service for Watchdog
  - path: /etc/systemd/system/mc-watchdog.service
    content: |
      [Unit]
      Description=Minecraft Watchdog
      After=minecraft.service

      [Service]
      User=minecraft
      ExecStart=/opt/minecraft/watchdog.sh
      Restart=always

      [Install]
      WantedBy=multi-user.target

runcmd:
  # Create minecraft user
  - useradd -m -s /bin/bash minecraft
  - mkdir -p /opt/minecraft
  - chown minecraft:minecraft /opt/minecraft

  # Download assets from R2
  - rclone sync r2:mc-assets/server/ /opt/minecraft/
  - rclone sync r2:mc-assets/mods/ /opt/minecraft/mods/
  - rclone sync r2:mc-assets/config/ /opt/minecraft/config/
  - rclone sync r2:mc-assets/scripts/ /opt/minecraft/

  # Download world from R2
  - rclone copy r2:mc-worlds/current/world.tar.gz /tmp/
  - |
    if [ -f /tmp/world.tar.gz ]; then
      tar -xzf /tmp/world.tar.gz -C /opt/minecraft/
      rm /tmp/world.tar.gz
    fi

  # Set permissions
  - chown -R minecraft:minecraft /opt/minecraft
  - chmod +x /opt/minecraft/*.sh

  # Install cloudflared for Dynmap tunnel
  - curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared
  - chmod +x /usr/local/bin/cloudflared
  # Tunnel config injected via secrets

  # Start services
  - systemctl daemon-reload
  - systemctl enable minecraft mc-watchdog
  - systemctl start minecraft mc-watchdog

  # Notify worker that server is ready (includes IP for DNS update)
  - |
    VPS_IP=$(curl -s http://169.254.169.254/hetzner/v1/metadata/public-ipv4)
    curl -X POST "https://admin.grove.place/api/mc/webhook/ready" \
      -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
      -H "Content-Type: application/json" \
      -d "{\"serverId\": \"$(curl -s http://169.254.169.254/hetzner/v1/metadata/instance-id)\", \"ip\": \"$VPS_IP\"}"
```

---

## Watchdog Script

```bash
#!/bin/bash
# /opt/minecraft/watchdog.sh

IDLE_TIMEOUT=900        # 15 minutes
SUSPEND_TIMEOUT=2700    # 45 minutes
BACKUP_INTERVAL=1800    # 30 minutes
WEBHOOK_URL="https://admin.grove.place/api/mc/webhook"
WEBHOOK_SECRET="${WEBHOOK_SECRET}"

last_player_time=$(date +%s)
last_backup_time=$(date +%s)
state="running"

get_player_count() {
    screen -S minecraft -p 0 -X stuff "list\n"
    sleep 0.5
    grep -oP 'There are \K[0-9]+' /opt/minecraft/logs/latest.log | tail -1
}

sync_world_to_r2() {
    echo "$(date): Syncing world to R2..."
    
    screen -S minecraft -p 0 -X stuff "save-off\n"
    screen -S minecraft -p 0 -X stuff "save-all\n"
    sleep 5
    
    tar -czf /tmp/world.tar.gz -C /opt/minecraft world
    rclone copy /tmp/world.tar.gz r2:mc-worlds/current/
    
    timestamp=$(date +%Y-%m-%d-%H%M)
    rclone copy /tmp/world.tar.gz r2:mc-worlds/backups/world-${timestamp}.tar.gz
    
    # Keep last 10 backups
    rclone delete r2:mc-worlds/backups/ --min-age 7d
    
    rm /tmp/world.tar.gz
    screen -S minecraft -p 0 -X stuff "save-on\n"
    
    curl -s -X POST "${WEBHOOK_URL}/backup-complete" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d '{"timestamp": "'$(date -Iseconds)'"}'
    
    last_backup_time=$(date +%s)
}

notify_state_change() {
    curl -s -X POST "${WEBHOOK_URL}/state-change" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d '{"state": "'$1'", "timestamp": "'$(date -Iseconds)'"}'
}

while true; do
    current_time=$(date +%s)
    player_count=$(get_player_count)
    
    if [ "$player_count" -gt 0 ]; then
        last_player_time=$current_time
        if [ "$state" != "running" ]; then
            state="running"
            notify_state_change "RUNNING"
        fi
    fi
    
    idle_duration=$((current_time - last_player_time))
    backup_duration=$((current_time - last_backup_time))
    
    if [ "$backup_duration" -ge "$BACKUP_INTERVAL" ] && [ "$state" = "running" ]; then
        sync_world_to_r2
    fi
    
    case $state in
        running)
            if [ "$player_count" -eq 0 ] && [ "$idle_duration" -ge "$IDLE_TIMEOUT" ]; then
                echo "$(date): No players for ${IDLE_TIMEOUT}s, suspending"
                state="suspended"
                notify_state_change "SUSPENDED"
                sync_world_to_r2
                systemctl stop minecraft
                suspend_start_time=$current_time
            fi
            ;;
        suspended)
            suspend_duration=$((current_time - suspend_start_time))
            if [ "$suspend_duration" -ge "$SUSPEND_TIMEOUT" ]; then
                echo "$(date): Suspended for ${SUSPEND_TIMEOUT}s, terminating"
                notify_state_change "TERMINATING"
                sleep 60
            fi
            ;;
    esac
    
    curl -s -X POST "${WEBHOOK_URL}/heartbeat" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d '{
            "state": "'$state'",
            "players": '$player_count',
            "idleSeconds": '$idle_duration',
            "lastBackup": "'$(date -d @$last_backup_time -Iseconds)'"
        }'
    
    sleep 30
done
```

---

## Implementation Checklist

### Phase 1: Cloudflare Setup
- [ ] Create R2 bucket: `mc-assets`
- [ ] Create R2 bucket: `mc-worlds`
- [ ] Create D1 database: `mc-state`
- [ ] Generate R2 API credentials
- [ ] Create Cloudflare Tunnel for Dynmap
- [ ] Create A record for `mc.grove.place` (initial placeholder)
- [ ] Create SRV record for `grove.place` connection
- [ ] Note Zone ID and create API token for DNS updates

### Phase 2: Prepare Assets
- [ ] Download Fabric server JAR for **1.20.1**
- [ ] Collect all mods
- [ ] Create `server.properties`
- [ ] Create `ops.json`
- [ ] Create `whitelist.json`
- [ ] Create `eula.txt` with `eula=true`
- [ ] Upload to `mc-assets` bucket

### Phase 3: Hetzner Setup
- [ ] Create Hetzner Cloud account
- [ ] Generate API token
- [ ] Add SSH key
- [ ] Test API with both regions (EU + US)

### Phase 4: mc.grove.place Pages Site
- [ ] Create SvelteKit project with GroveEngine
- [ ] Build status display page
- [ ] Configure Cloudflare Pages deployment
- [ ] Set up `mc.grove.place` custom domain

### Phase 5: Worker Development
- [ ] Create `mc-control` worker
- [ ] `/api/mc/start` — provisions VPS (with region param)
- [ ] `/api/mc/stop` — graceful shutdown
- [ ] `/api/mc/status` — full status
- [ ] `/api/mc/status/public` — public status for Pages site
- [ ] `/api/mc/whitelist` — add/remove/list
- [ ] `/api/mc/command` — send command
- [ ] `/api/mc/sync` — manual backup
- [ ] `/api/mc/history` — session history
- [ ] Webhook handlers (ready, heartbeat, state-change, backup-complete)
- [ ] **Cloudflare DNS update on server ready**
- [ ] Authentication middleware

### Phase 6: VPS Scripts
- [ ] Write cloud-init YAML
- [ ] Write `start.sh`
- [ ] Write `stop.sh`
- [ ] Write `watchdog.sh`
- [ ] Write `sync-to-r2.sh`
- [ ] Test on throwaway Hetzner server

### Phase 7: Admin Dashboard
- [ ] Add Minecraft tab to admin.grove.place
- [ ] **Region toggle (EU/US)**
- [ ] Dynamic Start/Status button (header)
- [ ] Server status widget
- [ ] Player list widget
- [ ] TTL countdown widget
- [ ] Session cost widget (shows rate based on region)
- [ ] Storage widget
- [ ] Backup widget
- [ ] **Whitelist management widget**
- [ ] **Usage history widget**
- [ ] Console widget (WebSocket)
- [ ] Dynmap embed

### Phase 8: Testing
- [ ] Full start cycle (EU region)
- [ ] Full start cycle (US region)
- [ ] Region toggle between sessions
- [ ] DNS update on boot
- [ ] Player join/leave detection
- [ ] Idle → suspended transition
- [ ] Suspended → terminated transition
- [ ] World backup/restore
- [ ] Warm restart from suspended
- [ ] Cold start from offline
- [ ] Whitelist add/remove
- [ ] Cost tracking accuracy (per region)

### Phase 9: Polish
- [ ] Error handling and retries
- [ ] Loading states
- [ ] Discord webhook notifications (optional)
- [ ] Documentation

### Future: TCPShield Integration (when needed)
- [ ] Upgrade to TCPShield Pro ($5/month)
- [ ] Get Hetzner Floating IP ($3/month per region)
- [ ] Configure TCPShield network and backend
- [ ] Update DNS to CNAME → TCPShield
- [ ] Install RealIP plugin on server
- [ ] Update worker to call TCPShield API

---

## Quick Reference

| Action | Method | Endpoint |
|--------|--------|----------|
| Start server | POST | `/api/mc/start` |
| Stop server | POST | `/api/mc/stop` |
| Get status | GET | `/api/mc/status` |
| Public status | GET | `/api/mc/status/public` |
| Manage whitelist | GET/POST | `/api/mc/whitelist` |
| Send command | POST | `/api/mc/command` |
| Manual backup | POST | `/api/mc/sync` |
| Session history | GET | `/api/mc/history` |
| Console stream | WS | `/api/mc/console` |

| State | Duration | What Happens |
|-------|----------|--------------|
| IDLE | 15 min | Timer starts when last player leaves |
| SUSPENDED | 45 min | MC process stopped, VPS still running |
| TERMINATED | Immediate | World synced, VPS deleted |

---

## Monthly Cost Scenarios

### EU Option (CX33)

| Usage | Hetzner | R2 (~2GB) | Total |
|-------|---------|-----------|-------|
| 25 hrs | ~$0.21 | ~$0.03 | **~$0.24** |
| 50 hrs | ~$0.43 | ~$0.03 | **~$0.46** |
| 100 hrs | ~$0.85 | ~$0.03 | **~$0.88** |
| 200 hrs | ~$1.70 | ~$0.03 | **~$1.73** |
| 720 hrs (24/7) | ~$5.80 (cap) | ~$0.03 | **~$5.83** |

### US Option (CPX31)

| Usage | Hetzner | R2 (~2GB) | Total |
|-------|---------|-----------|-------|
| 25 hrs | ~$0.70 | ~$0.03 | **~$0.73** |
| 50 hrs | ~$1.40 | ~$0.03 | **~$1.43** |
| 100 hrs | ~$2.80 | ~$0.03 | **~$2.83** |
| 200 hrs | ~$5.60 | ~$0.03 | **~$5.63** |
| 720 hrs (24/7) | ~$17.40 (cap) | ~$0.03 | **~$17.43** |
