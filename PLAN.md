# Outpost — v2 plan

> Bringing Outpost back as its own thing, outside the grove.
> Written 2026-11-11, before any code. Decisions first, reasoning under each.

## Where v1 got to

v1 (`legacy/v1/`) was a Cloudflare Worker (`mc-control`) plus 188 Fabric mods in R2,
a 38 MB world in R2, a D1 state table, and a 228-line bash `watchdog.sh` running
inside the Hetzner VPS.

It worked, and there are receipts: **12 sessions, 4.62 hours, $0.0565 total.**
Last session ended 2025-12-17. All of it is still deployed and intact — worker,
D1, both R2 buckets, and the world tarball.

None of that data is being carried forward. v2 is a fresh server on a fresh world;
the v1 buckets are history.

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

### 6. Keep R2's key layout, throw away its contents

R2 stays as a dumb blob shelf: no D1, no Worker, two buckets.

The **key layout** is load-bearing and stays exactly as v1 had it, because the
cloud-init template in `internal/cloudinit/` reads it:

```
mc-assets/server/{fabric-server-launch.jar,server.properties,ops.json,whitelist.json,eula.txt}
mc-assets/mods/*.jar
mc-assets/config/
mc-assets/scripts/{start.sh,stop.sh}
mc-worlds/current/world.tar.gz        (optional — absent means generate a new one)
```

The **contents** do not survive. Autumn decided on 2026-11-11 that the next server
is a fresh start: new modpack, new world, nothing carried over from December 2025.
That deletes a whole category of work — no data migration, no compatibility
testing, no "does the old world still load" — and it means the 298 MB of v1 mods
and the 38.8 MB world in R2 are disposable.

Missing assets are a first-class state, not an error: the template already handles
an absent world ("Minecraft will generate one") and an empty `mods/` prefix.

**Consequence:** R2 is populated by `outpost publish`, which fills both buckets
from the Mac over the S3 API (see README). The daemon writes exactly two things
there: the world, by rclone from the VPS at shutdown, and the player list.

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
        ├──► Hetzner cx23 in nbg1  ─ ephemeral, deleted on idle
        │      (cx33 is the 8 GB step; swapping is one line in config)
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

1. ~~Rename `AutumnsGrove/GroveMC` → `AutumnsGrove/outpost` on GitHub.~~ Done
   2026-11-11 (`gh repo rename`), with a fresh description. GitHub redirects the
   old URLs, but the old remote and any bookmarks now resolve to `outpost`.
2. Create a tagged, pre-authorised Tailscale auth key for `tag:outpost`.
3. `wrangler delete` the `mc-control` worker; remove the `mc-control` and `mc`
   entries from `grove-router`; drop the `Outpost` glossary term from
   `grove-term-manifest.json`; clean the stale `mc.grove.place` A and SRV records.
4. Deploy to the Potato: `docs/outpost.md` in the homelab repo, systemd unit,
   Meridian tile, entry in `service-healthcheck.sh`.

## Decisions from the hardening pass (2026-11-11, before the first real run)

Written while fixing the seven things that would have turned a bad first session into a
billing problem. None of them change the shape above; they close holes in it.

### 9. Renting is serialised, and `Start` refuses two situations outright

`outpost start` and `outpost serve` are two processes sharing one SQLite file, so
"is anything already running?" is not a question either of them can answer alone. `Start`
now takes an exclusive file lock (`outpost.db.lock`) before it reads state, and it asks
Hetzner what exists before it rents: a lost or restored database must not become two
billing servers. It also refuses to start from `ERROR` while a VPS is still recorded —
`ERROR` is exactly where a failed save leaves a server running, and renting over it would
orphan the world. The message names `outpost reset`, because that is the fix.

### 10. A server that never came up has no world, so it is not saved

