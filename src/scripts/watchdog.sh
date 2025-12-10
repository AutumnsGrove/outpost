#!/bin/bash
# watchdog.sh - Monitor player count and trigger shutdown/backup flows
# Run as minecraft user

IDLE_TIMEOUT=900        # 15 minutes
SUSPEND_TIMEOUT=2700    # 45 minutes
BACKUP_INTERVAL=1800    # 30 minutes
WEBHOOK_URL="${WEBHOOK_URL:-https://admin.grove.place/api/mc/webhook}"
WEBHOOK_SECRET="${WEBHOOK_SECRET}"

last_player_time=$(date +%s)
last_backup_time=$(date +%s)
state="running"

get_player_count() {
    screen -S minecraft -p 0 -X stuff "list\n"
    sleep 0.5
    grep -oP 'There are \K[0-9]+' /opt/minecraft/logs/latest.log | tail -1
}

sync_world_to_r2() {
    echo "$(date): Syncing world to R2..."

    screen -S minecraft -p 0 -X stuff "save-off\n"
    screen -S minecraft -p 0 -X stuff "save-all\n"
    sleep 5

    tar -czf /tmp/world.tar.gz -C /opt/minecraft world
    rclone copy /tmp/world.tar.gz r2:mc-worlds/current/

    timestamp=$(date +%Y-%m-%d-%H%M)
    rclone copy /tmp/world.tar.gz r2:mc-worlds/backups/world-${timestamp}.tar.gz

    # Keep last 10 backups
    rclone delete r2:mc-worlds/backups/ --min-age 7d

    rm /tmp/world.tar.gz
    screen -S minecraft -p 0 -X stuff "save-on\n"

    curl -s -X POST "${WEBHOOK_URL}/backup-complete" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d '{"timestamp": "'$(date -Iseconds)'"}'

    last_backup_time=$(date +%s)
}

notify_state_change() {
    curl -s -X POST "${WEBHOOK_URL}/state-change" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d '{"state": "'$1'", "timestamp": "'$(date -Iseconds)'"}'
}

while true; do
    current_time=$(date +%s)
    player_count=$(get_player_count)

    if [ "$player_count" -gt 0 ]; then
        last_player_time=$current_time
        if [ "$state" != "running" ]; then
            state="running"
            notify_state_change "RUNNING"
        fi
    fi

    idle_duration=$((current_time - last_player_time))
    backup_duration=$((current_time - last_backup_time))

    if [ "$backup_duration" -ge "$BACKUP_INTERVAL" ] && [ "$state" = "running" ]; then
        sync_world_to_r2
    fi

    case $state in
        running)
            if [ "$player_count" -eq 0 ] && [ "$idle_duration" -ge "$IDLE_TIMEOUT" ]; then
                echo "$(date): No players for ${IDLE_TIMEOUT}s, suspending"
                state="suspended"
                notify_state_change "SUSPENDED"
                sync_world_to_r2
                systemctl stop minecraft
                suspend_start_time=$current_time
            fi
            ;;
        suspended)
            suspend_duration=$((current_time - suspend_start_time))
            if [ "$suspend_duration" -ge "$SUSPEND_TIMEOUT" ]; then
                echo "$(date): Suspended for ${SUSPEND_TIMEOUT}s, terminating"
                notify_state_change "TERMINATING"
                sleep 60
            fi
            ;;
    esac

    curl -s -X POST "${WEBHOOK_URL}/heartbeat" \
        -H "Authorization: Bearer ${WEBHOOK_SECRET}" \
        -H "Content-Type: application/json" \
        -d '{
            "state": "'$state'",
            "players": '$player_count',
            "idleSeconds": '$idle_duration',
            "lastBackup": "'$(date -d @$last_backup_time -Iseconds)'"
        }'

    sleep 30
done
