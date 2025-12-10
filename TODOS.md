# GroveMC Implementation TODOs

## Phase 1: Cloudflare Setup
- [ ] Create R2 bucket: `mc-assets`
- [ ] Create R2 bucket: `mc-worlds`
- [ ] Create D1 database: `mc-state`
- [ ] Generate R2 API credentials
- [ ] Create Cloudflare Tunnel for Dynmap
- [ ] Create A record for `mc.grove.place` (initial placeholder)
- [ ] Create SRV record for `grove.place` connection
- [ ] Note Zone ID and create API token for DNS updates

## Phase 2: Prepare Assets
- [ ] Download Fabric server JAR for 1.20.1
- [ ] Collect all mods
- [ ] Create `server.properties`
- [ ] Create `ops.json`
- [ ] Create `whitelist.json`
- [ ] Create `eula.txt` with `eula=true`
- [ ] Upload to `mc-assets` bucket

## Phase 3: Hetzner Setup
- [ ] Create Hetzner Cloud account
- [ ] Generate API token
- [ ] Add SSH key
- [ ] Test API with both regions (EU + US)

## Phase 4: mc.grove.place Pages Site
- [ ] Build status display page (SvelteKit)
- [ ] Configure Cloudflare Pages deployment
- [ ] Set up `mc.grove.place` custom domain

## Phase 5: Worker Development
- [ ] `/api/mc/start` - provisions VPS (with region param)
- [ ] `/api/mc/stop` - graceful shutdown
- [ ] `/api/mc/status` - full status
- [ ] `/api/mc/status/public` - public status for Pages site
- [ ] `/api/mc/whitelist` - add/remove/list
- [ ] `/api/mc/command` - send command
- [ ] `/api/mc/sync` - manual backup
- [ ] `/api/mc/history` - session history
- [ ] Webhook handlers (ready, heartbeat, state-change, backup-complete)
- [ ] Cloudflare DNS update on server ready
- [ ] Authentication middleware

## Phase 6: VPS Scripts
- [ ] Finalize cloud-init YAML
- [ ] Finalize `start.sh`
- [ ] Finalize `stop.sh`
- [ ] Finalize `watchdog.sh`
- [ ] Finalize `sync-to-r2.sh`
- [ ] Test on throwaway Hetzner server

## Phase 7: Admin Dashboard
- [ ] Add Minecraft tab to admin.grove.place
- [ ] Region toggle (EU/US)
- [ ] Dynamic Start/Status button (header)
- [ ] Server status widget
- [ ] Player list widget
- [ ] TTL countdown widget
- [ ] Session cost widget (shows rate based on region)
- [ ] Storage widget
- [ ] Backup widget
- [ ] Whitelist management widget
- [ ] Usage history widget
- [ ] Console widget (WebSocket)
- [ ] Dynmap embed

## Phase 8: Testing
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

## Phase 9: Polish
- [ ] Error handling and retries
- [ ] Loading states
- [ ] Discord webhook notifications (optional)
- [ ] Documentation

---

## Blocked
*None currently*

## Notes
- Full spec: `grove-minecraft-spec.md`
- Worker secrets set via `wrangler secret put`
- GroveEngine patterns available in local Projects folder
