# Handoff

**Written:** 2026-11-11
**Repo:** [AutumnsGrove/outpost](https://github.com/AutumnsGrove/outpost) (renamed
from `GroveMC` today)
**State:** v2 skeleton is built, tested, merged to `main`, and does not yet run
against real infrastructure.

---

## One paragraph

Outpost is an on-demand Minecraft server. `outpost start` rents a Hetzner VPS,
cloud-init joins it to the tailnet and pulls the server files from R2, the
orchestrator polls RCON for the player count, and when everybody has logged off
and stayed off it saves the world to R2 and deletes the VPS. The orchestrator is a
Go binary that lives on the Le Potato; it does all the thinking and runs none of
the Minecraft. A two-hour session costs about two cents.

v1 from December 2025 worked (12 sessions, 4.6 hours, $0.057) and is preserved in
`legacy/v1/`. v2 exists because v1's state machine was split across a Cloudflare
Worker and a bash script inside the VPS, and the seam between them was never
finished. `PLAN.md` has the full reasoning.

---

## What is actually done

| | |
|---|---|
| Merge commit | `f4ddbbd` on `main`, plus docs commits after |
| Tests | 33, all passing (`make test`) |
| Build | `make build` (mac) and `make potato` (linux/arm64, static, no cgo) — both verified |
| Smoke-tested | CLI against a throwaway config; HTTP API answers `401` without the bearer token and correct JSON with it |
| Touched real infra | **No.** No Hetzner call has been made from this code. |

Built: config loader, SQLite store, Hetzner client, Cloudflare DNS client, RCON
client, SSH runner, cloud-init renderer, the state machine, the JSON API, the CLI,
and `outpost publish` (R2 over the S3 API, Fabric launcher, Mojang UUIDs).

Still not built: the modpack itself. `publish` takes a directory; nobody has
chosen what goes in it yet.

---

## The decisions, and why (full text in `PLAN.md`)

1. Repo renamed to `outpost`, history kept, v1 moved to `legacy/v1/`.
2. The orchestrator runs on the Le Potato, not on Cloudflare.
3. No webhooks, no in-VPS watchdog. One RCON probe over the tailnet answers both
   "is it up" and "who is playing".
4. `SUSPENDED` is gone — a warm VPS bills the same as a busy one.
5. Idle: 10 minutes to `IDLE`, 20 more to terminate (v1 was 15 + 45).
6. R2's **key layout** stays; R2's **contents** are discarded. Fresh modpack, fresh
   world, nothing carried over.
7. DNS is a plain A record, updated on start, DNS-only (Minecraft is raw TCP).
8. `modernc.org/sqlite`, pure Go, so the arm64 cross-compile needs no toolchain.

---

## The four v1 bugs, so they don't come back

1. **`$(grep -c … || echo "0")` produced `"0\n0"`** and killed the watchdog's
   arithmetic under `set -e`, so the script crash-looped and backed up the world on
   every death (ten 38 MB uploads in 3½ minutes). *Lesson: no logic in bash.*
2. **`TERMINATING` had no handler.** The watchdog signalled it and slept; the worker
   ignored it; the health check only reclaimed servers already `off`. Only the
   manual Stop button ever deleted anything. *Lesson: one owner per state.*
3. **`SUSPENDED` saved nothing.** Stopping the JVM does not stop Hetzner billing.
4. **Player count came from `tail -500 | grep -c "joined the game"`**, a running
   total over a sliding window. RCON existed in the worker and was not used.

The current code guards each: RCON replaces log parsing, `Tick` is idempotent,
deletion is confirmed before the state returns to `OFFLINE`, and a failed world
save leaves the VPS running rather than deleting a world it could not back up.

---

## Open work, in order

### 1. ~~Publishing assets to R2~~ — done

`outpost publish` (written 2026-11-11, after this handoff) fills the asset bucket:
it fetches a Fabric launcher from Fabric Meta, resolves whitelist names to UUIDs
through Mojang, generates `eula.txt`, `server.properties` and `start.sh` when the
pack has none, uploads `server/`, `mods/`, `config/` and `scripts/`, and verifies
every key's size in R2 before it reports success. `--prune` removes stale mods,
`--dry-run` prints the plan.

What is left of this item is the *content* question, not the plumbing: vanilla
first, then decide on a modpack.

The layout it writes is the one the template reads:

```
mc-assets/server/{fabric-server-launch.jar,server.properties,ops.json,whitelist.json,eula.txt}
mc-assets/mods/*.jar
mc-assets/config/
mc-assets/scripts/{start.sh,stop.sh}
mc-worlds/current/world.tar.gz        optional — absent means "generate one"
```

`legacy/v1/r2-manage.sh` and `legacy/v1/upload-mods.sh` were the alternative and
are now dead: a bash script would have been the third language in the project and
the only piece that decides what a server boots from. The client-only filter they
carried (118 lines, 84 mods) moved to `internal/publish/client-only-mods.txt` as
the default, and a pack can override it with its own copy.

