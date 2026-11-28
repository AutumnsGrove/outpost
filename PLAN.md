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
        │      cloud-init: tailscale, the pack's JDK, rclone pull from R2,
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

Issue #12. Phase 1: Modrinth packs on Fabric, done at publish time. This went first,
before the in-game commands work that is still parked in its own branch, because
`/outpost` is unreachable until a server has mods installed — and a pack is what
installs them.

### 23. `pack fetch` produces a directory, and `publish` stays dumb

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

### 24. `env.server` replaces client-only-mods.txt, for packs only

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

### 25. A pack's non-mod files live in `root/`, and it is pulled before `server/`

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

### 26. RAM is a person's judgement, and the fallback list does not cover it

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

### 27. The saved world is tagged with the pack that made it

*(Superseded by 30: the marker now sits at `mc-worlds/profiles/<id>/pack.json` and the
comparison is by pack rather than by build. What follows is how it worked with one
world, which is why the profiles layout was worth the change.)*

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

### 28. Modrinth only, in phase 1. CurseForge and Forge are deliberate nos

Modrinth's API needs no key, its packs are open files with checksums, and the same
`.mrpack` is what Prism installs — so "hand over a link" is the entire workflow.
CurseForge needs an API key, some packs forbid third-party distribution, and its
manifests carry project ids rather than URLs. Forge and NeoForge ship an *installer*
that has to run with a JDK before the server can boot, which is a decision about where
that JDK lives (the Mac at publish time, or the VPS at boot) and not something to
smuggle into this branch. FTB stays out: no clean public API for server files.

`pack fetch` says which of those it hit rather than downloading something surprising.

### 29. Not done, on purpose, on this branch

- A pack's `start.sh` flags (Aikar, `-XX:+UseZGC`) are ignored; the script is generated
  from the memory figure. Packs that need specific flags will need a way to pass them.
- A pack's `server.properties` is replaced wholesale by ours (see 25).
- Client-only detection is entirely the pack's word (see 24), and packs do get it
  wrong: Cobblemon's official pack marks FancyMenu, CraftPresence and InvMove as
  server-required, which drags 122 MB of FancyMenu UI assets into `config/` and onto
  R2. Following the pack is still the right default — the alternative is a filename
  list that guesses — but a pack that lies about sides is a pack whose server build
  is heavier than it needs to be, not a broken one.
- No live session yet. A real Fabric pack booting on a real VPS with a Prism client on
  the same pack version is the test that counts, and it is the next thing to run.

## Decisions from the profiles branch (2026-11-15, after the pack panel)

The ask: paste a Modrinth link into the page, see what is in it, install it — and
be able to keep **a world per pack** and swap between them. "A world on Cobblemon,
then one on Homestead, hit a button, and swap the whole thing."

### 30. A profile is a pack and its world, and the whole layout is scoped by it

```
mc-assets/profiles/<id>/{pack.json,server/,mods/,config/,scripts/,root/}
mc-worlds/profiles/<id>/{world.tar.gz,pack.json,backups/...}
```

