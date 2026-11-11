#!/bin/bash
# watchdog.sh - Monitor player count and trigger shutdown/backup flows
# Run as minecraft user via systemd

set -e

IDLE_TIMEOUT=900        # 15 minutes (seconds)
SUSPEND_TIMEOUT=2700    # 45 minutes (seconds)
BACKUP_INTERVAL=1800    # 30 minutes (seconds)
WEBHOOK_URL="${WEBHOOK_URL:-https://admin.grove.place/api/mc/webhook}"
WEBHOOK_SECRET="${WEBHOOK_SECRET}"

LOG_FILE="/opt/minecraft/logs/watchdog.log"
MC_LOG="/opt/minecraft/logs/latest.log"

last_player_time=$(date +%s)
last_backup_time=$(date +%s)
suspend_start_time=0
state="running"
max_retry=3

log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $1" | tee -a "$LOG_FILE"
}

# More robust player count detection
get_player_count() {
    local count=0
    local retry=0

    while [ $retry -lt $max_retry ]; do
        # Send list command to Minecraft console
        screen -S minecraft -p 0 -X stuff "list\n" 2>/dev/null || true
        sleep 1

        # Try multiple patterns for player count
        # Pattern 1: "There are X of a max of Y players online"
        # Pattern 2: "There are X/Y players online"
        if [ -f "$MC_LOG" ]; then
            # Get last occurrence of player count
            count=$(tail -50 "$MC_LOG" | grep -oE 'There are [0-9]+' | tail -1 | grep -oE '[0-9]+' || echo "0")

            if [ -n "$count" ] && [ "$count" -ge 0 ] 2>/dev/null; then
                echo "$count"
                return 0
            fi
        fi

        retry=$((retry + 1))
        sleep 1
    done

    # Default to 0 if we can't determine
    echo "0"
}

sync_world_to_r2() {
    log "Syncing world to R2..."

    # Disable autosave
    screen -S minecraft -p 0 -X stuff "save-off\n" 2>/dev/null || true
    sleep 2

    # Force save
    screen -S minecraft -p 0 -X stuff "save-all\n" 2>/dev/null || true
    sleep 5

    # Create tarball
    if [ -d /opt/minecraft/world ]; then
        tar -czf /tmp/world.tar.gz -C /opt/minecraft world

        # Get size for reporting
        local size_bytes=$(stat -f%z /tmp/world.tar.gz 2>/dev/null || stat --printf="%s" /tmp/world.tar.gz 2>/dev/null || echo "0")

        # Upload to R2
        rclone copy /tmp/world.tar.gz r2:mc-worlds/current/ --retries 3

        # Create timestamped backup
        local timestamp=$(date +%Y-%m-%d-%H%M)
        rclone copy /tmp/world.tar.gz "r2:mc-worlds/backups/world-${timestamp}.tar.gz" --retries 3

        # Cleanup old backups (keep last 7 days)
        rclone delete r2:mc-worlds/backups/ --min-age 7d 2>/dev/null || true

        rm -f /tmp/world.tar.gz

        # Notify webhook
        curl -s -X POST "${WEBHOOK_URL}/backup-complete" \
            -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
            -H "Content-Type: application/json" \
            -d "{\"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\", \"sizeBytes\": $size_bytes}" \
            --retry 3 --retry-delay 2 || true

        log "World sync complete (${size_bytes} bytes)"
    else
        log "Warning: World directory not found"
    fi

    # Re-enable autosave
    screen -S minecraft -p 0 -X stuff "save-on\n" 2>/dev/null || true

    last_backup_time=$(date +%s)
}

notify_state_change() {
    local new_state="$1"
    log "State change: $new_state"

    curl -s -X POST "${WEBHOOK_URL}/state-change" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d "{\"state\": \"$new_state\", \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}" \
        --retry 3 --retry-delay 2 || true
}

send_heartbeat() {
    local player_count="$1"
    local idle_duration="$2"
    local backup_time="$3"

    # Use UTC time format that's portable
    local backup_iso=$(date -u -r "$backup_time" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "@$backup_time" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "")

    curl -s -X POST "${WEBHOOK_URL}/heartbeat" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d "{
            \"state\": \"$state\",
            \"players\": $player_count,
            \"idleSeconds\": $idle_duration,
            \"lastBackup\": \"$backup_iso\"
        }" \
        --retry 2 --retry-delay 1 || true
}

# Cleanup on exit
cleanup() {
    log "Watchdog shutting down"
    sync_world_to_r2
}
trap cleanup EXIT

log "Watchdog started (IDLE_TIMEOUT=${IDLE_TIMEOUT}s, SUSPEND_TIMEOUT=${SUSPEND_TIMEOUT}s)"

# Main loop
while true; do
    current_time=$(date +%s)
    player_count=$(get_player_count)

    # Ensure player_count is a valid number
    if ! [[ "$player_count" =~ ^[0-9]+$ ]]; then
        player_count=0
    fi

    # Update last player time if players are online
    if [ "$player_count" -gt 0 ]; then
        last_player_time=$current_time

        # Resume from suspended state if players join
        if [ "$state" = "suspended" ]; then
            log "Player joined, resuming from suspended state"
            systemctl start minecraft || true
            sleep 10
            state="running"
            notify_state_change "RUNNING"
        elif [ "$state" != "running" ]; then
            state="running"
            notify_state_change "RUNNING"
        fi
    fi

    idle_duration=$((current_time - last_player_time))
    backup_duration=$((current_time - last_backup_time))

    # Periodic backup while running
    if [ "$backup_duration" -ge "$BACKUP_INTERVAL" ] && [ "$state" = "running" ]; then
        sync_world_to_r2
    fi

    # State machine
    case $state in
        running)
            if [ "$player_count" -eq 0 ] && [ "$idle_duration" -ge "$IDLE_TIMEOUT" ]; then
                log "No players for ${IDLE_TIMEOUT}s, transitioning to IDLE"
                state="idle"
                notify_state_change "IDLE"
            fi
            ;;
        idle)
            # Check if we should suspend (Minecraft process stops, VPS stays warm)
            if [ "$player_count" -eq 0 ] && [ "$idle_duration" -ge $((IDLE_TIMEOUT + 300)) ]; then
                log "Idle for extended period, suspending Minecraft process"
                sync_world_to_r2
                systemctl stop minecraft || true
                state="suspended"
                suspend_start_time=$current_time
                notify_state_change "SUSPENDED"
            elif [ "$player_count" -gt 0 ]; then
                state="running"
                notify_state_change "RUNNING"
            fi
            ;;
        suspended)
            suspend_duration=$((current_time - suspend_start_time))

            if [ "$suspend_duration" -ge "$SUSPEND_TIMEOUT" ]; then
                log "Suspended for ${SUSPEND_TIMEOUT}s, signaling termination"
                notify_state_change "TERMINATING"

                # Wait a bit for the worker to delete the VPS
                # The VPS will be deleted externally; this is just a signal
                sleep 120

                # If we're still here, the worker might have failed
                # Continue waiting for external termination
                log "Waiting for external termination..."
            fi
            ;;
    esac

    # Send heartbeat
    send_heartbeat "$player_count" "$idle_duration" "$last_backup_time"

    # Sleep before next check
    sleep 30
done
