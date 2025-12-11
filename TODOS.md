# GroveMC - Ready for Deployment

> **Status**: All code is written! Just need to set up infrastructure and deploy.

---

## NEXT SESSION: Infrastructure Setup

### Step 1: Cloudflare D1 Database
```bash
cd /Users/mini/Documents/Projects/GroveMC/src/worker

# Create the database
wrangler d1 create mc-state

# Copy the database_id from the output, then update wrangler.toml:
# database_id = "YOUR_ID_HERE"

# Apply the schema
wrangler d1 execute mc-state --file=./schema.sql
```

### Step 2: Cloudflare DNS Setup
1. Go to Cloudflare Dashboard → grove.place → DNS
2. Create A record: `mc.grove.place` → `1.1.1.1` (placeholder, will be updated dynamically)
3. Create SRV record: `_minecraft._tcp.grove.place` → `0 5 25565 mc.grove.place`
4. Note down:
   - Zone ID (from Overview page)
   - mc.grove.place A record ID (click the record, check URL or API)

### Step 3: Cloudflare API Token
1. Dashboard → My Profile → API Tokens → Create Token
2. Use template "Edit zone DNS"
3. Zone Resources: Include → Specific zone → grove.place
4. Save the token

### Step 4: R2 API Credentials (for VPS rclone)
1. Dashboard → R2 → Manage R2 API Tokens → Create API token
2. Permissions: Object Read & Write
3. Specify buckets: mc-assets, mc-worlds
4. Save Access Key ID and Secret Access Key

### Step 5: Hetzner Setup
1. Create project in Hetzner Cloud Console
2. Security → API Tokens → Generate API token (Read & Write)
3. Security → SSH Keys → Add your SSH public key, note the ID

### Step 6: Set Worker Secrets
```bash
cd /Users/mini/Documents/Projects/GroveMC/src/worker

# Cloudflare
wrangler secret put CF_ACCOUNT_ID      # Your Cloudflare account ID
wrangler secret put CF_API_TOKEN       # DNS edit token from Step 3
wrangler secret put CF_ZONE_ID         # grove.place zone ID
wrangler secret put CF_MC_RECORD_ID    # mc.grove.place A record ID

# Hetzner
wrangler secret put HETZNER_API_TOKEN  # From Step 5
wrangler secret put HETZNER_SSH_KEY_ID # SSH key ID (number)

# R2 (for VPS cloud-init)
wrangler secret put R2_ACCESS_KEY      # From Step 4
wrangler secret put R2_SECRET_KEY      # From Step 4

# Internal auth
wrangler secret put WEBHOOK_SECRET     # Generate: openssl rand -hex 32
wrangler secret put GROVEAUTH_URL      # https://auth-api.grove.place
```

### Step 7: Upload Assets to R2
```bash
cd /Users/mini/Documents/Projects/GroveMC

# Scripts
wrangler r2 object put mc-assets/scripts/start.sh --file=src/scripts/start.sh
wrangler r2 object put mc-assets/scripts/stop.sh --file=src/scripts/stop.sh
wrangler r2 object put mc-assets/scripts/watchdog.sh --file=src/scripts/watchdog.sh
wrangler r2 object put mc-assets/scripts/sync-to-r2.sh --file=src/scripts/sync-to-r2.sh

# Server config
wrangler r2 object put mc-assets/server/server.properties --file=src/assets/server.properties
wrangler r2 object put mc-assets/server/ops.json --file=src/assets/ops.json
wrangler r2 object put mc-assets/server/whitelist.json --file=src/assets/whitelist.json
wrangler r2 object put mc-assets/server/eula.txt --file=src/assets/eula.txt
```

### Step 8: Deploy
```bash
# Deploy mc-control Worker
cd /Users/mini/Documents/Projects/GroveMC/src/worker
pnpm install
pnpm deploy

# Deploy GroveAuth (if not auto-deployed)
cd /Users/mini/Documents/Projects/GroveAuth
pnpm deploy
```

### Step 9: Test
```bash
# Check status endpoint
curl https://mc-control.grove.workers.dev/api/mc/status/public

# Should return: {"state":"OFFLINE","region":null,...}
```

---

## Completed

- [x] D1 schema (`schema.sql`)
- [x] Worker services (hetzner.ts, dns.ts, database.ts, cloudinit.ts)
- [x] Auth middleware (GroveAuth JWT verification)
- [x] All API routes (status, webhooks, start, stop, whitelist, sync, command, history)
- [x] Worker index.ts routing
- [x] Minecraft placeholder configs (server.properties, ops.json, whitelist.json, eula.txt)
- [x] watchdog.sh fixes (player detection, portable dates, state machine)
- [x] GroveAuth backend minecraft routes
- [x] GroveAuth frontend admin dashboard
- [x] R2 buckets created (mc-assets, mc-worlds)
- [x] D1 database created (mc-state) - need ID in wrangler.toml
- [x] Hetzner account created

---

## After Deployment: Testing Checklist

- [ ] Public status page works (mc.grove.place)
- [ ] Admin dashboard loads at /dashboard/minecraft
- [ ] Start server (EU region)
- [ ] DNS updates to VPS IP
- [ ] Minecraft client can connect
- [ ] Player count detection works
- [ ] Idle timeout triggers
- [ ] World backup to R2
- [ ] Stop server works
- [ ] Session history recorded
- [ ] Cost calculation accurate

---

## Future Enhancements (After MVP)

- [ ] Download Fabric server JAR + mods (currently placeholders)
- [ ] Cloudflare Tunnel for Dynmap (map.grove.place)
- [ ] Discord webhook notifications
- [ ] WebSocket console for live logs
- [ ] US region testing

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