The whitelist is generated from names at publish time, so v1's single-entry
`AutumnsAdventure` file cannot come back by accident: `--whitelist` takes both
people and Mojang supplies the UUIDs.

### 2. Server pricing needs a second look

Hetzner raised prices on 15 June 2026 for new orders and rescales — some plans
more than doubled — so the `hourly_rate` values in `config.yaml` are stale and the
"~$0.012/hr" in the README is no longer right. It only feeds the cost display in
`outpost status`, never the bill, but it should be corrected from an invoice before
the first real run.

More importantly: **Hetzner has shown "Limited availability of cloud instances"
on its status page since 26 June 2026**, and on 4 November 2026 every CX and CAX
plan was marked unavailable on the website. That is a bigger risk to this design
than the price is — `outpost start` is a request to rent one specific server type
in one specific location, and a sold-out plan turns into a failed start.

#### Rough per-session cost, 8 GB class

Third-party aggregator figures, checked November 2026, USD/month excluding VAT.
**Verify on the provider's own page before relying on any of it.**

| Plan | vCPU / RAM | $/month | ≈ $/hr | 2 h session |
|---|---|---|---|---|
| Hetzner CAX21 (arm64) | 4 / 8 | 7.59 | 0.0104 | **$0.021** |
| Hetzner CPX31× | 4 / 8 | 15.59 | 0.0214 | $0.043 |
| Vultr Regular | 4 / 8 | 40 | 0.0548 | $0.11 |
| DigitalOcean Premium | 2 / 8 | 42 | 0.0575 | $0.12 |
| Hetzner CCX13 (dedicated) | 2 / 8 | ~41 | 0.0700 | $0.16 |

× the plan this project originally shipped with in 2025. Note it is the *most
expensive* shared option in the table — it was repriced. `config.example.yaml`
now defaults to the 4 GB CX22 instead.

#### What that means

- **Provider choice is not the big lever. Instance size is.** Dropping from an
  8 GB to a 4 GB plan (CX22 at ~$0.0063/hr, $0.013 per session) halves to thirds
  the cost, and it is the same lever no matter who hosts it. That is really a
  question about the modpack: v1 used `-Xms4G -Xmx6G` because 188 mods needed it.
  A deliberately lean modpack might fit 4 GB, which is worth measuring before
  hunting for a cheaper host.
- **ARM is roughly half price** (CAX21 vs CPX21 at the same spec). Fabric and
  Minecraft run on arm64, but any mod shipping native libraries needs an arm64
  build. Worth testing with the actual modpack, not worth assuming.
- **Hetzner still wins on price by 3-6× at this size**, and per-hour billing with
  a monthly cap is exactly the right shape: for two-hour bursts you never come
  near the cap. The reason to look elsewhere is stock, not money.
- **DigitalOcean moved to per-second billing in January 2026** and Scaleway bills
  per second, which shaves the provisioning minutes off the bill. At these amounts
  that is fractions of a cent — irrelevant to the decision.
- **The usual "cheap VPS" names don't work for this.** Contabo and OVHcloud's VPS
  line are monthly or 12-24 month contracts, which is the opposite of the point.
  Anchors like Hetzner's €4/month 4 GB plan look unbeatable on a monthly basis and
  are the wrong unit entirely.
- **IPv4 is billed separately and is charged whether or not the server runs.** On
  Hetzner that is ~€0.50/month, billed hourly. Since the record outlives each
  session, this is a small standing cost to include in the ledger.

#### Recommended shape

Keep Hetzner as the primary and make the orchestrator tolerate its absence rather
than migrating away:

- try each configured region (and server type) in turn before failing — the config
  already supports multiple regions, and `Start` currently takes exactly one
- prefer the smallest instance the chosen modpack actually runs on
- keep `regions:` in `config.yaml` as the only place a provider is named, so a
  second provider is a new implementation of the `Servers` interface and nothing
  else. The interface in `internal/orchestrator` was written with that in mind.

Cross-check candidates, in the order worth the effort: **Hetzner CAX** (ARM, price),
**Scaleway** (per-second, EU), **DigitalOcean** (per-second, reliable stock),
**Vultr** (many regions). Ignore anything without an API and hourly deletion.

### 3. Secrets

Nothing is stored in the repo. Export these before `outpost serve`:

```
HETZNER_TOKEN       Hetzner Cloud API token
CF_TOKEN            Cloudflare, Zone:DNS:Edit
TS_AUTHKEY          Tailscale auth key, tagged tag:outpost
R2_ACCESS_KEY       R2 token pair (used by the VPS via rclone)
R2_SECRET_KEY
OUTPOST_TOKEN       optional bearer token for the local API
```

