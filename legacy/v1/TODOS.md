# GroveMC - Deployment Progress

> **Status**: 🟢 **SERVER RUNNING!** Fresh world generated, ready for Friday gameplay session!

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
  - [x] `scripts/cloud-init.yaml` (updated with chunk reassembly)
  - [x] `server/server.properties`
  - [x] `server/ops.json` ✅ Real UUID
  - [x] `server/whitelist.json` ✅ Real UUID
  - [x] `server/eula.txt`
  - [x] `server/fabric-server-launch.jar`
- [x] Bulk mod upload script created (`upload-mods.sh`)
- [x] Client-only mod skip list created (`client-only-mods.txt` - 84 mods)
- [x] **188 server mods uploaded to R2** (`mc-assets/mods/`) *(travelerz removed)*
- [x] Fabric server JAR uploaded to R2
- [x] World tarball uploaded (2.9GB in 12 chunks)
- [x] ops.json updated with real UUID (`4d9b897a-6b40-4a30-8e83-eccb33333817`)
- [x] whitelist.json updated with real UUID and username (`AutumnsAdventure`)
- [x] cloud-init.yaml updated to handle chunked world downloads
- [x] GroveMC Worker deployed → `https://mc-control.m7jv4v7npb.workers.dev`
- [x] GroveAuth frontend deployed → `https://groveauth.pages.dev`
- [x] GroveAuth GitHub Actions auto-deploy configured

---

## Current Session (Dec 12, 2025)

**Server started successfully!** Fresh world with seed `8586235716160945827`
- VPS IP: `116.203.182.110`
- DNS: `mc.grove.place` → VPS IP ✅
- 188 mods loaded
- Server listening on port 25565 ✅

### Ready for Friday Testing:
- [ ] Connect with Minecraft client (`grove.place` or `mc.grove.place`)
- [ ] Verify fresh world generated correctly
- [ ] Player count detection works
- [ ] Test idle timeout (15 min no players → IDLE)
- [ ] Test world backup to R2 on shutdown
- [ ] Test auto-shutdown after 45 min idle
- [ ] Verify session history recorded in D1
- [ ] Verify cost calculation in dashboard

---

## Testing Checklist (Verified)

- [x] Public status endpoint returns correct state
- [x] Admin dashboard loads at /dashboard/minecraft
- [x] Start server (EU region) from dashboard
- [x] DNS updates to VPS IP automatically
- [x] Server starts and loads all 188 mods
- [ ] Minecraft client can connect
- [ ] Player count updates in dashboard
- [ ] Idle timeout triggers correctly
- [ ] World backup to R2 works
- [ ] Stop server works from dashboard
- [ ] VPS auto-terminates after idle timeout
- [ ] Session history recorded with correct duration/cost

---

## Bugs Found During First Deploy 🐛

### 1. ✅ FIXED - Cloudflare Bot Protection Blocks Webhooks
**Problem:** Webhook URL `https://mc-control.grove.place/api/mc/webhook` gets Cloudflare challenge page
**Fix:** Use `https://mc-control.m7jv4v7npb.workers.dev/api/mc/webhook` (workers.dev bypasses bot protection)
**File:** `src/worker/src/routes/start.ts` line 84

### 2. ✅ FIXED - Cloudflare Error 1042 - Worker-to-Worker Fetch
**Problem:** GroveAuth calling mc-control via workers.dev URL fails with error 1042
**Fix:** Use custom domain `mc-control.grove.place` for worker-to-worker calls (via grove-router proxy)
**File:** `GroveAuth/src/routes/minecraft.ts` - `MC_CONTROL_URL`

### 3. ✅ FIXED - Cloud-Init World Download - Chunked Files Not Handled
**Problem:** Cloud-init checks for `world.tar.gz` but we uploaded 12 chunks (`world.tar.gz.part_*`)
**Fix:** Updated cloud-init to detect and reassemble chunks before extraction
**File:** `src/worker/src/services/cloudinit.ts` - world download section

### 4. ⚠️ MANUAL - Hetzner SSH Key ID Not Updated
**Problem:** Worker had old SSH key ID, new machines couldn't be accessed
**Fix:** Update `HETZNER_SSH_KEY_ID` secret when SSH keys change
**Command:** `wrangler secret put HETZNER_SSH_KEY_ID`

### 5. ✅ FIXED - Hetzner Location/Server Type Unavailable (Error 412)
**Problem:** `fsn1` location or `cx33` server type not available
**Fix:** Changed to `nbg1` (Nuremberg) and `cx32` (widely available)
**Files:** `src/worker/src/services/hetzner.ts`

### 6. ✅ FIXED - Frontend [object Object] Bug
**Problem:** Dashboard showed `[object Object]` for Players Online
**Fix:** API returns `{ players: { online: N, max: M } }` - fixed accessor to `players?.online`
**File:** `GroveAuth/frontend/src/routes/dashboard/minecraft/+page.svelte`

### 7. ✅ FIXED - Frontend Whitelist Field Name Mismatch
**Problem:** Whitelist entries showing `...` instead of usernames
**Fix:** API returns `name` - fixed accessor to `player.name`
**File:** `GroveAuth/frontend/src/routes/dashboard/minecraft/+page.svelte`

### 8. ✅ FIXED - Missing Mod Dependency
**Problem:** `travelerz` mod requires `nameplate` mod which wasn't included
**Fix:** Removed `travelerz` from R2 (`mc-assets/mods/`)
**Note:** Now 188 server mods

