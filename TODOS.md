# GroveMC - Deployment Progress

> **Status**: 🎉 **READY TO TEST!** All assets uploaded, worker deployed. Time to spin up the server!

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

## NEXT: Live Testing!

### Test 1: Verify Status Endpoint
```bash
curl https://mc-control.m7jv4v7npb.workers.dev/api/mc/status/public
# Expected: {"state":"OFFLINE","players":{"online":0,"max":20},"version":"1.20.1"}
```
✅ Already verified working!

### Test 2: Start Server from Dashboard
1. Go to https://groveauth.pages.dev/dashboard/minecraft
2. Click "Start Server" (EU region)
3. Watch for state changes: OFFLINE → STARTING → RUNNING

### Test 3: Verify DNS Update
```bash
dig mc.grove.place
# Should show Hetzner VPS IP (not 1.1.1.1)
```

### Test 4: Connect with Minecraft
- Server address: `grove.place` or `mc.grove.place`
- Version: 1.20.1 with Fabric + matching mods

### Test 5: Verify World Loaded
- Check that your existing world loaded (not a fresh world)
- Your builds/progress should be there!

### Test 6: Test Auto-Shutdown
- Disconnect and wait 15 min → should go IDLE
- Wait 5 more min → should go SUSPENDED
- Wait 45 min total → VPS should terminate

---

## Testing Checklist

- [ ] Public status endpoint returns OFFLINE ✅
- [ ] Admin dashboard loads at /dashboard/minecraft
- [ ] Start server (EU region) from dashboard
- [ ] DNS updates to VPS IP (check `dig mc.grove.place`)
- [ ] Minecraft client can connect to `grove.place` or `mc.grove.place`
- [ ] Existing world loaded correctly
- [ ] Player count detection works
- [ ] Idle timeout triggers (15 min no players → IDLE)
- [ ] Suspended state triggers (20 min no players → SUSPENDED)
- [ ] World backup to R2 works (check mc-worlds bucket)
- [ ] Stop server works from dashboard
- [ ] VPS auto-terminates after 45 min idle
- [ ] Session history recorded in D1
- [ ] Cost calculation accurate

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
│   └── (188 mods)                  ✅
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
