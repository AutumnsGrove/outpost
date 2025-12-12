# Outpost - On-Demand Minecraft Server

> **Internal codename:** GroveMC

On-demand Minecraft 1.20.1 (Fabric) server with hourly billing, automated lifecycle management, and Cloudflare integration. An outpost is where you gather at the edge of wilderness.

## Overview

Outpost provides a cost-effective way to run a Minecraft server for friends. Instead of paying for 24/7 hosting, the server spins up on-demand and automatically shuts down when idle.

**Key Features:**
- Region toggle (EU vs US) for cost/latency tradeoffs
- Automatic shutdown after 15min idle + 45min suspended
- World backups to Cloudflare R2
- Cost tracking per session and region
- Whitelist management via admin panel

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         CLOUDFLARE                               │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │ R2: mc-assets│  │ R2: mc-worlds│  │ D1: mc-state │          │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘          │
│         └─────────────────┼─────────────────┘                   │
│                           │                                      │
│              ┌────────────▼────────────┐                        │
│              │   Worker: mc-control    │                        │
│              │  - Start/stop server    │                        │
│              │  - Hetzner API calls    │                        │
│              │  - DNS updates          │                        │
│              └────────────┬────────────┘                        │
│                           │                                      │
│    ┌──────────────────────┼──────────────────────┐              │
│    │                      │                      │               │
│    ▼                      ▼                      ▼               │
│ admin.grove.place    mc.grove.place      map.grove.place        │
│ (Minecraft tab)      (Status page)       (Dynmap)               │
└───────────────────────────┼──────────────────────────────────────┘
                            │
                            ▼
               ┌─────────────────────────┐
               │    Hetzner Cloud VPS    │
               │  - Minecraft Server     │
               │  - Watchdog Process     │
               │  - Dynmap               │
               └─────────────────────────┘
```

## Cost Estimates

| Usage | EU (CX33) | US (CPX31) |
|-------|-----------|------------|
| 25 hrs/mo | ~$0.24 | ~$0.73 |
| 50 hrs/mo | ~$0.46 | ~$1.43 |
| 100 hrs/mo | ~$0.88 | ~$2.83 |

## Tech Stack

- **Frontend**: SvelteKit on Cloudflare Pages
- **Backend**: Cloudflare Worker (TypeScript)
- **Storage**: R2 (world backups), D1 (state)
- **VPS**: Hetzner Cloud (ephemeral)
- **Package Manager**: pnpm

## Project Structure

```
src/
├── worker/     # mc-control Cloudflare Worker
├── pages/      # mc.grove.place SvelteKit site
├── scripts/    # VPS cloud-init and shell scripts
└── sql/        # D1 database schema
```

## Development

```bash
# Install dependencies
pnpm install

# Worker development
cd src/worker
pnpm dev

# Pages development
cd src/pages
pnpm dev
```

## Deployment

Worker secrets are set via `wrangler secret put`:
- `CF_ACCOUNT_ID`, `CF_API_TOKEN`, `CF_ZONE_ID`, `CF_MC_RECORD_ID`
- `HETZNER_API_TOKEN`, `HETZNER_SSH_KEY_ID`
- `WEBHOOK_SECRET`, `ADMIN_AUTH_SECRET`

## Full Specification

See [grove-minecraft-spec.md](grove-minecraft-spec.md) for the complete technical specification including:
- Server lifecycle state machine
- API endpoints
- D1 schema
- VPS setup scripts
- Admin dashboard layouts

---

*Part of the grove.place infrastructure*