### 9. ✅ FIXED - Systemd Service Uses Screen (Exits Immediately)
**Problem:** `start.sh` uses `screen -dm` which exits, systemd thinks service stopped
**Fix:** Changed start.sh to run Java directly with `exec` (foreground mode for systemd)
**Files:** `src/scripts/start.sh`, updated watchdog.sh to not rely on screen

### 10. ✅ FIXED - External VPS Deletion Leaves Worker State Stale
**Problem:** Deleting VPS from Hetzner console doesn't update worker state
**Fix:** Added cron-based health check (every 5 minutes) that detects orphaned VPS and resets state
**Files:** `src/worker/src/services/healthcheck.ts`, `src/worker/wrangler.toml` (cron trigger)

### 11. ✅ FIXED - Cloud-init .env file not created
**Problem:** .env file was in write_files but got overwritten by rclone sync
**Fix:** Added runcmd step to recreate .env file after rclone sync
**File:** `src/worker/src/services/cloudinit.ts`

### 12. ✅ FIXED - Cloud-init rclone sync deletes files
**Problem:** `rclone sync` deletes files in the target that aren't in the source. The order was:
1. sync server/ → /opt/minecraft/ (this created logs/mods/config dirs)
2. sync mods/ → /opt/minecraft/mods/ (ok)
3. sync config/ → /opt/minecraft/config/ (ok)
4. sync scripts/ → /opt/minecraft/ (this DELETED server files like eula.txt, server.properties!)
**Fix:** Changed all `rclone sync` to `rclone copy` to only add files, not delete
**File:** `src/worker/src/services/cloudinit.ts`

### 13. ✅ FIXED - Frontend field name mismatches (snake_case vs camelCase)
**Problem:** Frontend expected snake_case fields but API returns camelCase
- `started_at` → `startedAt`
- `duration_seconds` → `durationSeconds`
- `cost_usd` → `costUsd`
- `last_backup` → `lastWorldSync`
- `history.monthly` → `history.thisMonth`
**Fix:** Updated all frontend accessors to use camelCase API field names
**File:** `GroveAuth/frontend/src/routes/dashboard/minecraft/+page.svelte`

### 14. ✅ FIXED - Wrong hourly rate in frontend
**Problem:** Frontend showed $0.0085/hr but actual Hetzner cx32 rate is $0.0119/hr
**Fix:** Frontend now uses `serverStatus.costs.hourlyRate` from API
**Files:** `src/worker/src/services/database.ts`, `GroveAuth/frontend/...`

### 15. ✅ FIXED - Travelerz mod dependency missing (again)
**Problem:** Travelerz mod was re-uploaded to R2 and requires `nameplate` mod
**Fix:** Deleted `travelerz-1.0.1.jar` from R2 `mc-assets/mods/`
**Note:** Now 188 server mods (down from 189)

---

## Future Enhancements (After MVP Works)

- [ ] Dynmap integration (map.grove.place via Cloudflare Tunnel)
- [ ] Discord webhook notifications
- [ ] WebSocket console for live logs
- [ ] US region testing
- [ ] Resource pack server distribution
- [ ] Additional mods (if desired)
- [x] Health check cron to detect orphaned VPS/state mismatch ✅ Implemented
- [ ] Better mod dependency validation before upload
- [ ] RCON support for sending commands to running server

### Panel Features - Modpack & World Management
> See `docs/panel-features-plan.md` for full spec

**Backend API (mc-control):**
- [ ] `GET /api/mc/mods` - List all mods
- [ ] `DELETE /api/mc/mods` - Delete all mods
- [ ] `DELETE /api/mc/mods/:filename` - Delete single mod
- [ ] `GET /api/mc/world` - World info
- [ ] `DELETE /api/mc/world` - Reset world
- [ ] `GET /api/mc/backups` - List backups
- [ ] `POST /api/mc/backups/:id/restore` - Restore backup

**GroveAuth Proxy:**
- [ ] Add proxy routes for all new mc-control endpoints
- [ ] Handle multipart upload for mod files

**Frontend (Dashboard):**
- [ ] ModpackManager component (list, upload, delete mods)
- [ ] WorldManager component (reset, backups)
- [ ] Drag-and-drop file upload
- [ ] Confirmation dialogs for destructive operations

---

## Quick Reference

| Resource | Location |
|----------|----------|
| Worker API | `https://mc-control.m7jv4v7npb.workers.dev` |
| Admin Dashboard | `https://groveauth.pages.dev/dashboard/minecraft` |
| Worker code | `GroveMC/src/worker/` |
| VPS scripts | `GroveMC/src/scripts/` |
| MC configs | `GroveMC/src/assets/` |
| Full spec | `GroveMC/grove-minecraft-spec.md` |
| SSH key | `~/.ssh/grovemc` |
| Mods folder | `/Volumes/External/Prism/Instances/Becoming Autumn/minecraft/mods` |

---

## R2 Bucket Structure (Current State)

### `mc-assets` (static files) ✅ Complete
```
mc-assets/
├── server/
│   ├── fabric-server-launch.jar    ✅
│   ├── server.properties           ✅
│   ├── ops.json                    ✅
│   ├── whitelist.json              ✅
│   └── eula.txt                    ✅
├── mods/
│   └── (188 mods)                  ✅ (travelerz removed)
└── scripts/
    ├── start.sh                    ✅
    ├── stop.sh                     ✅
    ├── watchdog.sh                 ✅
    ├── sync-to-r2.sh               ✅
    └── cloud-init.yaml             ✅
```

### `mc-worlds` (world data) ✅ Uploaded
```
mc-worlds/
├── current/
│   └── world.tar.gz.part_*         ✅ (12 chunks, will become single file after first backup)
└── backups/
    └── (auto-created by watchdog)
```
