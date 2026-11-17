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
outpost pack vanilla     # write a server with no mods: Fabric, empty mods/ (--mc for 1.21.1)
outpost pack list        # the installed profiles, and which one is active
outpost pack use <id>    # switch which pack+world the next start boots
outpost world list       # every profile's world, and any world no profile claims
outpost world import <id>  # adopt an existing world into a profile, by copy
outpost world delete <id>  # move a world to trash/, where it waits 14 days
outpost pack delete <id>   # remove a profile: its world, backups and files
outpost start [region]   # rent a server (default region: eu; --type for the plan)
outpost stop             # save the world, delete the server
outpost status           # what it is doing, and what it has cost
outpost update           # ask the host watcher for the newest image — see "Deploying"
outpost tick             # one reconcile pass, for cron
outpost reset [--force]  # delete every outpost server, back to OFFLINE
outpost console [cmd]    # one command on the live server (-i for a prompt)
outpost player list      # who may join, and who is an operator
outpost player add|remove|op|deop <name>
```

`outpost reset` refuses to delete a world that was never saved to R2, or servers
this database is not tracking; `--force` overrides it. `outpost stop` is the
command that retries the save.

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

There is no loader flag to remember for vanilla: `outpost pack vanilla` writes the
`pack.json` that means "no mods", and `outpost publish` does the rest.

## Modpacks

A modpack is fetched, not assembled:

```bash
outpost pack fetch https://modrinth.com/modpack/cobblemon-fabric --memory 4
outpost publish pack
```

No `--whitelist` on the publish: when nobody is named, the list already in R2 goes back up
unchanged, so a re-publish never asks Mojang anything and the operators recorded on the
page stay operators. `--whitelist`/`--op` are for changing who may play, not for
repeating what the bucket already knows.

`pack fetch` takes a Modrinth slug, a modpack URL (the one Prism's "open in browser"
gives you), a `.mrpack` URL, or a `.mrpack` on disk. `--version` pins a pack version;
the default is the newest release for Minecraft 1.20.1, and any version outside the
table in `internal/publish` (1.20.1 on Java 17, 1.21.1 on Java 21) is refused rather
than downloaded, because the VPS installs the JDK the pack's `pack.json` asks for and a
version outside the table has no JDK to install. Files the pack marks
`env.server: unsupported` are not fetched at all, so no client mod reaches the server —
except when a mod the pack keeps says it cannot load without one. `pack fetch` reads the
dependencies out of every jar it installs, and a missing one it can satisfy from a file
the pack skipped is kept instead, with a warning saying which mod wanted it (PLAN 40). A
dependency nothing in the pack provides fails the fetch with the mod and the version
range it asked for, rather than renting a box for a server that will not boot.

What it writes is an ordinary pack directory plus `pack.json`, and everything else
follows from that file:

- **The launcher comes from the pack.** Its Minecraft and loader versions win over
  `--fabric` and `--loader`, and `--prune` is implied: two versions of one mod in
  `mods/` is a server that crashes, not a warning.
- **`--memory` is the RAM the pack needs** (default 4, the `cx23`). It sizes
  `scripts/start.sh` — two GB stay with the OS — and it is what the page's plan picker
  defaults to: the cheapest plan that holds the pack. `publish` refuses only a pack no
  rentable plan can hold, and warns when the configured default cannot while a bigger
  plan can. `fallback_types` do not cover this: Hetzner falls back when a plan is sold
  out, not when it is too small.
- **`publish` tags the saved world with the pack.** The marker next to the world is what
  lets the next publish say "this world was made by Cobblemon, not Homestead". The same
  pack at a newer version is an update and keeps the world; a different pack is a
  question — `--new-world` discards it, `--keep-world` loads it anyway, and neither is
  assumed. A discard is the same operation as `outpost world delete`: the world goes to
  `trash/` first, verified, and a copy that fails aborts the publish with the old world
  exactly where it was.

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
more and not deleted: `outpost publish --profile default` brings the assets forward, and
`outpost world import <profile>` adopts the world (below).

### Worlds, and the one from before profiles

```bash
outpost world list        # every profile's world, plus any world no profile claims
outpost world import <id> # adopt one into a profile
```

```
* cobblemon-fabric           84.1 MB  Cobblemon Fabric 1.5.1 (modrinth/cobblemon-fabric)
  homestead                 11.2 MB  hand-rolled world
no profile claims these — `outpost world import <profile> --from <key>`:
  current/world.tar.gz      11.2 MB  no marker — nothing knows what made it
