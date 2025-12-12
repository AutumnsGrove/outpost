# GroveMC - Deployment Progress

> **Status**: Almost ready! Fabric JAR uploaded, world uploading (chunked), UUID fixed. Just need to re-upload configs and deploy worker.

---

## Completed

- [x] D1 database created (`mc-state`)
- [x] D1 schema applied (all tables: server_state, sessions, monthly_summary, whitelist_cache, backups)
- [x] D1 database ID added to `wrangler.toml` (`969fbe3d-2c67-4c2e-baa2-a92b8f24d4e2`)
- [x] R2 buckets created (`mc-assets`, `mc-worlds`)
- [x] Hetzner Cloud account created
- [x] SSH key generated (`~/.ssh/grovemc`)
- [x] All worker secrets configured:
  - [x] `CF_ACCOUNT_ID`
  - [x] `CF_API_TOKEN` (Edit zone DNS for grove.place)
  - [x] `CF_ZONE_ID`
  - [x] `CF_MC_RECORD_ID`
  - [x] `HETZNER_API_TOKEN`
  - [x] `HETZNER_SSH_KEY_ID`
  - [x] `R2_ACCESS_KEY`
  - [x] `R2_SECRET_KEY`
  - [x] `WEBHOOK_SECRET` (auto-generated)
  - [x] `ADMIN_AUTH_SECRET` (auto-generated)
- [x] Cloudflare DNS records created:
  - [x] A record: `mc.grove.place` → `1.1.1.1` (DNS only, gray cloud)
  - [x] SRV record: `_minecraft._tcp.grove.place` → `0 5 25565 mc.grove.place`
- [x] Worker code complete (all API routes, services)
- [x] VPS scripts written (start.sh, stop.sh, watchdog.sh, sync-to-r2.sh)
- [x] Placeholder configs created (server.properties, ops.json, whitelist.json, eula.txt)
- [x] GroveAuth backend minecraft routes
- [x] GroveAuth frontend admin dashboard
- [x] R2 uploads complete:
  - [x] `scripts/start.sh`
  - [x] `scripts/stop.sh`
  - [x] `scripts/watchdog.sh`
  - [x] `scripts/sync-to-r2.sh`
  - [x] `server/server.properties`
  - [x] `server/ops.json` (needs UUID fix)
  - [x] `server/whitelist.json` (needs UUID fix)
  - [x] `server/eula.txt`
- [x] Bulk mod upload script created (`upload-mods.sh`)
- [x] Client-only mod skip list created (`client-only-mods.txt` - 84 mods)
- [x] **189 server mods uploaded to R2** (`mc-assets/mods/`)
- [x] Fabric server JAR uploaded to R2 (`server/fabric-server-launch.jar`)
- [x] World tarball created and uploading in chunks (2.9GB → 12 x 250MB parts)
- [x] ops.json updated with real UUID (`4d9b897a-6b40-4a30-8e83-eccb33333817`)
- [x] whitelist.json updated with real UUID and username (`AutumnsAdventure`)

---

## NEXT STEPS (In Order)

### Step 1: Re-upload ops.json and whitelist.json to R2
The local files are updated with real UUIDs - now upload them:
```bash
wrangler r2 object put mc-assets/server/ops.json --file=src/assets/ops.json --remote
wrangler r2 object put mc-assets/server/whitelist.json --file=src/assets/whitelist.json --remote
```

### Step 2: Deploy Worker
```bash
cd /Users/mini/Documents/Projects/GroveMC/src/worker
pnpm install
pnpm run deploy
```

### Step 3: Test
```bash
# Check status endpoint (should return OFFLINE state)
curl https://mc-control.grove.workers.dev/api/mc/status/public

# Expected response:
# {"state":"OFFLINE","players":{"online":0,"max":20},"version":"1.20.1"}
```

---

## Testing Checklist (After Everything is Deployed)

- [ ] Public status endpoint returns OFFLINE
- [ ] Admin dashboard loads at /dashboard/minecraft
- [ ] Start server (EU region) from dashboard
- [ ] DNS updates to VPS IP (check `dig mc.grove.place`)
- [ ] Minecraft client can connect to `grove.place` or `mc.grove.place`
- [ ] Player count detection works
- [ ] Idle timeout triggers (15 min no players → SUSPENDED)
- [ ] World backup to R2 works
- [ ] Stop server works
- [ ] Session history recorded in D1
- [ ] Cost calculation accurate

---

## Future Enhancements (After MVP Works)

- [ ] Dynmap integration (map.grove.place via Cloudflare Tunnel)
- [ ] Discord webhook notifications
- [ ] WebSocket console for live logs
- [ ] US region testing
- [ ] Resource pack server distribution
- [ ] Additional mods (if desired)

---

## Quick Reference

| Resource | Location |
|----------|----------|
| Worker code | `GroveMC/src/worker/` |
| VPS scripts | `GroveMC/src/scripts/` |
| MC configs | `GroveMC/src/assets/` |
| Admin dashboard | `GroveAuth/frontend/src/routes/dashboard/minecraft/` |
| Backend routes | `GroveAuth/src/routes/minecraft.ts` |
| Full spec | `GroveMC/grove-minecraft-spec.md` |
| SSH key | `~/.ssh/grovemc` (private) / `~/.ssh/grovemc.pub` (public) |
| Mods folder | `/Volumes/External/Prism/Instances/Becoming Autumn/minecraft/mods` |

---

## R2 Bucket Structure (Current State)

### `mc-assets` (static files)
```
mc-assets/
├── server/
│   ├── fabric-server-launch.jar    ✅ Uploaded
│   ├── server.properties           ✅ Uploaded
│   ├── ops.json                    ⚠️ Need to re-upload (UUID fixed locally)
│   ├── whitelist.json              ⚠️ Need to re-upload (UUID fixed locally)
│   └── eula.txt                    ✅ Uploaded
├── mods/
│   └── (189 mods uploaded!)        ✅ Complete
└── scripts/
    ├── start.sh                    ✅ Uploaded
    ├── stop.sh                     ✅ Uploaded
    ├── watchdog.sh                 ✅ Uploaded
    └── sync-to-r2.sh               ✅ Uploaded
```

### `mc-worlds` (dynamic - created at runtime)
```
mc-worlds/
├── current/
│   └── world.tar.gz.part_*         🔄 Uploading (12 chunks, 250MB each)
└── backups/
    └── (auto-created by watchdog)
```
