#!/bin/bash
# stop.sh - Gracefully stop Minecraft server
# Run as minecraft user

cd /opt/minecraft

# Send stop command to screen session
screen -S minecraft -p 0 -X stuff "stop\n"

# Wait for server to stop (max 30 seconds)
for i in {1..30}; do
    if ! screen -list | grep -q "minecraft"; then
        echo "Server stopped gracefully"
        exit 0
    fi
    sleep 1
done

# Force kill if still running
echo "Server didn't stop gracefully, force killing..."
screen -S minecraft -X quit
exit 0