v1's tokens only ever existed as `mc-control` Worker secrets and are not readable.
Hetzner also needs the SSH key from `~/.ssh/grovemc` uploaded to the project, with
its integer key id in `config.yaml` as `hetzner.ssh_key_id`.

**Check the Hetzner account while you are in there.** `mc.grove.place` still points
at `116.203.50.47`, which reverse-resolves to a Hetzner IP with port 80 open. Ports
22 and 25565 are closed, so it is probably not a leftover Minecraft box, but it has
not been confirmed and it has had eleven months to bill. `outpost reset` deletes
every server labelled `project=outpost`; anything else you delete by hand.

### 4. Deploy to the Le Potato

That is the `homelab` repo, and it has a documented shape:

- `docs/outpost.md` — service doc, written at planning time per the homelab README
- a systemd unit for `outpost serve`, `EnvironmentFile` for the secrets
- Meridian: a tile in `~/Meridian/config.yaml`, and a target in
  `/etc/meridian/targets.json` if it should be actionable
- `maintenance-setup/service-healthcheck.sh` — in the same change as the install
- port **8896** is free; the binary listens on `127.0.0.1:8896`, so
  `tailscale serve --bg 8896` puts it at `https://lepotato.taild95077.ts.net:8896`

Backups: `outpost.db` is the only state. Daily `sqlite3 .backup` alongside the
other services is enough.

### 5. First real run

Use a throwaway config with `idle_grace: "1m"` and `idle_timeout: "2m"` so the
whole session — provision, play, idle, save, delete — fits in one sitting instead
of an hour.

- [ ] `outpost start eu`, then `outpost status` until it says `RUNNING` (~3 min)
- [ ] confirm `mc.<domain>` moved to the new IP
- [ ] connect with the client, verify the modpack and a fresh world
- [ ] watch `outpost status` report `players 1`
- [ ] log off, watch `RUNNING → IDLE → TERMINATING → OFFLINE`
- [ ] in `wrangler r2 bucket info mc-worlds`, confirm `current/world.tar.gz` grew
- [ ] confirm the VPS is gone in the Hetzner console, not just in the state table
- [ ] check the session row: `outpost status` lifetime hours and cost look sane

Then restore the real timings (10m / 20m).

### 6. Afterwards

- **Snapshot the prepped VPS.** `hetzner.snapshot_id` is wired through
  `createServer` and unused. Once one clean run has produced a working
  `/opt/minecraft`, snapshot it: provisioning drops from ~3 minutes of downloading
  the whole modpack to about 40 seconds, and the snapshot costs cents per month.
- **Undeploy v1.** `wrangler delete` the `mc-control` worker, drop D1 `mc-state`,
  empty and delete the `mc-assets` and `mc-worlds` buckets (298 MB + 849 MB of
  December 2025 that is now explicitly disposable), and delete the stale
  `mc.grove.place` A record and `_minecraft._tcp.grove.place` SRV record.
- **Lattice cleanup PR.** Remove the last grove-shaped traces:
  - delete `Lattice/_junkdrawer/features/outpost-routes/` and
    `_junkdrawer/features/outpost-heartwood/`
  - delete `_junkdrawer/features/firefly/consumers/outpost.ts`
  - drop the `outpost` entry from `libs/engine/src/lib/data/grove-term-manifest.json`
    (it is still a live glossary term on the landing site)
  - remove the `mc-control` origin and the `mc` subdomain from
    `services/grove-router/src/index.ts`
  - sweep the prose in `docs/developer/decisions/project-organization.md`,
    `docs/patterns/firefly-pattern.md` and
    `docs/philosophy/naming-research/outpost-grove-journey.md`
- **Optional:** a Meridian-side start/stop button, once the API token is in place.
- **Naming:** done. The GitHub repo is `AutumnsGrove/outpost` and the local clone
  is `~/Documents/Projects/outpost`. Nothing in the repo depends on the path.

---

## Map

```
cmd/outpost/              CLI (cobra): serve, start, stop, status, tick, reset
internal/orchestrator/    the state machine — read this first
internal/cloudinit/       cloud-init template; owns the VPS boot, nothing else
internal/rcon/            Minecraft RCON client and list parsing
internal/hetzner/         Cloud API: create, get, delete, list
internal/dns/             Cloudflare A record
internal/store/           SQLite schema, state, sessions, backups, events
internal/config/          config.yaml + env secrets
legacy/v1/                the 2025 Worker build, its spec and its bug log
PLAN.md                   decisions and reasons
```

Three things to keep true:

- **`Tick` is the only thing that changes state.** If a new behaviour needs a
  transition, it goes there, not in an HTTP handler and certainly not in the VPS.
- **The VPS has no opinions.** If the cloud-init template grows a timer or a loop,
  the split-brain that killed v1 is back.
- **Never delete a world you could not back up.** A failed save is `ERROR` with the
  server still running, on purpose.