The active profile is a row in `meta`, not in `config.yaml`: it is state ("what am I
playing"), and the page switches it. The profile is fixed at rent time and recorded on
the session row, so the boot pulls one profile's assets and the shutdown uploads to that
same profile even if the active one changes an hour later. Switching is refused while
anything is rented, and `Start` refuses a profile with no server files in R2 rather than
renting a VPS that pulls nothing, generates a vanilla world, never answers RCON, and
times out fifteen minutes later with a bill.

This also retired a question the single-world layout could not answer: with one world,
"which pack made this" was guesswork at publish time. With profiles it is structural —
the world belongs to the profile, and the profile is the pack. The guard survives
*inside* a profile, where re-publishing is still possible, and it now compares the pack
rather than the build: 1.5.1 → 1.5.2 is an update that keeps the world, which is the
normal reason to re-publish. Only a different pack is a question, and the page asks it
with a checkbox while the CLI asks for `--new-world` or `--keep-world`.

The profile id comes from the pack's slug (`cobblemon-fabric`), so pasting a link needs
no naming step. `--profile` overrides it, and a hand-rolled pack directory defaults to
`default`, which is also what a database that has never been told otherwise boots.

**What this costs:** disk in R2, deliberately. Two profiles in R2 is two copies of ~300
MB of mods that never change. That is inside the free tier, and it is the price of a
swap that takes one click instead of a re-upload.

**What it does not do:** the old single-profile keys (`mc-assets/server/`, `mc-assets/mods/`,
`mc-worlds/current/`) are not migrated and not deleted. They stop being read. Re-publishing
the current pack with `outpost publish --profile default` is the one command that brings
the old assets forward, and the old world is left where it is as an archive rather than
guessed at.

### 31. The player list stays global, and cloud-init copies it last

Whitelist and ops are one file for the whole server: adding a friend should add them to
every world, not once per pack. So `server/whitelist.json` and `server/ops.json` are the
two keys `publish` does **not** scope by profile, `internal/roster` keeps being their only
writer, and cloud-init copies them after the profile's files — so a pack that ships its
own whitelist does not get to decide who may play.

`outpost publish` reuses those two keys byte for byte when it is not handed names: a
re-publish should not re-resolve the same players through Mojang (which rate-limits), and
an op set recorded on the page ought to survive a new pack version. `--whitelist`/`--op`
replace the list deliberately — that is the one case where names are resolved — and
`--open` publishes with `white-list=false` and no list at all.

The one file whose absence is legitimate is exactly these two. The template says so out
loud rather than swallowing the error.

### 32. Installing and switching are daemon jobs, because the page is the only client

`outpost pack fetch` runs on the Mac and needs no config; the page cannot. So the daemon
grew a `pack.Manager` — profiles from R2, a preview that downloads only the `.mrpack`,
and an install that fetches into `packs/<profile>/` and publishes from there — and the
CLI uses the same manager for `pack list`. Two front doors, one implementation, and the
rules live where they can be tested: one job at a time, nothing while a server is rented,
the archive cached so preview-then-install downloads it once.

The job outlives the request that started it (`context.WithoutCancel`), for the same
reason renting does: a browser tab closed mid-install must not leave half a profile.
Progress is a phase plus two counters, which is enough for "downloading mods — 86 of 231"
and no more.

A pack's RAM figure is still a judgement (`--memory`, default 4 GB); the manager passes
it to the same `Inspect` the CLI uses, so the refusal and the warning are the same
sentence in both places.

### 33. A world is copied, never read, and never moved

`outpost world import` adopts a world that no profile claims, and everything about its
shape follows from one number: `r2.Get` stops at 8 MiB. The pre-profile world is 11.7 MB,
so a copy written as a read and a write silently wrote 8,388,608 bytes and reported
success — caught only by a `Stat` afterwards.

So `r2.Client.Copy` is a server-side `CopyObject`, and it measures the destination against
the source before returning. The bytes never cross the potato, there is no size ceiling,
and a partial copy is an error rather than a world. `world.Import` never calls `Get` on a
world at all; the tests point the fake store's `Get` at the world key to prove it.

The source is never deleted, and the import is idempotent: a profile that already holds a
world is left alone. That is the same rule as "never delete a world you could not back
up", stated for the other direction — the copy you did not make is the one you still have.

The marker written beside the import is the profile's installed `pack.json` when there is
one, because that is the pack that will boot this world, and the guard then sees a perfect
match instead of an unknown. With no pack installed, the marker says hand-rolled (no
source, no slug), which is compatible with any hand-rolled publish and is something to be
asked about before a Modrinth pack claims a world it did not make.

The import is refused while a server is rented, for the reason installing is: a live
session uploads over its own profile's world at shutdown, so a copy placed underneath it
is a copy that disappears when they log off.

### 34. Vanilla is a pack with no mods, not a second kind of server

There are two readings of "vanilla" and the choice is not cosmetic: the real server jar
from Mojang's version manifest, or Fabric with an empty `mods/`.

It is Fabric. A zero-mod Fabric server plays as vanilla and accepts vanilla clients, so
the difference is invisible to a player, while the jar would mean a second jar name in
`Build`, a second `scripts/start.sh`, and a second boot path that cloud-init, the
provisioning timeout and the orchestrator all have to tolerate. "No logic in bash" has a
sibling: no second way to boot.

What vanilla *does* get is an identity. `publish.VanillaPack(mc)` is an ordinary
`pack.json` with `source: "outpost"` and `slug: "vanilla"`, so vanilla is a profile like
any other — the world guard claims its world (`SamePack` is source+slug, so a vanilla
world is never claimed by a Modrinth pack and vice versa), `prune` empties `mods/`, and
`world import` can name what will boot.

`loader: "fabric"` is the honest field. The pack is Fabricked and says so; "vanilla" is
the absence of mods, which `mods: 0` and the name say plainly. The panel reads it as
"Vanilla 1.21.1 · minecraft 1.21.1 · fabric", which is exactly what to match in Prism.

**The version is a parameter, and the default is still row zero.** Vanilla has no project
and no versions to resolve, but it is still one game version or another, and the image
boots every row of `Minecrafts` — so the panel's button carries a select of the table and
`outpost pack vanilla --mc` takes the same answer, rather than hardcoding 1.20.1 into the
one install path that has nothing else to ask. Empty means `SupportedMC`, for the reason
`pickVersion` defaults to it: an unqualified command must not replace a world. The
select's *initial* value is the version the vanilla profile already holds, so a reinstall
is a reinstall; picking another one goes through the same world guard as a pack, which
refuses and suggests a derived profile id (`vanilla-1-21-1`). A version outside the table
is refused by `CheckPack` before the job starts, in the panel, in the CLI and in the API.

The panel's install writes into `packs/_vanilla` and empties it first, deliberately not
into `packs/vanilla`: that directory may still hold a pack fetched under the same name,
and publishing it would send those jars up as "vanilla". `outpost pack vanilla` refuses
a directory holding another pack for the same reason.

### 35. Deleting copies first, and the copy expires on its own

Deleting is the one operation where being wrong cannot be retried, so three things
about its shape are decisions rather than implementation.

**Copy to `trash/`, remove second.** `trash/<name>-<stamp>.tar.gz` is written with the
same server-side `CopyObject` the world import uses, size verified, and the live key is
removed only after that returns. A failed copy aborts with the world exactly where it
was — the shutdown path's rule ("never delete a world you could not back up") applied in
the other direction. The copy also means the page's confirmation does not have to be
scary: the world is recoverable, so being clear about *which* world matters more than
making someone type its name.

**The trash expires by key stamp, swept on the next delete.** 14 days, the same number
the shutdown upload uses for `backups/` (`rclone --min-age 14d`). There is no timer: the
trash grows exactly when something is deleted, which is exactly when it should be
emptied. The age comes from the key, so a trash entry this code did not write is left
alone — guessing at the age of an object it did not name would delete somebody else's
thing.

**Two commands, because "delete" is two different wants.** `outpost world delete` keeps
the pack installed and starts a fresh world next time (the common one: the world is
broken, the pack is fine). `outpost pack delete --yes` is the profile — assets, backups,
marker, and the world through the trash — for a slug that was typed wrong. `--force` is
deliberately absent: this is not the "servers this database does not track" case `reset
--force` exists for, and the confirmation should be about *which* world, not about
skipping a check.

`publish --new-world` — and the page's "replace the world" checkbox, which is the same
flag — discards through the same `trash/` copy, with the same 14-day sweep. It was the
one remaining path that destroyed a world in a single step, and it is the one a person
reaches for casually ("this pack made a world I do not want, start fresh"). The naming
lives in `internal/trash` rather than in either caller, because two packages write those
keys — `world` deletes, `publish` discards — and a drift between them would leave
`world list` advertising entries it should skip and a sweep that never empties.

The **active** profile is refused by `pack delete` and allowed, with a note, by the
world delete. Deleting what Start boots leaves the next session failing at "profile X has
no server installed", which is a confusing way to find out what happened; deleting the
active *world* just means the next session makes a new one, which is what someone
deleting a world asked for.

Everything here is refused while a server is rented: a live session uploads over its own
profile's world at shutdown, so a delete underneath it is a delete that undoes itself.

### 36. The plan is a choice, and the rate belongs to the session

Until now `server_type` lived in `config.yaml` per region and the only thing that moved
it was an edit and a restart, so a pack that wanted 8 GB could not be started from the
page at all, and the page could not say why. Three decisions make the size a choice.

**One table, in Go.** `internal/hetzner/plans.go` holds type, vCPU, RAM and EUR/hour for
the CX and CAX lines; `region.hourly_rate` is gone from the config. Two tables is how
the readout and the RAM guard start disagreeing, and the guard is the one that matters:
it refuses a start that would run out of memory. The prices are Hetzner's list prices
including the IPv4 a server needs, for new orders since the June 2026 repricing, and they
are a snapshot: a plan the table does not know is "unknown", never "too small".

**`meta.server_type` is the last choice.** The page picks, `outpost start --type` picks,
and the choice is remembered in the meta table rather than written back into
`config.yaml`. An empty choice resolves to the remembered type, then to the region's
`server_type`.

**The rate is recorded on the session.** `sessions.hourly_rate` is written at rent time,
and every cost the system reports — `Status.SessionCost`, the month, the session row —
reads it. Hetzner keeps a server on the price it was created at, so looking the rate up
later would report a number that was never charged. A row written before the column
existed reads 0 and falls back to the plan table rather than claiming the session was
free.

Two consequences worth stating:

- **The picker defaults to the cheapest plan that holds the active pack**, from the
  pack's own `memoryGB`, and a start refuses a plan below it with the plan that would
  work named in the refusal. `publish` asks the same table: it refuses only a pack no
  rentable plan can hold, and otherwise warns that the configured default is too small
  while naming the plan to pick.
- **`fallback_types` keeps its old meaning** — sold out, not too small — and the example
  config now says so, because the old comment read like a size menu.

### 37. A Minecraft version and its JDK are one row, and the boot reads the pack

`SupportedMC` was one constant and cloud-init installed `openjdk-17-jdk-headless`. Adding
1.21 that way would be two facts that must not drift — which Minecraft version a pack is
for, which JDK the image installs — and drift here is the expensive kind: the wrong JDK
is "Unsupported class file major version 65" at boot, an orchestrator that reads it as a
server which never came up, and a deleted VPS twenty minutes into a rental. So
`publish.Minecrafts` is a table of rows, `{MC, Java}`, oldest first, and `SupportedMC` is
row zero.

**A row is a promise, not a fact about Minecraft.** `1.20.1 → 17` and `1.21.1 → 21` are
in it because somebody will boot them. It is not derived from a version family: "1.20.4
is probably 17 as well" is exactly the guess that turns a working image into a rental
that dies at boot, and a refusal is cheaper than a guess. Adding a row means adding a
JDK, a Fabric build and a boot to the list of things that have been tried.

**The JDK is chosen per profile, at rent time.** `Start` already reads
`profiles/<id>/pack.json` for the pack's RAM figure; it reads the same file for `mc`,
looks up the row, and passes `Java` to cloud-init, which installs exactly one JDK. Both
JDKs would be ~200 MB on every rental and a second place for the boot to disagree with
the pack. A profile with no manifest, or one whose manifest names no Minecraft version,
gets `SupportedMC`'s row: a JDK is one package, and the loud failure is at publish, not
at boot.

**Everything that asks "can this run" reads the table.** `CheckPack` accepts a pack whose
`mc` is a row and refuses one outside it naming the rows; `pack fetch` refuses it before
the download; the picker marks a row bootable or greys it out with the same wording. The
greyed-out rows are the point (issue #22): hiding a 1.21.4 build is how somebody concludes
the pack has no 1.21 build at all.

**`pickVersion`'s default stays `SupportedMC`, not "newest runnable".** Adding a row must
not change which Minecraft version an unqualified `pack fetch` installs — that is a
silent world replacement for anybody who typed the command twice. Choosing is explicit:
a version id in the panel, or `--version` on the command line.

**"An update is available" is scoped to the world.** With two runnable versions, the
newest runnable build of a pack is no longer necessarily an update: a 1.21.1 build is a
different game than the 1.20.1 world in the profile. `Versions` reports `Newer` from the
versions matching the Minecraft version the profile holds, so the row does not advertise
a build that would replace the world instead of updating it.

**A Minecraft version change inside a profile is refused.** Same pack, same slug, newer
version — an update, the world comes along (30). Same pack, different Minecraft version —
refused by the world guard, which suggests a derived profile id (`fabulously-optimized-1-21-1`),
lets `--new-world` discard the world, and lets `--keep-world` load it anyway with a
warning. This is the one pack change that cannot keep a world, and it is now sayable
before the upload rather than discovered at boot.

**A hand-rolled directory gets a generated `pack.json`.** `source` and `slug` stay empty
on purpose, so two hand-rolled builds are not "a different pack" to `SamePack` and
republishing a curated directory over its own world keeps working. What it does carry is
`mc` (from `--fabric`, else `SupportedMC`) and `fileCount`, which is what the page, the
plan guard and the JDK all read — and what makes a hand-rolled 1.21.1 build
distinguishable from a 1.20.1 one. No `fetchedAt`: nothing was fetched, and a timestamp
would make every republish of an unchanged directory upload a new manifest.

That has one consequence worth stating: dropping a hand-rolled directory into a profile
that holds a fetched pack's world is now *refused*, where it used to delete the marker
and the manifest and boot the new jars over the old world silently. It is the same rule
as any other pack swap — the world guard asks whose world it is — and `--keep-world` is
the door for somebody who means it.

### 38. The box pulls an image, and the daemon cannot update itself

Outpost was the last project on this box that committed its build output and built
itself on the Potato. Ivy, Warbler, Polaris and Sepia all run a multi-stage image
built in CI and pulled by digest, and Warber's Dockerfile says why in one line: a
cold build on that box *"peaks near 730 MiB and exhausts swap"*. Outpost's deploy was
`git pull --ff-only && go build` **on that box**, from the page's Update button and
from Meridian, with `internal/updater` hand-rolling the "build to a temporary name,
swap only on success" pattern that an image plus a watcher already provides. So the
deploy is now an image, and the work that made the committed bundle *safe* (#36's
pinned `version.name`, `make web-check`, a workflow whose only job was to prove the
committed bundle matched the sources) is gone with the bundle.

Dockerfile, `docker-compose.yml` at the repository root (Meridian's compose targets
run in the project folder), `.github/workflows/docker.yml` publishing
`linux/amd64,linux/arm64` to ghcr, and `deploy/watcher/update.sh` on the host. The
shape is Warbler's, deliberately: this box has one way to deploy a service and a
second way is a second thing to remember at 1am.

Four decisions here did not have a sibling to copy.

**The CLI runs in the container, behind a host wrapper.** `outpost publish` and
`outpost start` both open `outpost.db`, and one SQLite file with two writers whose
only guard is `TryLock` is a shape that works until it does not. So every command
runs in the container — `docker compose exec` — and `/usr/local/bin/outpost` on the
host is a two-line wrapper that does exactly that, so the commands in the README
keep working verbatim. The alternative (daemon in a container, CLI on the host) was
the least work and the worst answer: it puts a second process on the database to
save nobody an ergonomic change.

**`web/build` is not committed, and Node is required for a clean Go build.**
`go:embed all:build` matches nothing when the directory is absent, so a fresh clone
cannot `go build ./...` until `make web` has run. That is Polaris's choice over
committing a placeholder, and the reason is the failure mode: a placeholder makes a
UI-less binary build *silently*, and a UI-less binary deployed by mistake looks like
a broken page rather than a broken build. CI installs Node and builds the frontend
before its Go steps, because it hits the same wall.

**Updating is a mailbox, not a process.** The daemon writes `update-signal/requested`
(the literal word `latest`, never a digest — it cannot name an image) and the host
watcher does the rest: resolve the digest, refuse an image reference of any other
shape, snapshot the database, pin, pull, recreate, poll `/health` for 120 seconds,
and on failure put the previous digest back *and* restore the snapshot. `outpost
update`, the page's button and Meridian all end in the same file, and the outcome
comes back in `update-signal/result`, which the page shows. `internal/updater` is
deleted; its three rules do not carry over because the image is either built or it
is not.

The one rule the watcher needs that the siblings do not: **it refuses while a
Minecraft server is rented.** Warbler's "busy" is a few seconds of upload and it
waits; outpost's busy is a billed VPS with a world in memory, and recreating the
daemon then kills the loop that saves that world on logout.

**The tailnet is asked over the socket, not the CLI.**
`internal/tailnet` used to shell out to `tailscale status --json`. A distroless image
has no such binary, so it now reads `GET /localapi/v0/status` over a mounted
`tailscaled.sock` — the same document, parsed by the same function — and falls back
to the CLI where the socket is not where expected. Mounting the CLI in instead would
have meant a Debian runtime, a tailscale package and its dependencies, for one JSON
document.

The trade-off is real and worth naming: the LocalAPI is more than the status
document — it is the interface `tailscale up`/`down`/`logout` use — so whatever runs
in that container could drive the tailnet. What holds it is the rest of the compose
file (uid 1000, read-only root, no capabilities, no docker socket) and the tailnet's
ACLs. A read-only mount does not narrow a socket, because connecting is not a write
through the mount; a proxy for `/localapi/v0/status` alone is the next step if that
posture is not enough.

**The migration moves state; it does not start beside it.** The old unit ran with
`WorkingDirectory=~/outpost` and `database: "outpost.db"`, so the live database was
at the checkout root and the container can only see `data/`. `deploy/install.sh`
therefore stops the old daemon *first*, then moves `outpost.db` (with `-wal`/`-shm`,
because moving one out from under a writer is how a database becomes a database plus
a corrupt WAL), `packs/` and `config.yaml` into `data/`, and appends `update_signal_dir`
to a config that predates the mailbox — otherwise the Update button would be hidden
immediately after the watcher was installed to serve it. A rented VPS survives the
move: the container opens the same database and resumes the same state machine.

The compose file has no `build:` key on purpose. Compose builds instead when the
image is missing, so a build key would turn the first `up -d` — and any rollback to a
pruned digest — into a cold build on the Potato, which is the thing the image exists
to avoid.

**SSH is a library, not a subprocess.** `internal/orchestrator` reaches the VPS
over SSH for the part RCON cannot do — tar the world, rclone it to R2, confirm it
landed — and `internal/remote` used to exec the system client. A distroless image has
no such client, so the package speaks SSH itself (`golang.org/x/crypto/ssh`). The key
moves with the state: `deploy/install.sh` copies it to `data/ssh/outpost` and points
`hetzner.ssh_private_key` at it, because the container has no home folder. Host keys
are not verified, and cannot be: every VPS is new and its host key is one nobody has
seen, so what protects that channel is the tailnet plus the key that is the only way
to authenticate. The alternative — a Debian runtime with `openssh-client`, a key
mount, and a known_hosts question that has no good answer — is more moving parts for
one command runner.

Two small things the container needed and could not get from a shell: `outpost
healthcheck` (the image's `HEALTHCHECK`, which asks its own `/health` — distroless
has no curl, and "the process is alive" is not the question the watcher asks before
keeping a new image) and `outpost snapshot` (`VACUUM INTO`, because a `cp` of a
WAL-mode database is not a consistent copy). The daemon's `WORKDIR` is `/data`, so
`config.yaml`, `outpost.db` and `packs/` are relative paths from one bind mount and
the deployed config is the file the repo documents.

Retired: `deploy/outpost.service` (the container is the unit), `internal/updater`
and its tests, `cmd/outpost/update.go`'s git path, `web.yml`'s committed-build job,
`make web-check`. Kept: the `deterministic` job (two builds of one tree must match —
that property is what makes an image reproducible, wherever the bytes end up), and
`version.name`'s source hash, which is the client's cache token.

### 39. Not done, on purpose, on this branch

- Two worlds on the *same* pack: a profile is one pack, so a second Cobblemon world needs
  a second profile id (`--profile cobblemon-second`), which the page has no field for yet.
- Undoing a delete from the page: the trash holds the world for 14 days, but restoring it
  is a copy back by hand (`rclone` or a Go one-liner), not a button.
- The world itself is not versioned inside a profile: a pack update keeps the world,
  which is right for 1.5.1 → 1.5.2 and wrong for a pack that changes world generation.
  A *Minecraft* version change is refused (37); a pack that silently changed the world
  generator is not detectable from a `pack.json`. The backups in
  `profiles/<id>/backups/` are the answer for now.
- The old single-profile keys are left in place (see 30). `outpost world import` is the
  door to `mc-worlds/current/`, but nothing imports it on its own: a world moved without
  being asked for is a world moved by surprise.

### 40. A mod that says it cannot load without another wins over a client-only label

Homestead 2.0.1 marks `chipped` required on both sides and `athena`, which Chipped
declares it will not load without, `server: unsupported`. Obeying the pack's side
annotation (24) therefore produced a mod set that Fabric Loader refuses, which is a
server that never answers RCON: a rent that reads "Building" until the provisioning
timeout deletes it, with nothing in the daemon's log saying why.

`pack fetch` now reads the dependency block out of every jar it installs — including
the jars nested in `META-INF/jars/`, because that is where Fabric API's thirty-odd
modules and every bundled dependency live — and asks whether anything in the pack
provides what those mods say they require. `minecraft`, `java` and `fabricloader` are
not mods and do not have to be installed.

- A gap that a *skipped* file can close is closed: that file is fetched, read for the
  ids it provides, and kept only if it is needed. The rest of the staging directory is
  deleted. The pack's annotation is overruled, the warning says so, and the server
  boots. A client mod that is present and unused costs a few hundred kilobytes; a
  server that will not boot costs a session and a rent.
- A gap nothing in the pack can close is an error, and the fetch fails with the mod,
  the file it came from, and the version range it wanted. That is the expensive case
  turned into a message: it costs a re-read of the source, not a VPS.

The extra download is paid only by a pack that has a gap, because the scan runs on what
was already fetched. A pack whose kept mods are satisfied — nearly all of them —
downloads nothing more than before.

Not covered: a mod that *recommends* something absent is normal and is still ignored
(the Fabric loader only warns), CurseForge-hosted mods a pack cannot redistribute are
not fetched from anywhere, and `--plan-only` cannot scan jars it did not download.

Two things the first draft got wrong, found in review, are part of 40 rather than a
follow-up: a jar that answers for more than one id is moved into `mods/` exactly once
(it was renamed a second time from a path it had already left, which failed a pack that
was satisfiable), and a nested jar is read whole rather than under the `fabric.mod.json`
cap (a bundled library over 4 MiB was silently invisible, so its ids looked missing).
A scan that could not read a jar is not evidence of a missing mod: `scanModDeps` reports
whether it saw everything, and when it did not, unexplained gaps become warnings and the
boot decides. Only a scan that saw every byte may refuse a pack.

### 41. Java 25 arrives as a tarball, because Ubuntu stops at 21

Minecraft 26.x needs Java 25. Fabric's own metadata says so (`fabric-api` for 26.1, 26.2
and 26.3 declares `"java": ">=25"`, while 1.20.5 through 1.21.11 declare `>=21` and
1.17 through 1.20.4 declare `>=17`), and Ubuntu 24.04 packages no JDK newer than 21. The
old table said "only the majors Ubuntu ships are accepted", which was a true statement
about apt and a false one about what can be installed: it made 26.x unbootable by
construction, and the panel greyed out every pack build for it.

So there are two ways to get a JDK and `internal/cloudinit` now knows both. A major in
`javaMajors` (8, 11, 17, 21) is a package, as before. A major in `javaTarballs` (25) is
fetched from Adoptium's API in `runcmd`, extracted to `/opt/jdk-<n>`, and linked into
`/usr/local/bin/java`, which is ahead of `/usr/bin` on the default PATH — `scripts/start.sh`
runs a bare `java`, so nothing else has to learn about it. Both tables are validated
before anything is rendered: a major in neither is refused with the list, not with a boot
failure twenty minutes into a rental.

Two details worth writing down:

- **The API's architecture names are not debian's.** `dpkg --print-architecture` says
  `amd64`/`arm64`; Adoptium wants `x64`/`aarch64`. A rent may land on either, because the
  server type follows capacity (`cx33` and `cax11` are both fallbacks), so the mapping is
  in the template next to the rclone one, which learned the same lesson first.
- **Adoptium is a URL we depend on.** The rclone step already does this and it has been
  reliable; `--retry 3` and `set -e` mean a failure shows as a failed cloud-init rather
  than a server that starts with the wrong java. The provisioning timeout is still the
  backstop, and it is still silent — that gap is issue #49 territory, not this change.

`{MC: "26.3", Java: 25}` is a row in `publish.Minecrafts` now, and per 37 a row is a
promise. It was earned the same night: `outpost pack vanilla --mc 26.3`, published to its
own profile, rented — cloud-init installed Temurin 25.0.5 from Adoptium, and the log says
`Starting minecraft server version 26.3` / `Done (4.265s)`, RCON answering on the next
tick. The whole promise took one rent and about six minutes. The range table that makes 1.20.4 or 1.21.8 askable
without a row each is issue #48's second half.

### 42. Modrinth's per-project sides decide, and the pack's annotations do not

The first draft of this was "read each jar's `environment` field", on the theory that a
pack which annotates nothing leaves the jars' own word as the fallback. Testing it against
the pack that caused the bug (`fabulously-optimized` 15.0.0-beta.4, Minecraft 26.3) killed
that theory twice over:

- The pack **does** annotate, and annotates everything identically: all fifty files are
  `env.client: required, env.server: required`. That is not a judgement about sides, it is
  what a launcher export writes when nobody said otherwise — and it made `env.server`
  useless as evidence in exactly the case it was needed.
- The jar that crashed the server, `e4mc`, declares `environment: "*"`. Its own metadata
  says it runs on a dedicated server, it registers a server command, and it was built
  against an API that no longer exists, so the first player's login threw
  `NoSuchMethodError: CommandSourceStack.hasPermission(int)` and the server exited. The
  player saw "kicked, then the server closed".

What knows better is Modrinth's per-project `server_side` — author-declared too, so not an
oracle, but written once by the person who wrote the mod rather than exported per pack by
whoever assembled one. It is already reachable: the
download URL carries the project id (`cdn.modrinth.com/data/<project>/versions/<version>/…`).
Of Fabulously Optimized's 50 files, 36 are `server_side: unsupported` — sodium, iris, the
entity-texture mods, both of its resource packs. `pack fetch` now drops those before
anything is downloaded: 64 files and 21.7 MB instead of 100 and 42.1 MB, and the skipped
list says why. Files with no Modrinth project behind them (a GitHub release, a mirror) get
no opinion and are installed as the pack says, and a project we could not ask about is
*not* dropped — an API hiccup must not empty a server.

`e4mc` is still installed, because Modrinth says `required` and it is wrong about a mod
that no longer matches the server API it targets. That residue is not detectable from
metadata, and the answer to it is not another filter but a daemon that says why a server
died — issue #51, which is the next thing after this. Until then, a client pack installed
on a server carries the risk that one of its mods is broken on that version, and the way to
find out is a log the daemon currently will not show you.

### 43. Java versions are ranges, and the picker is a shortlist

37's table was rows of `{MC, Java}` and it had two of them, so the panel offered two
versions of anything and every other version read as impossible. Two of those rows were
factually right and one was, on 2026-11-17, wrong in a way nobody could see: the version
that was actually wanted — 26.3 — needed a JDK the image did not install.

The fact underneath is a range, not a row. A mod's `fabric.mod.json` declares the JDK it
was built for, and the famous ones agree: `fabric-api` says `java >=17` for 1.17–1.20.4,
`>=21` for 1.20.5–1.21.x and `>=25` for 26.x. Those boundaries are `publish.JavaRanges`
now, and `JavaFor` is a lookup in them, so `1.19.4` or `1.21.8` or `26.2` boots without a
line of code each. A ceiling of `1.21.x` means "any 1.21", so a patch release nobody has
heard of yet does not need an edit either.

Two things are deliberately still a list:

- **`Minecrafts` is what the panel offers**, eight versions oldest-first, because a select
  with forty entries is not a better question than a select with eight. Anything a range
  covers is accepted by `pack vanilla --mc`, the API and `CheckPack`; the buttons are a
  shortlist, not the boundary. A test asserts the offers all resolve and that row zero is
  `SupportedMC`, so a plain `pack vanilla` still cannot move a world by surprise.
- **`Booted` is a column, not a claim.** `1.20.1`, `1.21.1` and `26.3` are marked because a
  real rent came up on each; the other five say "untested" in the select. This is 37's "a
  row is a promise" made literal — the promise is per version and somebody earned it.

What a range does **not** promise is that a version works. It promises the JDK the boot
will be given, which was the expensive unknown; whether 1.21.8 comes up is the boot's
answer, and the daemon now says what happened when it does not (41, #53).

### 44. A profile owns its working copy, so deleting one gives the space back

`PACK` uploads a directory, so `pack fetch` has to write one: `data/packs/<profile>/`, with
the archives it downloaded kept in `data/packs/_cache/`. Two things about that were true
and unstated — the copy outlives the profile by design (nothing removed it), and nothing
ever pruned the cache. A box that had hosted four packs held a gigabyte of them, two
directories of which belonged to profiles that no longer existed in either bucket.

A delete now removes the directory too and reports the bytes, because "removed 83 asset
keys" describes somebody else's disk and the useful sentence is "and 27 MB on the box".
The cache is swept on install, at 14 days, matching the trash: one archive per pack version
forever is a museum, and re-downloading is cheap next to keeping every archive.

A directory is only removed when the manager owns it (`Dir` set, profile named). A fetch
into a hand-picked directory is somebody's own folder, and this is not the code that should
delete it.

### 45. A slow boot is not a crash, and a warning is not an error

41 and #53 shipped "the server says why" and the first long session produced a clean
example of it lying: a 47-mod pack took three minutes to boot, the provisioning ask landed
at two, and the trail said

```
01:51:21  crash  … : Mod dev_isxander_sdl uses the version release-3.4.14-7 which isn't
                 compatible with Loader's extended semantic version format …
01:51:41  ready  … is up at 94.130.181.83
```

Two mistakes, both about knowing what a line means rather than what it says.

**Severity is in the line.** Minecraft's log prefixes every line with its thread and level,
so the distiller parses it and ranks: an exception's own line (`java.lang.NoSuchMethodError:
…`) beats an `ERROR` headline ("Encountered an unexpected exception"), which beats a `WARN`,
which beats nothing. Before, a keyword list decided, and "isn't compatible with Loader's
extended semantic version format" is a keyword match on `incompatible` — a warning every mod
with a non-semver version emits, presented as the reason a server died.

**`crash` is terminal.** Before the timeout or the `rcon-dead` transition, what the daemon
knows is "it has not answered yet", so the event is a `why` — informational, worded that
way — and the once-only bookkeeping is keyed per kind, because a slow boot and a subsequent
death are different questions about the same box. A phone showing "crashed" for a server
that came up twenty seconds later is the same dishonesty as showing nothing.

The lesson generalises past this feature: a report is only worth having if it is right about
*when* as well as *what*.

### 46. The tree chopper is in the profile, not the pack

The ask was two server-side mods — fell a tree from one log, and clear the leaves right
after — with no client install. "Server-side only" is not a jar property that Fabric
enforces; it is a claim about where the work happens, so the evidence has to be the code
that does it, not the tag:

- **FallingTree** mixes into `AbstractBlockMixin` and `LevelMixin`;
  **Leaves Us In Peace** mixes into `ServerLevel`, `WorldChunk`, `PackedTicks`,
  `SerializableChunkData` and `LeavesBlock`. Neither jar ships a `client` mixin group, so
  a vanilla client connects to a server that has them and sees logs and leaves vanish.
- The alternatives lost on different grounds: Leaves Be Gone is `client_side: required`
  and drags `puzzles-lib` and `forge-config-api-port` with it; Modern Leaf Decay never got
  a 26.3 build; Server Tree Feller is 17 KB and 17K downloads with no source or issue
  tracker; Tree Harvester does both jobs in one jar but is all-rights-reserved and wants
  `collective` beside it. It stays the answer if the two-jar pair ever misbehaves.

Then the judge call: the mods went into the **profile**, not the pack. Patching a `.mrpack`
is the right shape on paper, and one was built and fetched to prove the manifest accepts
it, but it is not the deployed path —
`pack fetch` reads Modrinth, and `pruneLocal` (`internal/pack/download.go:194`) deletes
anything under `mods/` a fetch did not write, which is the same code that stops two
versions of one mod from coexisting. So there is no place a jar can be dropped that a
fetch will respect, and the jars were added to a copy of the working directory
(`data/packs/fo-26-3-trees`) and published with `--profile fabulously-optimized-26-3`.

`--profile` is load-bearing, and not for tidiness. A pack's profile id comes from
`pack.json`'s slug, so a fresh fetch names `fabulously-optimized` while the live profile is
`fabulously-optimized-26-3`: the default would have created a second profile with a second,
empty world. Keeping the same profile is also what makes `SamePack` — source and slug —
hold the world, so the publish was an update and never a question about the world.

What was *not* done, on purpose: publishing the fresh fetch, which is 16 mods, over a
profile that runs 47. That fetch is what PLAN 42 correctly produces from Fabulously
Optimized — 36 of its 50 files are `server_side: unsupported` — but this profile predates
that drop, so publishing it would have pruned 31 jars from a server that was working. The
request was two mods; a cleanup wearing the same commit is how a pleasant evening becomes
an afternoon of bisecting a modpack. The corollary is uncomfortable and stated rather than
hidden: `data/packs/fo-26-3` is not the source of truth for what the server runs, only R2
and the published profile are.

The debt: nothing in the repository reproduces this. The two jars exist in R2 and in one
directory on the Potato, and the next `pack fetch` deletes them. Issue #59 is the fix —
`pack fetch --extra` resolving the mods per Minecraft version and writing them into the
manifest with `env.client: unsupported` — and until it lands, the rule for the next
Fabulously Optimized beta is *re-merge*, not *refetch*.

Verified on a real session rather than by inference: trees fell from one log, the canopy
cleared seconds later, and the client was stock Fabulously Optimized.

## Decisions from the keep-it-up branch (2026-11-19)

### 47. A hold is session state, and it is measured from the last player out

The premise is that the server bills only while somebody is playing, and the 30-minute
idle clock is the mechanism. That mechanism is wrong on exactly the days it matters: an
hour before work and a friend with the afternoon free is a nine-hour gap with nobody on
it, and the buttons that could bridge it live behind a page nobody is sitting in front of.
Issue #61 asked for a way to extend the window from the page; issue #13 had already specced
the same idea in-game and named the Fabric jar as the expensive part. This is #13's page
half, built first.

**A hold is session state, not configuration.** It lives on `server_state`
(`idle_extension_sec`) and `finalize` → `store.Reset` wipes it with everything else, so the
next start is 30 minutes again without anyone remembering a rule. "Not sticky" is then not
a promise the UI makes, it is what the schema does. Persisting it in the row rather than in
memory is deliberate: a daemon restart mid-session must not quietly take a server down
early.

**It replaces the total, and it is measured from the last player out.** Pressing it during
`RUNNING` with people on sets the limit the *next* empty period gets — that is the whole
workflow, because the thing you cannot do from work is press it after you have gone. Pressing
it during `IDLE` raises the limit for the period already counting and does **not** reset
`IdleSince`: a 6h hold pressed at hour five of six does not buy a fresh six, because the
clock it is promising is the one the page is showing. An extension shorter than the empty
time already run would terminate the server on the next tick, so it is refused rather than
obeyed — a shorter hold must never be a faster shutdown.

**The ceiling exists to bound a mistake, not a bill.** `server.max_extend`, 12h, because
the scenario that motivated this needs nine. Twelve idle hours on a cx23 is €0.12: the
whole cap is about a tenth of a normal evening and cheaper than the four-minute boot that
follows a shutdown. A forgotten server costs €0.23 a day, which is survivable; a server
that went away while somebody was loading in is the failure worth designing against. So the
guard is surprise, not money: the hero says the deadline as a clock time, and the events
table gets an `extend` row so "why is this still billing" has an answer that is not a log
file.

**The readout is not in `/api/status`.** The first attempt added three fields there and
`TestStatusKeepsTheShapeMeridianReads` failed within a second, which is what it is for.
Status is pinned to the shape another service reads; `Status.TTLSec` already carries the
extension for anyone who only wants the countdown, so the page's control reads
`overview.extend` (`{seconds, capSeconds, until}`) and `internal/orchestrator` keeps the one
copy of the limit rule in `idleLimit`, used by `Tick`, `Status` and `ExtendInfo` alike.
`outpost extend 6h` / `outpost extend off` exist because every other control does: a button
only in a browser tab is unreachable from the machine running the thing.

Deliberately not done: the in-game command surface (#13) and its per-session token, and any
form of absolute "until 16:00" hold. The relative form is one number and one subtraction;
the page is free to render the deadline it implies, and does.

## Decision from the inventory sorter (2026-11-22)

### 48. The sorter is a third jar in the same profile

The ask was one more server-side mod: sort an inventory with a click, and install nothing on
the client. The trigger is the whole question, because a sort *button* or a *keybind* is
client code and FO's players run a stock client (PLAN 46) — no server-side install can put a
button into a screen it does not own. So the candidates were judged on what a vanilla client
already sends, and on the jar's server-side mixin surface:

- **Inventory Sorting** (meza/Kyrptonaught, MIT, `3.0.1+fabric-26.3`) — what went in. The
  vanilla triggers are a middle-click or a double-click on an *empty* slot (the client sends
  the click, the server sorts) plus `/invsort sort` and `/invsort sortme`; `sortType`,
  `sortPlayerInventory`, `priority` and `nosort` are per-player commands, so nothing about
  the default has to be right on the first try. Its mixin config puts only `MixinContainer`
  in `mixins` and both screen mixins in `client` — the PLAN 46 test. It needs
  `fabric-api >=0.160.5+26.3` and `cloth-config >=26.3.158`, both already in the profile
  (0.166.2, 26.3.159), so it is one jar rather than three; meza_core and
  fabric-permissions-api ride inside it as nested jars.
- **Sort It Out!** (JamCoreModding, MIT, `1.5.0+26.3.x`) — the runner-up, and the one to
  reach for if the sorter ever misbehaves: 104 KB, one server mixin, double-click or the
  offhand key over a slot, `/sortitout` for preferences. It costs two jars, because it needs
  JamLib and the pack does not ship it.
- **Smart Inventory Sorter** (`1.1.2-mc26.3`) — automatic only: it sorts when a container
  closes, and with a vanilla client the sole manual trigger is `/sort`. All-rights-reserved,
  no source. The wrong shape for the ask, written down so the next person does not re-find
  it.

Deployed the way PLAN 46 was, which is to say by hand: the jar joined
`data/packs/fo-26-3-trees/mods/` on the Potato (49 → 50 mods) and that directory was
published with `--profile fabulously-optimized-26-3`. Publishing mid-session is safe — the
box boots from its own copy of `mods/` — but only the next boot loads a jar, so the session
that was live (0 players, ten minutes in) was stopped and started rather than left to the
idle clock. The world came back from R2 and the box came up with 50 mods.

Verified by the server's own answer rather than by inference: `Found new data pack
inventorysorter, loading it automatically` in the boot log, and `/help` listing `/invsort
(sort|doubleClickSort|sortPlayerInventory|allowPlayerInventorySorting|sortHighlightedInventory|sortIntoBundles|sortme|sortType|priority|nosort|reload|screenID|admin)`
beside the `/fallingtree toggle` that was already there. The mod wrote
`config/inventorysorter.json` with `enableDoubleClickSort: true` and
`sortPlayerInventory: false`; the second is the knob worth knowing about, since with it off
sorting a chest leaves your own inventory alone, and `/invsort sortPlayerInventory true`
sets it per player.

The debt is three jars instead of two, and #59 still pays it.

## Decision from the chunk loader (2026-11-23)

### 49. The loader is a lodestone, and it survives the reboot

The ask was a server-side chunk loader: keep an area simulating, install nothing on the
client. The test is 46's and 48's, but it bites harder here, because a chunk loader is
usually a *block* or an *item*. A modded one is invisible in a stock client's hand and
unplaceable by it, so the candidate had to be built out of vanilla content — and the
finalists that were not, lost before their mechanism mattered.

What went in is **Loadstone `3.0+26.3`**, one jar, `depends` nothing but `fabric-api: *`
(0.166.2 is already in the profile).

- **Trigger.** Place a vanilla lodestone and right-click it holding an ingot: iron loads
  1×1 chunks, diamond 3×3, netherite 5×5. An empty hand turns it off and drops the ingot
  back. Every part of that is an interaction a vanilla client already sends, and every
  block and item involved is already in its registry.
- **Proof in the jar.** The only common mixin is `LodestoneStateMixin` on
  `BlockBehaviour$BlockStateBase`, gated on `LODESTONE`; the client half is a separate
  config (`loadstone.client.mixins.json`, `environment: client`) holding the tint, the
  particles and the HUD, and `modmenu` is only a suggestion. `environment: "*"` with a
  `main` entrypoint, so the half that loads on a server is the half that does the work.
- **Why it won, which was not the trigger.** Loader state is a `SavedData`
  (`loadstone_data`) in the level's `SavedDataStorage`, so it is written to the world —
  `world/data/loadstone_data.dat` — and the loading itself is `addChunkTicket` on the
  level. The world tarball carries it and the ticket comes back when the box boots. That
  is the whole decision: every Outpost session is a fresh VPS, so a loader whose readme
  says "revisit the area after a restart" is a loader that is off every time we play.

Runner-up, and the one to reach for when a loader has to *move*: **Script's Chunk Loaders**
(`mc26.3-0.12.0`, MIT, 20 KB, source available). A dispenser holding four amethyst shards
around glowstone, shroomlight or magma turns a minecart into a 3×3 loader that travels with
the cart — shroomlight to always load, magma to always stop. It carries a `chunkLoader` NBT
flag and registers a `TicketType` with `FLAG_PERSIST | FLAG_LOADING | FLAG_SIMULATION |
FLAG_KEEP_DIMENSION_ACTIVE`, so it also survives a restart, and it has no custom items or
blocks at all. It lost on the trigger, not on the code: a dispenser pattern and a cart on
rails is more to build and keep than a lodestone, and it covers 3×3 where Loadstone reaches
5×5. Loadstone cannot follow a rail line, which is the one thing it cannot do.

Rejected, written down so the next session does not re-find them:

- **Copper Chunk Loader** (`1.0.4+mod`, CC-BY-NC-SA-4.0) — tiers from 3×3 to 11×11 and a
  much nicer placed look, but its own readme requires revisiting the area after a restart,
  which is the failure above. A datapack-as-mod via `fabric-resource-loader`.
- **Chunk Loaders** (SuperMartijn642, `1.2.9-fabric-mc26.3`) — `client_side: required`, and
  its blocks and items are modded, so a stock client could not see what it placed.
- **Chunk Loader data pack** (HauntedCorpse, MIT) — no source, 20 downloads, a config
  `dialog` and nothing to read. Unprovable is not a side question.
- **simpleAFKBot** (`1.3.0`, MIT) — the only candidate that also enables mob spawning,
  because a fake player *is* a player. That is exactly why it cannot go here: it appears in
  `list`, so `Tick` never sees zero players, the idle clock never starts, and the box bills
  to the hold cap and then keeps going. If we ever want a deliberate "up and spawning" mode
  it belongs behind the hold, not beside it.

**The limit to say out loud:** no ticket-based loader spawns mobs. Forced chunks tick
redstone and grow crops (the random-tick path, 1.21.5+), but mob spawning needs a player in
range, so an iron farm or a mob tower will not run while the last person is offline. Mob
farming with nobody on needs the fake-player shape, with the billing problem above.

Deployed the way 46 and 48 were — by hand, into the live profile's working copy
(`data/packs/fo-26-3-trees/mods/`, 50 → 51 jars) and published with `--profile
fabulously-optimized-26-3`. It went in mid-session with two players on, which is safe: the
box boots from its own copy of `mods/`, so the running session was untouched, and the jar
waited for the next boot.

Verified on that boot, from the server's own mouth rather than by inference: 51 jars in
`/opt/minecraft/mods/` on the box, `Loading 93 mods:` followed by `- loadstone 3.0+26.3`
and `Loadstone Loaded Successfully!` in `latest.log`, and `/help` listing `/loadstone
(list|deactivate|activate)` beside the `/fallingtree toggle` and `/invsort` that were
already there.

One note on the licence: IlyRac's is all-rights-reserved, which 46 treated as a reason to
prefer something else. It permits use on private servers explicitly, and uploading our own
copy to our own bucket for our own box is that use — not redistribution. It is recorded
here rather than glossed over because the next person should get to weigh it.

The debt is four jars instead of three, and #59 still pays it.
