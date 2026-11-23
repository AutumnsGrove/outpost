---
name: mod-search
description: >
  Find, prove, install and verify a Minecraft Fabric mod that works with a stock
  client, so nobody has to add a jar to their launcher. Use when the user asks
  "is there a mod that does X" and adds that it has to be server-side, server-only,
  or install nothing on the client; also when a server-only mod misbehaves, needs
  replacing, or the question is "can the server do this without my client knowing".
license: MIT
metadata:
  author: Autumn Brown
  version: "1.0.0"
allowed-tools:
  - WebSearch
  - WebFetch
  - Read
  - Edit
  - Write
  - AskUserQuestion
  - Bash(curl:*)
  - Bash(unzip:*)
  - Bash(shasum:*)
  - Bash(ssh:*)
  - Bash(scp:*)
  - Bash(git:*)
---

# Mod Search — server-side only

A request here always has the same shape: *"is there a mod that does X, and can it
live only on the server so my friends on a stock client see it work?"* The answer is
never a search result. It is a jar whose code proves the work happens server-side, a
trigger a vanilla client already sends, dependencies the live profile already satisfies,
and a boot log that names it.

## Inputs

- `$request`: the feature in the user's own words ("chop one log, whole tree falls",
  "sort a chest with a click"). Synonyms matter more than the words chosen.

## Goal

A named jar, installed in the **active** profile, published to R2, verified from the
server's own output, with the decision and its runner-up written into `PLAN.md` and on a
pull request. A shortlist with no install is a valid stopping point only when the user
asks for research.

## Hard rules

These are lessons already paid for in `PLAN.md` 46 and 48. Do not relearn them.

1. **"Server-side only" is not a jar property.** It is a claim about where the work
   happens. The evidence is the code that does it, not the Modrinth tag and not the
   mod page's marketing line.
2. **Modrinth's `server_side` is author-declared.** It triages; it never decides.
   `server_side: unsupported` is a hard no. `required` still has to pass step 3.
3. **The trigger is the whole question.** A sort *button* or a *keybind* is client code.
   No server-side install can put a control into a screen it does not own. Only triggers
   a vanilla client already sends count: a chat command, a block break, a container
   click, a double-click or middle-click on an empty slot, item use, entity interaction.
4. **One mod request is one mod.** Do not smuggle a pack refetch or a cleanup into the
   same change. That is how a pleasant evening becomes an afternoon of bisecting a
   modpack.
5. **`--profile` is load-bearing.** A pack's profile id comes from `pack.json`'s slug, so
   publishing without `--profile` creates a *second* profile with a second, empty world.
6. **Never `pack fetch` to add a mod.** `pruneLocal` (`internal/pack/download.go`) deletes
   anything under `mods/` a fetch did not write. It is also what stops two versions of one
   mod coexisting.
7. **The repository is not the truth about what runs.** `packs/` in the repo is a fresh
   fetch; the live profile is R2 plus a directory on the Potato. Ask the Potato.
8. **Booted is not proof.** The proof is the mod's name in the boot log and its command
   in `/help`, not "the server came up".

## Steps

### 1. Learn the ground truth from the Potato

Never assume the pack, the Minecraft version, or the live mod list.

```bash
ssh potato-remote 'cd ~/outpost && docker exec outpost-outpost-1 /app/outpost pack list'
```

The `*` row is the active profile. Note its **exact id** and **mod count** — the count is
what identifies its working directory, because the directory name is not the profile id
(`fo-26-3-trees` publishes `fabulously-optimized-26-3`).

```bash
ssh potato-remote 'cd ~/outpost/data/packs && for d in */; do echo "$d $(ls ${d}mods 2>/dev/null | wc -l)"; done'
```

Match the count. Cross-check the candidate's `pack.json` (`slug`, `versionNumber`, `mc`)
against the active row. If two directories match, or none does, **stop and ask** — the
wrong directory publishes someone else's mods.

