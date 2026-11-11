# Outpost — on-demand Minecraft servers

A Minecraft server that exists only while you are playing on it.

`outpost start` rents a Hetzner VPS, restores the world from R2, and points DNS at
it. When everyone has logged off and stayed off, the orchestrator saves the world,
ships it back to R2, and deletes the server. You pay for the hours you played.

A two-hour session costs about **two cents**.

```
   you ──► outpost start                 outpost serve (the loop)
              │                                │
              ▼                                ▼
        Hetzner Cloud  ◄──── create / poll / delete
              │
              ▼
   ephemeral VPS ── tailnet ──► RCON: "who is online?"
        │                              │
        └── world ──► R2 ◄─────────────┘
```

## How it works

The orchestrator is a single Go binary that lives on the homelab box. It holds one
state machine and one clock:

```
OFFLINE ──► PROVISIONING ──► RUNNING ⇄ IDLE ──► TERMINATING ──► OFFLINE
```

- **PROVISIONING** — the VPS is being built by cloud-init. It joins the tailnet so
  the orchestrator can reach it without opening anything but port 25565.
- **RUNNING ⇄ IDLE** — one RCON `list` call, every 20 seconds, answers *is it up*
  and *who is playing*. Nobody on for `idle_grace` → `IDLE`. Nobody on for
  `idle_grace + idle_timeout` → shutdown.
- **TERMINATING** — `save-all`, `stop`, wait for the JVM to exit, tar the world,
  `rclone` it to R2, then `DELETE` the server. The delete is confirmed before the
  state goes back to `OFFLINE`.

Every transition is idempotent. Killing the process mid-shutdown does not lose
anything: the next tick picks up where the last one stopped, and the world is only
considered saved once R2 has it.

## Setup

One-time, none of it in this repo:

1. **Hetzner Cloud** — an API token, and an SSH key uploaded to the project. Note
   the key id.
2. **Cloudflare** — an API token with `Zone:DNS:Edit` for the zone, and the zone id.
   The record is created on first start.
3. **Tailscale** — an auth key for a tagged device (`tag:outpost`). Revoke it if it
   ever leaks; nothing else uses it.
4. **R2** — two buckets. Empty is fine: no world means Minecraft generates one,
   no mods means vanilla. The keys the cloud-init template expects are:

   ```
   mc-assets/server/{fabric-server-launch.jar,server.properties,ops.json,whitelist.json,eula.txt}
   mc-assets/mods/*.jar
   mc-assets/config/
   mc-assets/scripts/{start.sh,stop.sh}
   mc-worlds/current/world.tar.gz        optional
   ```

   Publishing assets into R2 is not built yet — see `HANDOFF.md`.

Secrets are environment variables, never the config file:

```bash
export HETZNER_TOKEN=...
export CF_TOKEN=...
export TS_AUTHKEY=tskey-auth-...
export R2_ACCESS_KEY=...
export R2_SECRET_KEY=...
export OUTPOST_TOKEN=...          # optional bearer token for the HTTP API
```

## Configuration

Copy `config.example.yaml` to `config.yaml`. Everything except the secrets above
goes there. The two timings worth knowing:

| Setting | Default | Meaning |
|---|---|---|
| `server.idle_grace` | `10m` | empty before the state flips to `IDLE` |
| `server.idle_timeout` | `20m` | extra empty time before the server dies |

Thirty minutes total after the last logout, so a quick AFK drop does not cost you a
three-minute rebuild.

## Commands

```bash
outpost serve            # the reconcile loop + JSON API (this is the daemon)
outpost start [region]   # rent a server (default region: eu)
outpost stop             # save the world, delete the server
outpost status           # what it is doing, and what it has cost
outpost tick             # one reconcile pass, for cron
outpost reset            # delete every outpost server, back to OFFLINE
```

`outpost serve` is the only long-running piece:
`tailscale serve --bg 8896` in front of it gives you
`https://<host>.ts.net:8896/api/status`.

## Building

```bash
make build      # local
make potato     # linux/arm64, the binary that goes to the Le Potato
make test
```

Pure Go SQLite, no cgo, so the cross-compile works from the Mac with no toolchain
gymnastics.

## Costs

The only thing that bills is a running VPS. At Hetzner's `cx32` rate (~$0.012/hr)
that is roughly a cent per hour of play; a two-hour session is about two cents and
a month of heavy use is under a dollar. R2 storage is ~1.1 GB, inside the free tier.

## Layout

```
cmd/outpost/          CLI
internal/orchestrator/ state machine (the interesting part)
internal/cloudinit/   the user-data template that builds the VPS
internal/rcon/        Minecraft RCON client
internal/hetzner/     Hetzner Cloud API
internal/dns/         Cloudflare A record
internal/store/       SQLite: state, sessions, backups, events
legacy/v1/            the 2025 Worker + watchdog build, kept for reference
HANDOFF.md            where this stands and what is left
PLAN.md               why it looks like this
```

`legacy/v1/` is not built or deployed. It earned its keep — 12 sessions, 4.6 hours,
$0.057 — and then explained, in `TODOS.md`, exactly which bugs to not repeat.
