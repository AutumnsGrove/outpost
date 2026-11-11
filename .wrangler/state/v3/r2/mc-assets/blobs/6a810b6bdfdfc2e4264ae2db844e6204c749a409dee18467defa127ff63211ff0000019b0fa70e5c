#!/bin/bash
# sync-to-r2.sh - Manual world backup to R2
# Run as minecraft user

cd /opt/minecraft

echo "Starting manual world backup..."

# Disable auto-save temporarily
screen -S minecraft -p 0 -X stuff "save-off\n"
screen -S minecraft -p 0 -X stuff "save-all\n"
sleep 5

# Create tarball
tar -czf /tmp/world.tar.gz -C /opt/minecraft world

# Upload to R2
rclone copy /tmp/world.tar.gz r2:mc-worlds/current/

# Create timestamped backup
timestamp=$(date +%Y-%m-%d-%H%M)
rclone copy /tmp/world.tar.gz r2:mc-worlds/backups/world-${timestamp}.tar.gz

# Cleanup
rm /tmp/world.tar.gz

# Re-enable auto-save
screen -S minecraft -p 0 -X stuff "save-on\n"

echo "Backup complete: world-${timestamp}.tar.gz"