Then read what is actually installed, which is the dependency inventory:

```bash
ssh potato-remote 'cd ~/outpost/data/packs/<dir> && cat pack.json && ls mods'
```

**Artifacts**: `<profile>` (exact id), `<dir>` (working copy), `<mc>`, `<loader>`, and the
live jar list. Every later step uses these and nothing else.
**Success criteria**: you can name the profile id, the directory, the Minecraft version,
and at least the fabric-api / cloth-config / library versions already present.

### 2. Search wide, in the user's synonyms

Run both, in parallel: `web_search` for discovery and community answers, Modrinth's API
for structured facts. Search the *feature* under several names — "tree chopper", "falling
tree", "timber", "treecapitator"; "inventory sorter", "sort chest", "invsort".

```bash
mc=<mc>
curl -sG https://api.modrinth.com/v2/search \
  -H "User-Agent: outpost-modresearch/1.0" \
  --data-urlencode "query=tree feller" \
  --data-urlencode "facets=[[\"versions:$mc\"],[\"categories:fabric\"]]" \
  --data-urlencode "limit=15" |
python3 -c 'import json,sys
for h in json.load(sys.stdin)["hits"]:
    print("%-28s %9d  server=%-10s client=%-10s %-16s %s" % (
        h["slug"], h["downloads"], h["server_side"], h["client_side"],
        h["license"], h["title"]))'
```

A User-Agent is required or Modrinth returns 400. Facets are JSON-in-URL; `versions` must
be exact (`26.3`, not `1.20.1`) and the loader is `fabric`. Use `%`-formatting with double
quotes, not an f-string: the whole script sits inside shell single quotes, so a `"` inside
an f-string expression reaches Python as a backslash and is a `SyntaxError` — and a
heredoc cannot help, because it would take stdin away from the pipe.

**Success criteria**: 8–15 candidates for this Minecraft version, fat ones and thin ones,
including the obvious loser — the loser is how the PLAN entry explains itself later.

### 3. Triage on facts, not on tags

For each candidate worth a second look:

```bash
curl -s "https://api.modrinth.com/v2/project/<slug>" -H "User-Agent: outpost-modresearch/1.0" |
python3 -c 'import json,sys; d=json.load(sys.stdin)
print(d["title"], d["license"]["id"], d["downloads"], "server:", d["server_side"], "client:", d["client_side"])
print(d["source_url"], d["issues_url"])'

curl -sG "https://api.modrinth.com/v2/project/<slug>/version" -H "User-Agent: outpost-modresearch/1.0" \
  --data-urlencode "game_versions=[\"$mc\"]" --data-urlencode 'loaders=["fabric"]' |
python3 -c 'import json,sys
for v in json.load(sys.stdin)[:6]:
    print(v["version_number"], v["version_type"], v["date_published"][:10], [f["filename"] for f in v["files"]])'
```

Reject on: `server_side: unsupported`; no build for `<mc>`; all-rights-reserved with no
source and no issue tracker; a very thin download count *and* no tracker (not
disqualifying, but it needs a written reason — this is the Server Tree Feller lesson).
Record the runner-up now; it goes in the PLAN entry.

**Success criteria**: a shortlist where every entry has a version string, a licence, a
download count, and a source/tracker link or an explicit note that there is none.

### 4. Prove it in the jar

This is the step that decides. Fetch the exact file, check its hash, and read its mixin
config and entrypoints.

```bash
cd /tmp && rm -rf modcheck && mkdir modcheck && cd modcheck
curl -sL -o mod.jar "<files[].url from the version json>"
shasum -a 512 mod.jar          # must equal files[].hashes.sha512

unzip -p mod.jar fabric.mod.json | python3 -m json.tool | head -60
unzip -l mod.jar | grep -iE "client|screen|keybind|hud" | head -20
unzip -l mod.jar | grep "META-INF/jars/"

for f in $(unzip -l mod.jar | awk '{print $4}' | grep -E 'mixins.*\.json$'); do
  echo "=== $f"; unzip -p mod.jar "$f"
done
```