```

The `current/world.tar.gz` line is the world from before profiles, and it is the only
copy of six months of play: it is imported, never moved, and no command deletes it.
`world import` is a server-side `CopyObject` whose destination size is checked against
the source before it reports success — `r2.Get` stops at 8 MiB, so a read-and-write copy
would have written two thirds of the world and called it done.

It is idempotent, and a profile that already holds a world is left alone rather than
overwritten. The copy is refused while a server is rented, because a running session
uploads over its own profile's world when it shuts down. The marker written beside the
import is the profile's installed `pack.json` when there is one — so the next boot of
that pack loads the world without asking — and a hand-rolled marker otherwise.

### Vanilla

```bash
outpost pack vanilla && outpost publish packs/vanilla
# publish reuses the player list already in R2; on an empty bucket, add
# `--whitelist <names>` or add someone on the page first.
```

Or pick a version and press **Install Vanilla** in the panel. Either way it lands in the
profile `vanilla`, gets the same world, the same player list, and the same name in the
panel.

The version is a choice, and it is the only one vanilla has: `--mc 1.21.1`, or the select
beside the button, whose options are `publish.Minecrafts` — the same table the boot reads
for a JDK. Naming none writes `1.20.1`, deliberately: an unqualified command should not
change which game a world is. The select opens on whatever `vanilla` already holds, so a
reinstall stays a reinstall; choosing another version is a Minecraft change, which the
world guard refuses and answers with a profile of its own (`--profile vanilla-1-21-1`).

Vanilla here is **Fabric with an empty `mods/`**, not Mojang's server jar. A zero-mod
Fabric server plays as vanilla and accepts vanilla clients, so a second jar name and a
second `start.sh` would buy a difference no player can see — at the cost of a boot path
nothing else in the system knows about. What it *is* is a real `pack.json`
(`source: "outpost"`, `slug: "vanilla"`), which is what makes the world guard, the
pruning and the panel treat vanilla like any other pack. `pack.json`
says `loader: fabric` because that is what boots; "vanilla" is the absence of mods, and
the panel shows that as "Vanilla 1.21.1 · minecraft 1.21.1 · fabric" for a 1.21.1 one.

`outpost pack vanilla` refuses a directory that holds another pack, because `publish`
uploads what is in the directory and old jars would go up as a vanilla server's mods.

### Deleting a world, or a profile

```bash
outpost world delete cobblemon-fabric     # the world, keeping the pack installed
outpost world delete --key current/world.tar.gz   # an orphan, by the key `list` printed
outpost pack delete cobblemon-fabric --yes        # the whole profile
```

The order is the whole rule: **copy to `trash/` first, remove the live key second**, and
a copy that fails leaves the world exactly where it was. That is the shutdown path's
promise — never delete a world you could not back up — applied to the one operation that
cannot be retried. The trash expires on its own after 14 days, swept by the next delete
rather than by a timer; the age comes from the key, so an entry this code did not write
is left alone rather than guessed at.

`publish --new-world` — and the page's "replace the world" checkbox — is the same
discard under a different flag: the old world goes to `trash/` before it is removed, so
"start fresh" is recoverable too. The naming lives in `internal/trash` so the two
callers cannot drift, and `world list` skips the trash for both.

`pack delete` needs `--yes` and is refused on the **active** profile: deleting what
Start boots is a confusing way to learn what you did, so `outpost pack use <other>`
comes first. `world delete` on the active profile is allowed and says so, because Start
will simply make a fresh world. Both are refused while a server is rented — the session
uploads over its own profile's world when it shuts down.

The page does the same two things from each profile row's **Delete…**: it names the
world and its size, and the two buttons are "Delete world" and "Delete profile".

`outpost serve` is the only long-running piece:
`tailscale serve --https=8896` in front of it gives you a page at
`https://<host>.ts.net:8896/` — state, who is playing, the address to hand out,
what the session has cost, **Start / Save and stop / Update** buttons, a console,
and the player list. It polls `GET /api/overview` once every five seconds;
`GET /api/status` is a separate, narrower contract kept for Meridian's tile, so
add new things to `/api/overview` rather than to `status`. The Modpack panel is its own
`GET /api/pack`, polled every two seconds only while a preview or install is running.

The page is a SvelteKit app built to static files and embedded with `go:embed`
(the same shape as the rest of the box), so the box needs no Node at runtime: the
bundle is built inside the image from `web/src`.

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
it writes to R2. On the Potato the secrets are in `~/outpost/.env`, which compose
passes to the container — and the host `outpost` command runs *inside* that
container, so the credentials are already there:

```bash
outpost player add AutumnsAdventure   # no export, no sudo
```

## Building

```bash
make web        # build the frontend the binary embeds — needed once per clone
make build      # the daemon, for local work
make test
make potato     # linux/arm64, for one-off debugging on the Le Potato
```

`web/build` is **not** committed. The image build runs `pnpm build` from `web/src`
and copies the result into the Go build, so a deployed binary always embeds the
bundle its own commit produced. Locally it exists only so that
`go:embed all:build` has something to embed: run `make web` once after cloning, or
`go build ./...` fails with `pattern all:build: no matching files found`. CI does
the same before its Go steps.

The build is reproducible — `version.name` in `web/vite.config.ts` is a hash of
`web/src`, not the `Date.now()` SvelteKit defaults to — which is what lets
`.github/workflows/web.yml` build twice and diff, and what keeps image builds from
minting new chunk names.

Pure Go SQLite, no cgo, so both the cross-compile and the image build need no
toolchain gymnastics.

