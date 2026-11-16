#!/usr/bin/env bash
#
# One-time setup of Outpost on the potato. Run from a checkout of the repository
# (for example ~/outpost), as the user who will own the install (not as root; it
# uses sudo only to install the systemd units and the host CLI wrapper).
#
#   deploy/install.sh
#   deploy/install.sh --dry-run
#
# It never overwrites an existing .env or config.yaml. Design: PLAN.md decision 38.
set -euo pipefail

INSTALL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
USER_NAME="$(id -un)"
PORT="8896"; DRY=0; START=1

while [ $# -gt 0 ]; do
	case "$1" in
	--port) PORT="$2"; shift 2 ;;
	--dry-run) DRY=1; shift ;;
	--no-start) START=0; shift ;;
	-h | --help) sed -n '2,13p' "$0"; exit 0 ;;
	*) echo "unknown option: $1" >&2; exit 2 ;;
	esac
done

run() { if [ "$DRY" -eq 1 ]; then echo "  would run: $*"; else "$@"; fi; }
say() { echo "==> $*"; }

if [ "$(id -u)" -eq 0 ]; then
	echo "Run this as your normal user, not root. It asks for sudo where it needs it." >&2
	exit 1
fi
command -v docker >/dev/null || { echo "docker is not installed" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "the docker compose plugin is not installed" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required" >&2; exit 1; }
command -v flock >/dev/null || { echo "flock (util-linux) is required" >&2; exit 1; }
id -nG | tr ' ' '\n' | grep -qx docker || { echo "$USER_NAME is not in the docker group" >&2; exit 1; }

say "Folders"
run mkdir -p "$INSTALL_DIR/data/packs" "$INSTALL_DIR/data/backups" "$INSTALL_DIR/update-signal"

say "Existing state"
# The old unit ran with WorkingDirectory=~/outpost and database: "outpost.db", so
# the live state is at the checkout root. The container only ever sees ./data, and
# starting on a fresh database there would abandon the session history — and, if a
# server is rented right now, leave it billing with nothing reading its state.
#
# The old daemon is stopped first, because moving a SQLite file out from under a
# running writer is how a database becomes a database plus a corrupt WAL.
if systemctl is-active --quiet outpost.service 2>/dev/null; then
	run sudo systemctl stop outpost.service
	echo "  stopped the old outpost.service (its state is moved next)"
fi

if [ -f "$INSTALL_DIR/outpost.db" ] && [ ! -f "$INSTALL_DIR/data/outpost.db" ]; then
	run mv "$INSTALL_DIR/outpost.db" "$INSTALL_DIR/data/outpost.db"
	for sidecar in -wal -shm; do
		[ -f "$INSTALL_DIR/outpost.db$sidecar" ] && run mv "$INSTALL_DIR/outpost.db$sidecar" "$INSTALL_DIR/data/outpost.db$sidecar"
	done
	[ -f "$INSTALL_DIR/outpost.db.lock" ] && run rm -f "$INSTALL_DIR/outpost.db.lock"
	echo "  moved outpost.db into data/ (sessions, backups, events and the active profile)"
elif [ -f "$INSTALL_DIR/data/outpost.db" ]; then
	echo "  data/outpost.db already exists, leaving it alone"
fi

# Fetched packs and the .mrpack cache lived beside the checkout too.
if [ -d "$INSTALL_DIR/packs" ]; then
	if [ -z "$(ls -A "$INSTALL_DIR/data/packs" 2>/dev/null || true)" ]; then
		run rm -rf "$INSTALL_DIR/data/packs"
		run mv "$INSTALL_DIR/packs" "$INSTALL_DIR/data/packs"
		echo "  moved packs/ into data/packs/"
	else
		echo "  data/packs already has files; leaving packs/ where it is (merge by hand if you need it)"
	fi
fi

say "Settings (.env)"
if [ -f "$INSTALL_DIR/.env" ]; then
	echo "  .env already exists, leaving it alone"
else
	if [ "$DRY" -eq 1 ]; then
		echo "  would write .env with the port, uid and the six secrets left blank"
	else
		{
			echo "OUTPOST_PORT=$PORT"
			echo "OUTPOST_UID=$(id -u)"
			echo "OUTPOST_GID=$(id -g)"
			echo ""
			echo "# The six credentials internal/config reads. Fill these in. They were"
			echo "# /etc/outpost.env under systemd; compose passes this file in as the"
			echo "# container's environment, and reads it itself for the OUTPOST_* values."
			echo "HETZNER_TOKEN="
			echo "CF_TOKEN="
			echo "TS_AUTHKEY="
			echo "R2_ACCESS_KEY="
			echo "R2_SECRET_KEY="
			echo ""
			echo "# Bearer token for the HTTP API (optional: the loopback listener and"
			echo "# tailscale serve are the boundary)."
			echo "OUTPOST_TOKEN="
		} >"$INSTALL_DIR/.env"
		chmod 600 "$INSTALL_DIR/.env"
		echo "  wrote .env (fill in the credentials; nothing will start without them)"
	fi
fi

say "Config"
if [ -f "$INSTALL_DIR/data/config.yaml" ]; then
	echo "  data/config.yaml already exists, leaving it alone"
elif [ -f "$INSTALL_DIR/config.yaml" ]; then
	run mv "$INSTALL_DIR/config.yaml" "$INSTALL_DIR/data/config.yaml"
	echo "  moved config.yaml into data/ (the container's working directory)"
else
	run cp "$INSTALL_DIR/config.example.yaml" "$INSTALL_DIR/data/config.yaml"
	echo "  copied config.example.yaml to data/config.yaml (edit it)"
fi

# A config that predates the mailbox has no update_signal_dir, and the daemon
# would come up with canUpdate: false — the Update button hidden and `outpost
# update` refusing — immediately after the watcher was installed to serve it.
if [ -f "$INSTALL_DIR/data/config.yaml" ] && ! grep -q '^update_signal_dir:' "$INSTALL_DIR/data/config.yaml"; then
	if [ "$DRY" -eq 1 ]; then
		echo "  would add update_signal_dir: /update-signal to data/config.yaml"
	else
		{
			echo ""
			echo "# Added by deploy/install.sh: the mailbox shared with the host update"
			echo "# watcher (internal/updatesignal). Empty means no watcher is installed."
			echo 'update_signal_dir: "/update-signal"'
		} >>"$INSTALL_DIR/data/config.yaml"
		echo "  added update_signal_dir to data/config.yaml"
	fi
fi

say "SSH key"
# The daemon is its own SSH client (internal/remote), so there is no ssh binary in
# the image and no ~/.ssh either: the key has to live in the mounted data folder.
run mkdir -p "$INSTALL_DIR/data/ssh"
if [ -f "$INSTALL_DIR/data/ssh/outpost" ]; then
	echo "  data/ssh/outpost already exists, leaving it alone"
else
	KEY_SRC=""
	# Where the config points now, if that is a path with a shell's tilde in it.
	KEY_SETTING="$(sed -n 's/^[[:space:]]*ssh_private_key:[[:space:]]*"\{0,1\}\([^"#]*\)"\{0,1\}.*/\1/p' "$INSTALL_DIR/data/config.yaml" 2>/dev/null | head -1)"
	case "$KEY_SETTING" in
	~/*) KEY_SRC="$HOME/${KEY_SETTING#\~/}" ;;
	*) KEY_SRC="$KEY_SETTING" ;;
	esac
	[ -f "$KEY_SRC" ] || KEY_SRC="$HOME/.ssh/outpost"
	if [ -f "$KEY_SRC" ]; then
		run cp "$KEY_SRC" "$INSTALL_DIR/data/ssh/outpost"
		run chmod 600 "$INSTALL_DIR/data/ssh/outpost"
		echo "  copied $KEY_SRC to data/ssh/outpost"
	else
		echo "  no key found: put the Hetzner SSH key at data/ssh/outpost (0600)"
	fi
fi

# Point the config at the copy, wherever it used to look. A host path — relative,
# absolute or tilde — is not a path the container has, and the value it wants is
# the one line that works.
if [ -f "$INSTALL_DIR/data/config.yaml" ] && [ -f "$INSTALL_DIR/data/ssh/outpost" ]; then
	if ! grep -q '^[[:space:]]*ssh_private_key:[[:space:]]*"\{0,1\}ssh/outpost"' "$INSTALL_DIR/data/config.yaml"; then
		if [ "$DRY" -eq 1 ]; then
			echo "  would point hetzner.ssh_private_key at ssh/outpost"
		else
			sed -i.bak -E 's|^([[:space:]]*)ssh_private_key:.*$|\1ssh_private_key: "ssh/outpost"|' "$INSTALL_DIR/data/config.yaml"
			rm -f "$INSTALL_DIR/data/config.yaml.bak"
			echo "  pointed hetzner.ssh_private_key at ssh/outpost"
		fi
	fi
fi

say "Host CLI wrapper"
# `outpost` on the host means "run it in the container", so the database has one
# writer and the commands in the README keep working. The daemon is the only
# process that ever opens outpost.db.
if [ "$DRY" -eq 1 ]; then
	echo "  would install /usr/local/bin/outpost"
else
	sudo tee /usr/local/bin/outpost >/dev/null <<WRAPPER
#!/bin/sh
# Outpost CLI. Runs in the container, where the database and the credentials are;
# 'outpost ...' means exactly this. Installed by deploy/install.sh.
#
# A terminal gets a TTY (outpost console -i needs one); a pipe does not, because
# compose's default TTY mangles redirected output.
if [ -t 0 ]; then TTY=; else TTY=-T; fi
exec docker compose -f "$INSTALL_DIR/docker-compose.yml" exec \$TTY outpost /app/outpost "\$@"
WRAPPER
	sudo chmod 0755 /usr/local/bin/outpost
fi

say "Update watcher (systemd units)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for unit in outpost-update.path outpost-update.service outpost-tick.service outpost-tick.timer; do
	sed -e "s|@INSTALL_DIR@|$INSTALL_DIR|g" -e "s|@USER@|$USER_NAME|g" \
		"$INSTALL_DIR/deploy/watcher/$unit" >"$tmp/$unit"
	if command -v systemd-analyze >/dev/null; then
		# Only report problems with this unit, not with other units on the machine.
		systemd-analyze verify "$tmp/$unit" 2>&1 | grep -F "$unit" || true
	fi
	run sudo install -m 644 "$tmp/$unit" "/etc/systemd/system/$unit"
done
run sudo systemctl daemon-reload
run sudo systemctl enable --now outpost-update.path outpost-tick.timer
run chmod +x "$INSTALL_DIR/deploy/watcher/update.sh"

say "Retire the old unit"
# The bare-metal unit is replaced by the container: it was stopped above, before its
# state was moved, and leaving it enabled would start a second daemon on the same
# database the first time the box reboots.
if [ -f /etc/systemd/system/outpost.service ]; then
	run sudo systemctl disable outpost.service
	echo "  disabled outpost.service (the container is the unit now)"
fi

if [ "$START" -eq 1 ]; then
	say "Starting Outpost"
	if [ "$DRY" -eq 1 ]; then
		echo "  would run: docker compose pull outpost && docker compose up -d (in $INSTALL_DIR)"
	else
		# Pull first, and never build: a cold build on this box is the thing the image
		# exists to avoid, and Compose would happily do one if the image were missing.
		if ! (cd "$INSTALL_DIR" && docker compose pull outpost); then
			echo "Could not pull the image. If GHCR says denied or not found, the package may be" >&2
			echo "private: make it public on GitHub (Packages, outpost, Package settings)." >&2
			exit 1
		fi
		(cd "$INSTALL_DIR" && docker compose up -d) || exit 1
	fi
fi

say "Checks"
if [ "$DRY" -eq 0 ]; then
	sleep 5
	printf '  %-34s' "outpost-update.path active:"; systemctl is-active outpost-update.path || true
	printf '  %-34s' "outpost-tick.timer active:"; systemctl is-active outpost-tick.timer || true
	printf '  %-34s' "the daemon answers /health:"; curl -fsS --max-time 5 "http://127.0.0.1:$PORT/health" || echo "not yet (give it a few seconds)"
	printf '  %-34s' "the container is healthy:"; docker inspect --format '{{.State.Health.Status}}' "$(cd "$INSTALL_DIR" && docker compose ps -q outpost)" 2>/dev/null || echo "unknown"
	printf '  %-34s' "the wrapper works:"; outpost status 2>&1 | head -1 || true
	echo
fi

cat <<NEXT

Next steps
  1. Fill in the credentials: $INSTALL_DIR/.env, then
       docker compose -f $INSTALL_DIR/docker-compose.yml up -d
  2. Edit the Minecraft settings: $INSTALL_DIR/data/config.yaml (the example has
     comments for every field).
  3. Put it on the tailnet:
       tailscale serve --bg $PORT http://127.0.0.1:$PORT
  4. Check the update mailbox is being read:
       systemctl start outpost-update.service
       cat $INSTALL_DIR/update-signal/result
  5. Meridian: point the outpost target at compose (see the homelab docs); its
     update action is 'systemctl start outpost-update.service', same as the
     siblings.
NEXT