Read the result against three tests:

- **Environment.** `fabric.mod.json` → `"environment": "*"` or `"server"` is fine.
  `"client"` means the loader skips it server-side: reject.
- **Mixin surface.** The mixin config's `"mixins"` array is common code and is what must
  do the job. A `"client"` array is the tell: absent is ideal, empty is acceptable, and a
  populated one means the feature may live behind client code — go to the trigger test.
  Client *classes* shipped in the jar are not automatically fatal (they only load when a
  client mod is present), but their presence raises the bar.
- **Trigger.** Name the exact vanilla interaction that starts the work. If it is a
  keybind, a button, or a custom screen, the mod is the wrong shape no matter what the
  tag says, because a stock client cannot send it.

Then check what it drags in: `depends` in `fabric.mod.json` at versions the live profile
already satisfies (step 1), plus any `META-INF/jars/` nested jars — those count as jars
even though they arrive inside one file. A mod needing a library the profile lacks is a
two-jar install, and that is a reason to prefer a different candidate.

**Success criteria**: for the pick and the runner-up, you can state in one sentence where
the work happens, which class it mixes into, and which vanilla action triggers it — and
the sha512 matches Modrinth.

### 5. Shortlist, and let her choose

Report: the pick, the runner-up, and the rejects with their one-line reasons. For each of
the first two: version string, licence, jar count including nested and missing
dependencies, the trigger, and what the client sees. If a real choice exists, ask with
the question tool (`pi-q-n-a` in pi, `AskUserQuestion` in Claude Code) rather than
deciding alone — the user is the one who has to live with the feel of the trigger.

**Human checkpoint**: no install without a go-ahead.
**Success criteria**: she has picked a jar, or asked for research only.

### 6. Install into the live profile

```bash
# hash it again on the far side; a truncated scp looks exactly like a corrupt mod
scp mod.jar "potato-remote:~/outpost/data/packs/<dir>/mods/<exact-filename>.jar"
ssh potato-remote 'cd ~/outpost/data/packs/<dir>/mods && shasum -a 512 <exact-filename>.jar'
```

Keep Modrinth's filename; renaming costs nothing and makes the next audit harder. Do not
touch any other jar, config, or `pack.json`.

If a session is live, publishing mid-session is safe — the box boots from its own copy —
but **only the next boot loads the jar**. With players on, ask before stopping. With an
empty server, stop → publish → start, rather than leaving a jar that is published but not
running.

**Success criteria**: `ls mods | wc -l` is exactly the old count + 1, and the hash matches.

### 7. Publish with the active profile named

```bash
ssh potato-remote 'cd ~/outpost && docker exec outpost-outpost-1 /app/outpost publish /data/packs/<dir> --profile <profile>'
```

Pass `--profile` even when it looks redundant. Same pack and same slug is what keeps the
world: `SamePack` compares source and slug, so this is an update, never a question about
the world. Note that `publish` prints only file counts and warnings — it never names the
profile, and publishing into a brand-new profile is silent, so its own output cannot be
the check.

**Success criteria**: `outpost pack list` shows the same exact profile id, now with the old
count + 1 mods, and **no new row**. A new row means `--profile` was wrong and a second,
empty world now exists. Exact id, not a lookalike: two profiles for one pack is the whole
failure being guarded.

### 8. Verify from the server's mouth

The jar only runs after a boot, and `start` refuses with `server is RUNNING; stop it first`
unless the daemon is `OFFLINE`. So the state decides the path:

```bash
ssh potato-remote 'cd ~/outpost && docker exec outpost-outpost-1 /app/outpost status'
```

- **`OFFLINE`** — start and verify now:
  ```bash
  ssh potato-remote 'cd ~/outpost && docker exec outpost-outpost-1 /app/outpost start'
  ```
