/**
 * Cloud-Init Template Generator
 * Generates cloud-init YAML for Hetzner VPS provisioning
 */

import type { Env, Region } from '../types/env.js';

export interface CloudInitConfig {
  region: Region;
  webhookUrl: string;
  tunnelToken?: string;  // Cloudflare Tunnel token for Dynmap
}

/**
 * Generate cloud-init YAML with secrets injected
 */
export function generateCloudInit(env: Env, config: CloudInitConfig): string {
  const { region, webhookUrl, tunnelToken } = config;

  // Escape any special characters in secrets for YAML
  const escapeYaml = (str: string) => str.replace(/'/g, "''");

  return `#cloud-config

package_update: true
package_upgrade: true

packages:
  - openjdk-17-jdk-headless
  - rclone
  - jq
  - tmux
  - screen
  - curl
  - wget
  - unzip

write_files:
  # Rclone config for R2
  - path: /root/.config/rclone/rclone.conf
    permissions: '0600'
    content: |
      [r2]
      type = s3
      provider = Cloudflare
      access_key_id = ${escapeYaml(env.R2_ACCESS_KEY)}
      secret_access_key = ${escapeYaml(env.R2_SECRET_KEY)}
      endpoint = https://${escapeYaml(env.CF_ACCOUNT_ID)}.r2.cloudflarestorage.com
      acl = private

  # Environment file for scripts
  - path: /opt/minecraft/.env
    permissions: '0600'
    content: |
      WEBHOOK_URL=${escapeYaml(webhookUrl)}
      WEBHOOK_SECRET=${escapeYaml(env.WEBHOOK_SECRET)}
      REGION=${region}

  # Systemd service for Minecraft
  - path: /etc/systemd/system/minecraft.service
    content: |
      [Unit]
      Description=Minecraft Server
      After=network.target

      [Service]
      User=minecraft
      WorkingDirectory=/opt/minecraft
      ExecStart=/opt/minecraft/start.sh
      ExecStop=/opt/minecraft/stop.sh
      Restart=on-failure
      RestartSec=5
      StandardOutput=append:/opt/minecraft/logs/service.log
      StandardError=append:/opt/minecraft/logs/service.log

      [Install]
      WantedBy=multi-user.target

  # Systemd service for Watchdog
  - path: /etc/systemd/system/mc-watchdog.service
    content: |
      [Unit]
      Description=Minecraft Watchdog
      After=minecraft.service
      Requires=minecraft.service

      [Service]
      User=minecraft
      WorkingDirectory=/opt/minecraft
      EnvironmentFile=/opt/minecraft/.env
      ExecStart=/opt/minecraft/watchdog.sh
      Restart=always
      RestartSec=10

      [Install]
      WantedBy=multi-user.target
${tunnelToken ? `
  # Cloudflare Tunnel service for Dynmap
  - path: /etc/systemd/system/cloudflared.service
    content: |
      [Unit]
      Description=Cloudflare Tunnel for Dynmap
      After=network.target minecraft.service

      [Service]
      Type=simple
      User=root
      ExecStart=/usr/local/bin/cloudflared tunnel run --token ${escapeYaml(tunnelToken)}
      Restart=always
      RestartSec=5

      [Install]
      WantedBy=multi-user.target
` : ''}
runcmd:
  # Create minecraft user
  - useradd -m -s /bin/bash minecraft
  - mkdir -p /opt/minecraft/logs
  - mkdir -p /opt/minecraft/mods
  - mkdir -p /opt/minecraft/config
  - mkdir -p /opt/minecraft/world
  - chown -R minecraft:minecraft /opt/minecraft

  # Copy rclone config to minecraft user
  - mkdir -p /home/minecraft/.config/rclone
  - cp /root/.config/rclone/rclone.conf /home/minecraft/.config/rclone/
  - chown -R minecraft:minecraft /home/minecraft/.config

  # Download server files from R2 (use copy, not sync, to avoid deleting other files)
  - sudo -u minecraft rclone copy r2:mc-assets/server/ /opt/minecraft/ --exclude "world/**"
  - sudo -u minecraft rclone copy r2:mc-assets/mods/ /opt/minecraft/mods/
  - sudo -u minecraft rclone copy r2:mc-assets/config/ /opt/minecraft/config/
  - sudo -u minecraft rclone copy r2:mc-assets/scripts/ /opt/minecraft/

  # Download world from R2 (if exists)
  - |
    cd /tmp
    # Check if world.tar.gz exists and has content
    WORLD_SIZE=$(rclone size r2:mc-worlds/current/world.tar.gz 2>/dev/null | grep "Total size:" | awk '{print $3}' || echo "0")
    if [ "$WORLD_SIZE" != "0" ] && [ -n "$WORLD_SIZE" ]; then
      echo "Downloading world.tar.gz ($WORLD_SIZE bytes)..."
      sudo -u minecraft rclone copy r2:mc-worlds/current/world.tar.gz /tmp/
      if [ -f /tmp/world.tar.gz ]; then
        sudo -u minecraft tar -xzf /tmp/world.tar.gz -C /opt/minecraft/
        rm /tmp/world.tar.gz
        echo "World restored from R2"
      else
        echo "Download failed, starting fresh"
      fi
    else
      echo "No existing world found in R2, Minecraft will generate a new world"
    fi

  # Recreate .env file (may have been overwritten by rclone sync)
  - |
    echo "WEBHOOK_URL=${escapeYaml(webhookUrl)}" > /opt/minecraft/.env
    echo "WEBHOOK_SECRET=${escapeYaml(env.WEBHOOK_SECRET)}" >> /opt/minecraft/.env
    echo "REGION=${region}" >> /opt/minecraft/.env
    chown minecraft:minecraft /opt/minecraft/.env
    chmod 600 /opt/minecraft/.env

  # Set permissions
  - chown -R minecraft:minecraft /opt/minecraft
  - chmod +x /opt/minecraft/*.sh
${tunnelToken ? `
  # Install cloudflared for Dynmap tunnel
  - curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared
  - chmod +x /usr/local/bin/cloudflared
` : ''}
  # Enable and start services
  - systemctl daemon-reload
  - systemctl enable minecraft mc-watchdog${tunnelToken ? ' cloudflared' : ''}
  - systemctl start minecraft
  - sleep 10
  - systemctl start mc-watchdog${tunnelToken ? ' cloudflared' : ''}

  # Notify worker that server is ready
  - |
    VPS_IP=$(curl -s http://169.254.169.254/hetzner/v1/metadata/public-ipv4)
    INSTANCE_ID=$(curl -s http://169.254.169.254/hetzner/v1/metadata/instance-id)
    curl -X POST "${escapeYaml(webhookUrl)}/ready" \\
      -H "Authorization: Bearer ${escapeYaml(env.WEBHOOK_SECRET)}" \\
      -H "Content-Type: application/json" \\
      -d "{\\"serverId\\": \\"$INSTANCE_ID\\", \\"ip\\": \\"$VPS_IP\\", \\"region\\": \\"${region}\\"}"

final_message: "GroveMC server setup complete after $UPTIME seconds"
`;
}

/**
 * Generate a minimal cloud-init for testing
 */
export function generateTestCloudInit(env: Env): string {
  return `#cloud-config

package_update: true

packages:
  - curl

runcmd:
  - echo "Test VM ready"
  - curl -X POST "${env.WEBHOOK_SECRET ? 'https://admin.grove.place/api/mc/webhook/ready' : 'http://localhost:8787/api/mc/webhook/ready'}" -H "Content-Type: application/json" -d '{"test": true}'

final_message: "Test VM ready after $UPTIME seconds"
`;
}
