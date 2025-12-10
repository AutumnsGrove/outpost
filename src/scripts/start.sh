#!/bin/bash
# start.sh - Start Minecraft server
# Run as minecraft user in /opt/minecraft

cd /opt/minecraft

# Memory settings for 8GB VPS (leave 1-2GB for OS)
MEMORY="-Xms4G -Xmx6G"

# JVM flags for Minecraft (Aikar's flags)
JVM_FLAGS="-XX:+UseG1GC -XX:+ParallelRefProcEnabled -XX:MaxGCPauseMillis=200 -XX:+UnlockExperimentalVMOptions -XX:+DisableExplicitGC -XX:+AlwaysPreTouch -XX:G1NewSizePercent=30 -XX:G1MaxNewSizePercent=40 -XX:G1HeapRegionSize=8M -XX:G1ReservePercent=20 -XX:G1HeapWastePercent=5 -XX:G1MixedGCCountTarget=4 -XX:InitiatingHeapOccupancyPercent=15 -XX:G1MixedGCLiveThresholdPercent=90 -XX:G1RSetUpdatingPauseTimePercent=5 -XX:SurvivorRatio=32 -XX:+PerfDisableSharedMem -XX:MaxTenuringThreshold=1 -Dusing.aikars.flags=https://mcflags.emc.gs -Daikars.new.flags=true"

# Start in screen session
screen -dmS minecraft java $MEMORY $JVM_FLAGS -jar fabric-server-launch.jar nogui

echo "Minecraft server starting..."