- **`RUNNING`** — the jar is published but not loaded, and that is a deferral, not a
  failure. Do not stop a session with players on it to prove a mod. With an empty server,
  `stop` then `start` is the same thing. With players on, verification waits for the next
  boot, and this step is not done until it happens.

Two proofs, in either order: the boot log naming the mod
(`Found new data pack <id>, loading it automatically`), and `/help` listing the mod's own
command beside the ones already there. `/help` is the RCON path and needs no key:

```bash
ssh potato-remote 'cd ~/outpost && docker exec outpost-outpost-1 /app/outpost console "help"'
```

The boot log is not on the daemon and there is no `outpost logs`: it lives on the ephemeral
VPS, read over the tailnet with the daemon's own key. The MagicDNS name in `outpost status`
is what the daemon resolves through tailscaled's LocalAPI, and it does **not** resolve on
the Potato's system resolver — so take the address from `tailscale status` instead:

```bash
ssh potato-remote 'tailscale status | awk "/^100\\./ && /outpost-/{print \$1; exit}"'
ssh potato-remote 'ssh -i ~/outpost/data/ssh/outpost -o StrictHostKeyChecking=accept-new root@<tailnet-ip> \
  "grep -i \"new data pack\" /opt/minecraft/logs/latest.log | tail -5"'
```

Useful alternates on the same key: `grep -i "Loading [0-9]* mods"` for the count the
loader saw, and `ls /opt/minecraft/crash-reports` when the boot did not finish.

A mod with no command is proven by the log line alone. "It reached `RUNNING`" proves
nothing about the jar — a mod that failed to load still boots.

**Success criteria**: the mod's name appears in the log for this boot, and its command (if
it has one) appears in `/help`.

### 9. Record it and ship it

Add a numbered decision to `PLAN.md`: the ask, the evidence (mixin target, trigger,
environment), the alternatives and why each lost, how it was deployed, and the debt. The
alternatives are the point — they are what stops the next session re-finding the runner-up.

Then commit and open a PR, per `AGENT.md`: one branch off `main`, conventional commit,
`docs(plan): record <mod> on the <profile> profile` for the writeup, the jar commit
separate if the pack is tracked. Subscribe to the PR instead of polling.

**Success criteria**: the PLAN entry reads as a decision, not a changelog, and the PR is
open with the mod-search debt stated.

## When issue #59 lands

`pack fetch --extra` will resolve extra mods per Minecraft version and write them into the
manifest with `env.client: unsupported`. When that exists, **steps 6 and 7 collapse into
one**: the mod is named in the fetch, not scp'd into a directory a fetch would prune. The
only edits needed then are step 6 (replace the scp with the `--extra` invocation), step 7
(drop the hand-publish), and the debt paragraph in the PLAN entry. Everything before step 6
is independent of how the jar arrives, and stays.

Until then, say the debt out loud in the PLAN entry: nothing in the repository reproduces
the live profile, the jars exist in R2 and in one directory on the Potato, and the next
`pack fetch` deletes them. The rule for a new pack beta is *re-merge*, not *refetch*.

## Failure modes seen before

| Symptom | What it actually was |
|---|---|
| Mod loads, feature does nothing | The trigger was client-side. The jar was fine; a vanilla client never sent the packet. |
| A second, empty world appeared | `publish` without `--profile`, so the slug made a new profile. |
| 31 jars vanished from a working server | A fresh fetch published over a profile it did not match. `pruneLocal` cleaned up. |
| Mod missing after `pack fetch` | Fetch prunes `mods/`; the jar has to be part of the fetch, which is #59. |
| "Server-side" mod wants a library | `depends` not satisfied by the profile — check the live jar list, not the repo's. |
| Boot log says incompatible version | The version string, not the mod: check `game_versions` for the exact `<mc>`, and that the loader is `fabric`. |