`PROVISIONING` timing out used to fall into the normal shutdown path, which tried to tar a
`world/` directory that was never generated, failed three times, and landed in `ERROR` with
a VPS nobody had a shell on. The shutdown path now asks two questions first: did this VPS
ever answer RCON (recorded as `ever_running` when it reaches `RUNNING`), and does
`/opt/minecraft/world` exist. Only if both say yes does it try to save; otherwise it
deletes. A world that cannot be confirmed missing is still saved — that asymmetry is the
whole point.

### 11. Five minutes of RCON silence, not one

`maxRCONFailures` was 3 ticks, so 60 seconds of a stalled main thread, a tailnet rekey or a
laggy save would save and tear down a live session. The limit is
`server.rcon_failure_limit`, default 15 ticks (five minutes at the default interval): long
enough to ride out a blip, short enough that a dead server does not bill for an hour.

### 12. The VPS exposes one port, and its tailnet node is disposable

The cloud-init template no longer runs `ufw allow OpenSSH`. SSH and RCON are reached over
`tailscale0`, which the rules already permit; a public 22 contradicted "only 25565 is
public". `tailscale up` also takes `--ephemeral`, so each session's node leaves the tailnet
when the machine is deleted instead of accumulating dead devices. `hetzner.firewall_id` is
available for a second, API-side layer; ufw remains the floor.

### 13. `config.Load` parses, `config.Validate` gates

Reading state must not require cloud credentials. `Load` no longer validates, and each
command declares whether it needs them (`requiresCloud`): `serve`, `start`, `stop`, `tick`
and `reset` do, `status` does not. That is what makes `outpost status` usable on a box where
nothing has been exported yet.

### 14. A sold-out plan falls back to another size, not to a failed start

Hetzner's stock has been thin since 2026 and answers a plan it cannot place with
`412 resource_unavailable` rather than queueing. On the first real run that meant
`cx23` and `cx33` were both unavailable in `nbg1` and the session simply could not
start, even though `cax11` was sitting there. `regions.*.fallback_types` is now a
list tried in order; a real error (bad token, bad image) still aborts immediately
instead of walking the list. Only `resource_unavailable` triggers a fallback,
because only that one means "try another shape".

### 15. The control surface is a page, not buttons on the hub

Meridian is a front door: a tile is a link, and a link to a JSON endpoint is a
404 in a browser. Outpost gets its own page at `/` — state, players, the address
to hand out, session cost, recent events, and Start / Save-and-stop / Update —
built as a static SvelteKit site embedded with `go:embed`, the same shape as
bwmon and Polaris. That keeps the frontend build out of the box (`web/build` is
committed; `git pull && go build` stays the whole deploy) and keeps the
spending buttons one deliberate click away from the dashboard, where a stray tap
is less likely. Meridian keeps a link and a Restart button.

The page's POSTs carry a per-process form token. That is not authentication —
the tailnet is, as everywhere else here — it stops some other page in a browser
from submitting a form that rents a server.

### 16. The daemon updates itself, from the page or from the CLI

`outpost update` does `git pull --ff-only`, builds to `outpost.new`, renames it
over the running binary only if the build succeeded, then restarts the unit.
`--ff-only` because an edited checkout on the box should fail loudly rather than
create a merge nobody can explain; the rename-last because `Restart=always`
saves you from a crash, not from a binary that starts and loops; and it refuses
while a server is rented, because a restart mid-session is survivable but
pointless. Warm rebuild is about five seconds.

### 17. Publishing assets is a Go command, not a bash script

Rejected: reviving `legacy/v1/r2-manage.sh`. It would have been the third language in a
Go project, and the one piece of the system that decides what a server boots from. `outpost
publish` (R2 over the S3 API, Fabric launcher fetched from Fabric Meta, whitelist UUIDs from
Mojang) is the same code path on the Mac and the Potato, and it can be tested.

## Decisions from the review pass (2026-11-13)

### 18. The daemon writes the player list to R2

