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
- [x] **189 server mods uploaded to R2** (`mc-assets/mods/`)
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
│   └── (189 mods)                  ✅
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
