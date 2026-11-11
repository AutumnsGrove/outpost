# Outpost — v2 plan

> Bringing Outpost back as its own thing, outside the grove.
> Written 2026-11-11, before any code. Decisions first, reasoning under each.

## Where v1 got to

v1 (`legacy/v1/`) was a Cloudflare Worker (`mc-control`) plus 188 Fabric mods in R2,
a 38 MB world in R2, a D1 state table, and a 228-line bash `watchdog.sh` running
inside the Hetzner VPS.

It worked, and there are receipts: **12 sessions, 4.62 hours, $0.0565 total.**
Last session ended 2025-12-17. All of it is still deployed and intact — worker,
D1, both R2 buckets, and your world tarball.

It stopped for two reasons, neither of which was "we decided to stop":

1. **`watchdog.sh` crash-looped.** `grep -c` prints `0` *and* exits 1, so
   `$(grep -c … || echo "0")` produced `"0\n0"`, which blew up `$((joins - leaves))`
   under `set -e`. systemd restarted it every 10s; the `trap cleanup EXIT` fired a
   full 38 MB world backup on every death. The backups table shows ten of them 20
   seconds apart.
2. **Auto-terminate was never wired up.** The watchdog emitted `TERMINATING` after
   45 idle minutes and then slept forever. No worker route handled that state, and
   the 5-minute health check only reclaimed VPSes whose Hetzner status was already
   `off`/`deleting`. A VPS sitting there "terminating" looked healthy. Only the
   manual Stop button ever called `deleteServer()`.

Both bugs live in the seam between two programs that each own half the state
machine. That seam is what v2 removes.

## Decisions

### 1. Rename the repo to Outpost, keep the history

`AutumnsGrove/GroveMC` → `AutumnsGrove/outpost`. 25 commits of hard-won deploy
knowledge and a 44 KB spec stay attached instead of being re-typed. v1 code moves
to `legacy/v1/` in one commit rather than being deleted, so the spec and the
TODOS bug log stay readable while v2 catches up.

### 2. The orchestrator is a Go service on the Le Potato

The Worker is not load-bearing. Every job it does — HTTP API, 5-minute cron,
Hetzner API, Cloudflare DNS, state store, webhook receiver, auth — is a job the
Potato already does for six other services. Moving the brain to the homelab also
deletes the auth problem: Tailscale is the access control, which is how Meridian,
Warbler, Sepia and the rest already work.

The Potato cannot run Minecraft (no RAM, wrong CPU) and will not try. It holds
state, calls APIs, and polls the VPS. That is all it ever needs to do.

**Consequence:** the Worker gets undeployed at the end, and `grove.place` keeps
nothing but a cleanup PR.

### 3. No webhook. No watchdog. RCON over Tailscale.

This is the important one. v1's split-brain existed because the VPS had to tell
the worker things over HTTP, so there were two clocks and two opinions.

In v2 the VPS has no opinions:

- cloud-init installs `tailscale`, joins the tailnet with a tagged auth key, and
  starts Minecraft with RCON enabled.
- The orchestrator reaches the VPS by MagicDNS name (`outpost-<id>`) over the
  tailnet, so **no port is ever exposed except 25565**.
- One RCON `list` call answers both questions at once: *is it up* and *who is
  playing*. That replaces the `ready`/`heartbeat`/`state-change` webhooks, the
  log-parsing heuristics, and the whole watchdog script.
- Shutdown is `save-all` → `stop` → rclone the world → `DELETE /servers/{id}`,
  driven from one place, in one order, with one retry policy.

Nothing on the internet needs to reach the homelab. Nothing on the homelab needs
to be reachable from the internet.

### 4. Drop the SUSPENDED state

v1's `SUSPENDED` stopped the Minecraft process while keeping the VPS warm. Hetzner
bills a running VPS whether or not a JVM is on it, so SUSPENDED saved nothing and
cost a whole extra state transition plus a `systemctl stop` from inside the
watchdog's own unit. v2 goes `RUNNING → IDLE → TERMINATING → OFFLINE` and nothing
else.

### 5. Idle timings: 10 min to IDLE, 20 more to terminate

v1 used 15 + 45 = 60 minutes of billing after the last player left. For a game
you play in two-hour bursts, that is half an hour of server nobody is using.
30 minutes total is still generous enough to cover "someone dropped to check
Discord". Both are config values.

### 6. Keep R2 exactly where it is

`mc-assets` (190 objects, 298 MB: 188 mods, the Fabric launcher, `server.properties`,
`start.sh`) and `mc-worlds` (`current/world.tar.gz`, 38.8 MB) are reused **with
v1's exact key layout**, so the existing world and modpack load with no migration
and no re-upload. R2 stays as a dumb blob shelf: no D1, no Worker, ~1.1 GB on the
10 GB free tier.

### 7. The DNS record is an A record, updated on start

Simplest thing that works for someone who is not on your tailnet: on `RUNNING`,
PATCH `mc.<domain>` → the VPS IPv4 with a 60s TTL; leave it pointing at the last
IP after shutdown. A stale A record to a dead IP fails to connect in ~1s, which is
the correct user experience for "server is offline". This is what v1 did and it
was fine — the bug there was the record surviving at an IP that wasn't even a
GroveMC box.

### 8. SQLite via `modernc.org/sqlite`, not `mattn/go-sqlite3`

Pure Go, so `GOOS=linux GOARCH=arm64 go build` cross-compiles from the Mac with no
cgo toolchain and no surprises on the Potato.

## Shape

```
Le Potato (~/outpost, Go binary, SQLite, tailscale serve :8896)
  ├── tick loop (every 20s) — the only thing that changes state
  ├── Hetzner API      create / poll / delete
  ├── Cloudflare API   A record
  ├── RCON over tailnet  list / save-all / stop
  ├── SSH over tailnet   rclone the world up
  └── JSON API + tile in Meridian
        │
        ├──► Hetzner cx32 in nbg1  ─ ephemeral, deleted on idle
        │      cloud-init: tailscale, java 17, rclone pull from R2,
        │      restore world, start MC, RCON on
        └──► R2  mc-assets/ + mc-worlds/   (unchanged from v1)
```

Player path: `mc.<domain>` → A record → `:25565` on the ephemeral VPS.

## Not doing yet

- Snapshots (`snapshot_id` is in the config and wired into `createServer`, but the
  snapshot does not exist yet). Once it does, provisioning drops from ~3 minutes of
  downloading 298 MB to about 40 seconds. Worth doing after one clean run.
- Dynmap / `map.` subdomain.
- Discord notifications. The homelab health check email already exists.
- A real dashboard. JSON API first, Meridian tile with a start/stop control second.

## Deployment steps that are not in this repo

These touch things outside the repo and need Autumn's go-ahead:

1. Rename `AutumnsGrove/GroveMC` → `AutumnsGrove/outpost` on GitHub.
2. Create a tagged, pre-authorised Tailscale auth key for `tag:outpost`.
3. `wrangler delete` the `mc-control` worker; remove the `mc-control` and `mc`
   entries from `grove-router`; drop the `Outpost` glossary term from
   `grove-term-manifest.json`; clean the stale `mc.grove.place` A and SRV records.
4. Deploy to the Potato: `docs/outpost.md` in the homelab repo, systemd unit,
   Meridian tile, entry in `service-healthcheck.sh`.