Issue #14 left this open: a runtime `op` over RCON is lost on the next boot, because
cloud-init restores `whitelist.json` and `ops.json` from `mc-assets/`. The bucket stays
the source of truth, and `internal/roster` is the one writer besides `publish`. Every
change goes to R2 first and is then applied to the running server over RCON, so a failed
write can never leave a change that dies with the VPS. A corrupt file is refused rather
than read as empty. Names are validated against Minecraft's own rules before they reach
a URL or an RCON command.

### 19. A world that exists must be restored, or the server must not start

The restore step used to treat any failed download as "no world in R2" and let Minecraft
generate a new one. Shutdown uploads whatever is in `/opt/minecraft/world` over
`current/world.tar.gz`, so a network blip at boot became a wiped world at shutdown. The
step now lists the bucket (which fails only for real reasons) and refuses to start
Minecraft when a world exists but cannot be restored. The orchestrator then times out and
deletes the server without a save, which leaves R2 untouched.

### 20. Stop and Reset wait for the lock; Reset will not destroy an unsaved world

Every tick writes back the whole state row it read at the start of its pass, so an
unlocked `Stop` that landed mid-tick was overwritten and the button did nothing. Stop and
Reset now wait for the tick. Reset also refuses to delete a server whose world was never
uploaded, or servers this database is not tracking, without `--force`.

### 21. The console cannot end the session, and the page cannot be driven cross-site

`stop` and `save-off` are refused anywhere in a console line (so `execute run stop` does
not get round it): the orchestrator would read a dead server and correctly save and delete
it. All POSTs refuse browsers' `Sec-Fetch-Site` values other than same-origin; without
that, `/api/start` was open to any web page visited from the tailnet when no bearer token
is set.

### 22. Not done, on purpose

- SSH uses `StrictHostKeyChecking=accept-new` with the default known_hosts. A recycled
  tailnet address with a new host key would fail the world save. Loosening host-key
  checking is a security decision, so it is left for a human to make.
- Secrets still travel in cloud-init user-data, which any local user on the VPS can read
  from the metadata service. Scope the R2 token to the two buckets (the handoff already
  assumes this) and treat the VPS as trusted with it.
- If the Potato itself is down, nothing deletes the VPS. A Hetzner spending alert is the
  backstop.

## Decisions from the modpack branch (2026-11-15)

Issue #12. Phase 1: Modrinth packs on Fabric, done at publish time. The first branch
taken off `main` after the in-game commands work was parked, because `/outpost` needs
mods installed to be reachable and a pack is what installs them.

### 27. `pack fetch` produces a directory, and `publish` stays dumb

`outpost pack fetch <slug|url>` downloads the pack's server files into an ordinary pack
directory and `outpost publish` uploads it. That is the whole seam: `internal/pack` is
the only package that knows Modrinth exists, `publish` still uploads a directory and
verifies key sizes, and neither imports the other's network clients.

What the pack directory gains is one file, `pack.json`: source, slug, pack version,
Minecraft version, loader version, file count, and the RAM figure. Its absence is not
an error — a directory of hand-picked jars is still a pack — which is what keeps
`publish` usable for a one-off server.

`outpost pack fetch` needs no config.yaml, no database and no credentials. It carries
the `noConfig` annotation, so it works on a fresh clone before anything has been set
up, which is the state a person is in when they have just decided what to play.

### 28. `env.server` replaces client-only-mods.txt, for packs only

A `.mrpack` marks every indexed file with `env.client` and `env.server`, where
`unsupported` means "this side must not load it". That is the pack author saying it,
which beats 118 filenames carried over from the December 2025 pack. For a directory
with a `pack.json`, the hand-maintained list is not consulted at all: applying it to a
pack would silently drop a mod the pack marked server-required because an old filename
happened to match.

It stays for hand-rolled packs, because inventing a second mechanism for them is worse
than the one that already exists.

The corollary is stated rather than papered over: a pack that marks *nothing*, like
Fabulously Optimized, gets every file installed on the server. `pack fetch` warns about
that instead of guessing from filenames.

### 29. A pack's non-mod files live in `root/`, and it is pulled before `server/`

