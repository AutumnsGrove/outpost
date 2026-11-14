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

   Fill those keys with `outpost publish` (below). It and the player list are the
   only two things that write to R2.

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
outpost publish [dir]    # upload a server pack to R2
outpost pack fetch <src> # resolve a Modrinth modpack into a pack directory
outpost pack list        # the installed profiles, and which one is active
outpost pack use <id>    # switch which pack+world the next start boots
outpost start [region]   # rent a server (default region: eu)
outpost stop             # save the world, delete the server
outpost status           # what it is doing, and what it has cost
outpost update           # git pull, rebuild, restart — see "Updating"
outpost tick             # one reconcile pass, for cron
outpost reset [--force]  # delete every outpost server, back to OFFLINE
outpost console [cmd]    # one command on the live server (-i for a prompt)
outpost cost             # this session and this calendar month
outpost hold 48h         # keep it up with nobody on (capped, priced, see below)
outpost hold cancel      # drop the hold
outpost player list      # who may join, and who is an operator
outpost player add|remove|op|deop <name>
```

`outpost reset` refuses to delete a world that was never saved to R2, or servers
this database is not tracking; `--force` overrides it. `outpost stop` is the
command that retries the save.

`outpost hold` needs no cloud credentials: it writes one row and one audit event,
and the tick loop that honours it runs wherever the daemon runs. Anything over
12h prints the projected cost and refuses without `--yes`, because 48h is about
25× a normal session and that is not something a typo should buy. `server.max_hold`
(default `72h`) is the ceiling.

## Publishing a server

`outpost publish` is what puts the server files in R2. A pack directory looks
like this, and everything except `mods/` is optional:

```
pack/
  server/       server.properties, eula.txt, ops.json, whitelist.json, the launcher jar
  mods/*.jar    with client-only-mods.txt listing what the server must not load
  config/       mod config
  scripts/      start.sh, stop.sh
  root/         anything else, to /opt/minecraft (a pack's config/, kubejs/, defaultconfigs/)
  pack.json     written by `outpost pack fetch`; see below
```

The shortest useful first publish generates the rest:

```bash
outpost publish ./pack --fabric 1.20.1 \
  --whitelist AutumnsAdventure,OtherPlayer --op AutumnsAdventure
```

That fetches the Fabric launcher for 1.20.1, resolves both names to UUIDs through
Mojang, writes `eula.txt`, a default `server.properties` and a `start.sh`, uploads
everything, and verifies each key's size in R2 before claiming success. Add
`--prune` to delete mods and config files the pack no longer has, and `--dry-run`
to see the plan without writing anything.

Vanilla is `--fabric 1.20.1` with an empty `mods/`.

## Modpacks

A modpack is fetched, not assembled:

```bash
outpost pack fetch https://modrinth.com/modpack/cobblemon-fabric --memory 4
outpost publish pack --whitelist AutumnsAdventure --op AutumnsAdventure
```

`pack fetch` takes a Modrinth slug, a modpack URL (the one Prism's "open in browser"
gives you), a `.mrpack` URL, or a `.mrpack` on disk. `--version` pins a pack version;
the default is the newest release for Minecraft 1.20.1, and a pack that has moved on to
1.21 is refused rather than downloaded, because cloud-init installs Java 17. Files the
pack marks `env.server: unsupported` are not fetched at all, so no client mod reaches
the server.

What it writes is an ordinary pack directory plus `pack.json`, and everything else
follows from that file:

- **The launcher comes from the pack.** Its Minecraft and loader versions win over
  `--fabric` and `--loader`, and `--prune` is implied: two versions of one mod in
  `mods/` is a server that crashes, not a warning.
- **`--memory` is the RAM of the box you will rent** (default 4, the `cx23`). It sizes
  `scripts/start.sh` — two GB stay with the OS — and `publish` refuses a pack that no
  configured region's plan can hold. `fallback_types` do not cover it: Hetzner only
  falls back when a plan is sold out, not when it is too small.
- **`publish` tags the saved world with the pack.** The marker next to the world is what
  lets the next publish say "this world was made by Cobblemon, not Homestead". The same
  pack at a newer version is an update and keeps the world; a different pack is a
  question — `--new-world` discards it, `--keep-world` loads it anyway, and neither is
  assumed.

Two things a fetched pack does not carry over: its own `start.sh` JVM flags, and the
settings in its `server.properties` — Outpost writes both. CurseForge and FTB packs are
not supported; `pack fetch` says so instead of half-trying.

### Profiles: a pack and its world, swapped from the page

Every pack lives in a **profile**, and a profile is one pack and one world:

```
mc-assets/profiles/cobblemon-fabric/{pack.json,server/,mods/,config/,scripts/,root/}
mc-worlds/profiles/cobblemon-fabric/{world.tar.gz,pack.json,backups/...}
```

So you can keep a Cobblemon world and a Homestead world and switch between them. The
page's **Modpack** panel is the whole of it: paste a Modrinth link, press **Resolve** to
see exactly what is in the pack — versions, file counts, download size, the mod list
itself, what was skipped as client-only — and **Install** to write it to R2, where it
becomes the active profile. **Use** on another profile switches back, world included.

```bash
outpost pack list        # profiles, mod counts, which has a world, which is active
outpost pack use homestead
outpost publish --profile homestead ./packs/homestead   # from the Mac
```

The active profile is fixed for the length of a session: `outpost pack use` and the
page's Install/Use buttons are both refused while a server is rented, because a running
server already pulled one profile's assets and will upload its world to that profile.
The player list is deliberately *not* per profile — adding someone adds them to every
world — and a `start` with no server files installed for the active profile is refused
before anything is rented.

The old single-profile keys (`mc-assets/server/`, `mc-worlds/current/`) are not read any
more and not deleted: `outpost publish --profile default` is the one command that brings
those assets forward if you want them.

`outpost serve` is the only long-running piece:
`tailscale serve --https=8896` in front of it gives you a page at
`https://<host>.ts.net:8896/` — state, who is playing, the address to hand out,
what the session has cost, **Start / Save and stop / Update** buttons, a console,
and the player list. It polls `GET /api/overview` once every five seconds;
`GET /api/status` is a separate, narrower contract kept for Meridian's tile, so
add new things to `/api/overview` rather than to `status`. The Modpack panel is its own
`GET /api/pack`, polled every two seconds only while a preview or install is running.

The page is a SvelteKit app built to static files and embedded with `go:embed`
(the same shape as bwmon and Polaris), so the box needs no Node at runtime and
`git pull && go build` is the whole deploy. The built output in `web/build` is
committed on purpose; `make web` rebuilds it.

## Console and players

The page and the CLI reach the same two places: RCON for the running game, and
`mc-assets` for anything that has to outlive the VPS.

```bash
outpost console list                  # one command, prints the reply
outpost console -i                    # a prompt; one command per line, `exit` to leave

outpost player list
outpost player add AutumnsAdventure
outpost player op AutumnsAdventure
outpost player remove AutumnsAdventure
```

`outpost player` writes `whitelist.json` and `ops.json` to R2 **first**, then tells
the running server over RCON, because the bucket is what cloud-init restores on the
next boot. The other way round, a successful `op` with a failed upload is a change
that dies with the VPS. Names are resolved through Mojang, so what gets stored is
the canonical spelling and UUID. One person editing is assumed; two writers racing
each other is last-write-wins.

Two things the console will not do, on purpose:

- **`stop` and `save-off` are refused**, anywhere in the line, so `execute run stop`
  does not get round it. The orchestrator reads a dead server as "everybody left":
  the probes fail, and it saves the world and deletes the VPS underneath you. Use
  Save and stop.
- **Nothing shells out.** RCON only ever reaches Minecraft, never the host.

Every command lands in `events` with where it came from — `page`, `api` or `cli` —
so "who opped the griefer" has an answer, and refusals are logged too.

Both commands need the credentials: the console does not (it reads the address and
password out of the database the daemon writes), but the player list does, because
it writes to R2. On the Potato the secrets are in `/etc/outpost.env`, which the
service gets from systemd and an interactive shell does not:

```bash
set -a; . /etc/outpost.env; set +a
```

## In-game commands

Players get the same surface without leaving Minecraft:

```
/outpost cost            this session, this month, the rate, what 2h more adds
/outpost status          state, players, time until shutdown, any hold
/outpost hold 48h        keep the server up with nobody on (op only)
/outpost hold 48h confirm  the second half of anything over 12h
/outpost cancel          drop the hold (op only)
```

That needs a mod, because Fabric has no plugin API and RCON can only send commands
*into* the game, never carry one out. `mod/` is a small server-side Fabric mod —
clients do not need it and do not load it — that registers `/outpost` with Brigadier
and prints whatever the daemon says. It contains no rules: who may hold, what it
costs, how long is too long are all decided by `POST /api/game/command`, which is
why that endpoint is testable with `curl` and the jar is under 200 lines.

Two locks on the door. The mod carries a **per-session token** that cloud-init writes
into `config/outpost.json` next to the RCON password; the jar ships in the public
`mods/` prefix, so a secret baked into it would be a published secret. And
`hold`/`cancel` are checked against `ops.json` in R2 as well as refused by the mod
at permission level 2 — the daemon cannot see the game's op state and does not take
the caller's word for it.

### Building and shipping the mod

```bash
cd mod && ./gradlew build        # needs a JDK 17; produces build/libs/outpost-1.0.0.jar
cd .. && cp mod/build/libs/outpost-1.0.0.jar pack/mods/
outpost publish pack --prune
```

It is pinned to Minecraft 1.20.1 and Fabric Loader 0.15.x (Fabric API 0.92.2 is the
last for 1.20.1 that accepts 0.15; 0.92.4+ demands loader 0.16.10). The pack needs
`fabric-api` on the server, which almost every modpack already has.

### Pointing the mod at the daemon

`server.game_endpoint` is the URL the VPS posts to. Use the tailnet **name** that
`tailscale serve` already publishes:

```yaml
server:
  game_endpoint: "https://lepotato.taild95077.ts.net:8896"
```

That is the one place this feature needed help from the plumbing. The VPS joins the
tailnet with `--accept-dns=false`, so MagicDNS names do not resolve there, and an
https URL against a bare `100.x` address fails certificate validation because the
certificate is issued for the name. So the daemon resolves that hostname *from its
own tailnet* at rent time and cloud-init pins it in the VPS's `/etc/hosts`:

```
100.81.103.51   lepotato.taild95077.ts.net
```

A resolvable name, a valid certificate, and no change to how the page is served. If
the lookup fails the session still starts, `/outpost` just answers that it cannot
reach the daemon, and `game-unreachable` lands in the event log.

## Building

```bash
make build      # local
make potato     # linux/arm64, the binary that goes to the Le Potato
make test
make web        # rebuild web/build after touching web/src, then commit it
```

`make build` does not touch the frontend: `web/build` is committed, so a plain
`go build` on the box produces a complete binary.

Pure Go SQLite, no cgo, so the cross-compile works from the Mac with no toolchain
gymnastics.

## Costs

The only thing that bills is a running VPS, and Hetzner bills in euros:
`cx23` (2 vCPU / 4 GB) is €0.0104/hr, so a two-hour session is **€0.021** and a
heavy month is under a euro. `cx33` (4 vCPU / 8 GB) doubles the RAM for €0.016/hr
— €0.032 a session — which is the one to switch to if the modpack needs it. R2
holds the modpack and a world, well inside the free tier.

Two caveats worth knowing before trusting any number here:

- **Hetzner repriced its cloud plans in June 2026**, and the `hourly_rate` values
  in `config.yaml` are estimates that only feed the status readout — never a bill.
- **Instance size is the lever, not the provider.** Halving the RAM now saves
  35%, not the threefold it saved in 2025 — Hetzner repriced the 8 GB tier
  downwards. `config.example.yaml` lists the four plans worth knowing, and `ash`
  is no longer one of them at six times the price.

## Layout

```
cmd/outpost/          CLI
internal/orchestrator/ state machine (the interesting part)
internal/cloudinit/   the user-data template that builds the VPS
internal/rcon/        Minecraft RCON client
internal/hetzner/     Hetzner Cloud API
internal/dns/         Cloudflare A record
internal/store/       SQLite: state, sessions, backups, events
internal/roster/      the player list: whitelist.json and ops.json in R2
legacy/v1/            the 2025 Worker + watchdog build, kept for reference
PLAN.md               why it looks like this
```

`legacy/v1/` is not built or deployed. It earned its keep — 12 sessions, 4.6 hours,
$0.057 — and then explained, in `TODOS.md`, exactly which bugs to not repeat.
