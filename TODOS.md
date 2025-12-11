# GroveMC - Deployment Progress

> **Status**: Infrastructure setup in progress. Secrets configured, R2 upload next.

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

---

## NEXT: Upload Remaining Assets

### Step 1: Download and Upload Fabric Server JAR
**REQUIRED**: The actual Minecraft server executable. Must be Fabric for 1.20.1.

1. Download Fabric Server from: https://fabricmc.net/use/server/
   - Minecraft Version: **1.20.1**
   - Loader Version: Latest stable
   - Installer Version: Latest
   - Download the "Server" JAR

2. Upload to R2:
```bash
wrangler r2 object put mc-assets/server/fabric-server-launch.jar --file=/path/to/downloaded/fabric-server-mc.1.20.1-loader.X.XX.X-launcher.X.X.X.jar
```

### Step 4: Download and Upload Mods
**REQUIRED**: Performance mods for a smooth server experience.

**Essential mods for Fabric 1.20.1:**
| Mod | Purpose | Download |
|-----|---------|----------|
| Fabric API | Required for all Fabric mods | https://modrinth.com/mod/fabric-api/versions?g=1.20.1 |
| Lithium | Server-side performance | https://modrinth.com/mod/lithium/versions?g=1.20.1 |
| FerriteCore | Memory optimization | https://modrinth.com/mod/ferrite-core/versions?g=1.20.1 |

**Optional but recommended:**
| Mod | Purpose | Download |
|-----|---------|----------|
| Chunky | Pre-generate chunks | https://modrinth.com/mod/chunky/versions?g=1.20.1 |
| Spark | Performance profiler | https://modrinth.com/mod/spark/versions?g=1.20.1 |

**Bulk upload using the helper script:**
```bash
cd /Users/autumn/Documents/Projects/GroveMC
./upload-mods.sh /path/to/your/mods/folder
```

This script will find all `.jar` files in the folder and upload them to `mc-assets/mods/`.

### Step 5: Upload World Backup (if you have one)
**OPTIONAL**: If you have an existing world to restore.

```bash
# First, create a tarball of your world folder
cd /path/to/your/minecraft/server
tar -czf world.tar.gz world/

# Upload to R2
wrangler r2 object put mc-worlds/current/world.tar.gz --file=world.tar.gz
```

If starting fresh, skip this step - a new world will be generated on first boot.

### Step 6: Fix ops.json and whitelist.json UUIDs
**IMPORTANT**: The placeholder UUIDs need to be replaced with real ones.

1. Look up your Minecraft UUID: https://mcuuid.net/?q=YourMinecraftUsername
2. Update `src/assets/ops.json` with real UUID
3. Update `src/assets/whitelist.json` with real UUID
4. Re-upload both files to R2

---

## After R2 Upload: Deploy Worker

```bash
cd /Users/autumn/Documents/Projects/GroveMC/src/worker
pnpm install
pnpm run deploy
```

---

## After Worker Deploy: Test

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

---

## R2 Bucket Structure (Target State)

### `mc-assets` (static files)
```
mc-assets/
├── server/
│   ├── fabric-server-launch.jar    ← NEED TO DOWNLOAD
│   ├── server.properties           ← Ready to upload
│   ├── ops.json                    ← Need real UUID
│   ├── whitelist.json              ← Need real UUID
│   └── eula.txt                    ← Ready to upload
├── mods/
│   ├── fabric-api-X.X.X.jar        ← NEED TO DOWNLOAD
│   ├── lithium-X.X.X.jar           ← NEED TO DOWNLOAD
│   └── ferritecore-X.X.X.jar       ← NEED TO DOWNLOAD
└── scripts/
    ├── start.sh                    ← Ready to upload
    ├── stop.sh                     ← Ready to upload
    ├── watchdog.sh                 ← Ready to upload
    └── sync-to-r2.sh               ← Ready to upload
```

### `mc-worlds` (dynamic - created at runtime)
```
mc-worlds/
├── current/
│   └── world.tar.gz                ← Optional: existing world backup
└── backups/
    └── (auto-created by watchdog)
```