Overrides put `config/`, `kubejs/`, `defaultconfigs/` and friends at the *server root*,
which the v1 key layout had no prefix for. Rather than teach cloud-init a list of
directory names, `mc-assets/root/` is rclone-copied to `/opt/minecraft/` and everything
in the pack that is not a `mods/` file goes there.

Order matters and is deliberate: `root/`, then `server/`, then `mods/`, `config/`,
`scripts/`. Our generated `server.properties` is in `server/`, so it lands after a
pack's own copy and wins on port, RCON and whitelist enforcement. cloud-init's `sed`
lines then force the session's RCON password over the top. A pack's other
`server.properties` settings are therefore lost, and `pack fetch` says so in its
warnings — a merge is a follow-up, not a guess to make here.

### 30. RAM is a person's judgement, and the fallback list does not cover it

The `.mrpack` manifest has no memory figure, so `pack fetch --memory` (default 4 GB,
the `cx23`) writes one into `pack.json` and sizes `start.sh` from it: two GB stay with
the OS, the JVM and the save, and the rest is `-Xmx`. A fetched pack therefore always
lands with a `start.sh` sized for the box, instead of publish's 2 GB vanilla default.

`publish` refuses a pack no configured region can hold, and warns about the regions
that cannot while others can. It does **not** trust `fallback_types` here, and the
message says why: Hetzner answers `resource_unavailable` when a plan is *sold out*,
not when it is too small, so a 4 GB `cx23` with an 8 GB pack is a rental that runs out
of memory on its first save.

Plan memory is a table in `internal/hetzner/plans.go` rather than an API call, so
`--dry-run` works with no network and `publish` keeps needing only R2 credentials. A
plan the table does not know skips the check: a stale table must not refuse a rental.

### 31. The saved world is tagged with the pack that made it

`publish` writes `mc-worlds/current/pack.json` next to `current/world.tar.gz`. The
world itself is pushed up by rclone from the VPS and knows nothing about packs, but the
marker survives because shutdown only overwrites `world.tar.gz`.

A world from another pack is refused, not loaded: block ids and registry entries differ
between packs, so a missing mod reads as corrupted chunks. `--new-world` discards it,
`--keep-world` loads it anyway, and neither is the default. A world with no marker at
all — the vanilla world from the first session, or one from a hand-rolled pack — is
also refused, because "probably fine" is not a thing to decide on someone's world.

The marker is written, and the old world removed, only *after* every asset is uploaded
and verified. A failed publish must not leave a bucket that claims a pack it does not
have, and must not leave no world to boot.

### 32. Modrinth only, in phase 1. CurseForge and Forge are deliberate nos

Modrinth's API needs no key, its packs are open files with checksums, and the same
`.mrpack` is what Prism installs — so "hand over a link" is the entire workflow.
CurseForge needs an API key, some packs forbid third-party distribution, and its
manifests carry project ids rather than URLs. Forge and NeoForge ship an *installer*
that has to run with a JDK before the server can boot, which is a decision about where
that JDK lives (the Mac at publish time, or the VPS at boot) and not something to
smuggle into this branch. FTB stays out: no clean public API for server files.

`pack fetch` says which of those it hit rather than downloading something surprising.

### 33. Not done, on purpose, on this branch

- A pack's `start.sh` flags (Aikar, `-XX:+UseZGC`) are ignored; the script is generated
  from the memory figure. Packs that need specific flags will need a way to pass them.
- A pack's `server.properties` is replaced wholesale by ours (see 29).
- Client-only detection is entirely the pack's word (see 28), and packs do get it
  wrong: Cobblemon's official pack marks FancyMenu, CraftPresence and InvMove as
  server-required, which drags 122 MB of FancyMenu UI assets into `config/` and onto
  R2. Following the pack is still the right default — the alternative is a filename
  list that guesses — but a pack that lies about sides is a pack whose server build
  is heavier than it needs to be, not a broken one.
- No live session yet. A real Fabric pack booting on a real VPS with a Prism client on
  the same pack version is the test that counts, and it is the next thing to run.