## Deploying

The Potato runs a published image, not a build of the checkout. GitHub Actions
builds `linux/amd64` and `linux/arm64` on every push to `main` and publishes
`ghcr.io/autumnsgrove/outpost:latest` plus a short-SHA tag
(`.github/workflows/docker.yml`); the box only ever pulls.

```bash
# once, from a checkout of this repo on the Potato (e.g. ~/outpost)
deploy/install.sh

# then, whenever you want the newest image
outpost update            # or the page's Update button, or Meridian
```

What is on the box, and what is in the image:

```
~/outpost/            the checkout: compose file, deploy/, nothing that runs
  data/               bind-mounted to /data, the container's working directory
    config.yaml         the settings (config.example.yaml has the comments)
    outpost.db          the only state
    packs/              fetched modpacks and the .mrpack cache
    backups/pre-update.db   the watcher's snapshot, restored by a rollback
  update-signal/      the mailbox: requested/check out, available.json/result back
  .env                the six credentials, 0600
```

`data/` is the whole state. An image is replaced by writing `OUTPOST_IMAGE` in
`.env` to a digest, `docker compose up -d`, and a health check — so no deploy ever
builds anything on the box, and the world in R2 never depends on one.

`outpost` on the host is a wrapper that runs the command **in** the container
(`docker compose exec`), so the database has one writer and the commands above work
unchanged. `deploy/install.sh` installs it at `/usr/local/bin/outpost`.

`deploy/install.sh` is also the migration off the systemd deploy, and it moves state
rather than starting beside it: it stops the old `outpost.service`, moves
`outpost.db` (with its `-wal`/`-shm`), `packs/` and `config.yaml` into `data/`, adds
`update_signal_dir` to a config that predates the mailbox, copies the Hetzner SSH
key to `data/ssh/outpost` and points `hetzner.ssh_private_key` at it, and disables
the old unit so a reboot cannot start a second daemon on the same database. A rented
server survives the move — the container picks up the same database and the same
state machine, which is why the old daemon is stopped before its files are touched.

The key has to move because the daemon is its own SSH client: `ssh` is a library call
now (`golang.org/x/crypto/ssh`), not a subprocess, because the image is distroless
and has no `ssh` binary, no `~/.ssh` and no `known_hosts`. Nothing is lost by that —
a host key on a VPS created ten seconds ago has nothing to be checked against
anyway.

### Updating, and why there is a watcher

The daemon cannot update itself any more, and must not be able to: it is a
container with a read-only filesystem and no docker socket. The thing that pulls a
digest, recreates a container and rolls back is a host script with exactly those
privileges — `deploy/watcher/update.sh`, started by a systemd path unit when the
daemon (or `outpost update`, or Meridian) writes `update-signal/requested`.

The watcher:

1. refuses while a Minecraft server is rented — recreating the daemon then would
   kill the loop that saves the world on logout;
2. snapshots the database through the daemon (`outpost snapshot`, `VACUUM INTO`);
3. resolves the newest digest, refuses an image reference of any other shape, pins
   it in `.env` and pulls;
4. recreates the container and waits up to 120 seconds for its own `/health` check;
5. on failure, puts the previous digest back **and** restores the database snapshot;
6. writes the outcome to `update-signal/result`, which the page shows, and removes
   the request on every exit path.

It is the same shape as Warbler's, Sepia's, Polaris's and Ivy's on this box, so
`systemctl start outpost-update.service` is what Meridian's update action runs.

## Costs

The only thing that bills is a running VPS, and Hetzner bills in euros. The page's
**Start** button has a size beside it, and each one carries its price:

```
Start  [ cx23 · 2 vCPU · 4 GB · €0.0096/h ▾ ]
       [ cx33 · 4 vCPU · 8 GB · €0.0144/h   ]
       [ cax11 · 2 vCPU · 4 GB · ARM · €0.0104/h ]
```

A `cx23` two-hour session is **€0.019** and a heavy month is under a euro; `cx33`
doubles the RAM for €0.0144/h, which is the one to pick if the modpack needs it. R2
holds a modpack and a world, well inside the free tier.

The numbers come from `internal/hetzner/plans.go` — one table of type, vCPU, RAM and
list price — and nowhere else. The rate is written onto the session row when it is
rented, so a later repricing or a config edit cannot rewrite what a past session cost.
`config.yaml` no longer has an `hourly_rate`; it is ignored if your file still carries
one.

Two caveats worth knowing before trusting any number here:

- **The prices include the primary IPv4** (€0.50/month), because a Minecraft server
  needs one to be reachable. They are Hetzner's list prices for new orders since the
  **15 June 2026** repricing, excluding VAT. A server keeps the rate it was created at,
  which is why the session stores it.
- **Instance size is the lever, not the provider.** Halving the RAM now saves 35%, not
  the threefold it saved in 2025 — Hetzner repriced the 8 GB tier downwards. The table
  carries the CX (x86) and CAX (ARM) lines only; CPX and CCX cost several times as much
  per GB and four players do not need dedicated vCPUs.

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
