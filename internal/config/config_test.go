package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const minimal = `
listen: "127.0.0.1:8896"
database: "outpost.db"

server:
  idle_grace: "10m"
  idle_timeout: "20m"

regions:
  eu:
    location: nbg1
    server_type: cx32
    hourly_rate: 0.0119

cloudflare:
  zone_id: "zone123"
  record_name: "mc.example.com"

hetzner:
  ssh_key_id: 7
  ssh_private_key: "~/.ssh/outpost"

r2:
  account_id: "account123"
`

func withSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("HETZNER_TOKEN", "h")
	t.Setenv("CF_TOKEN", "c")
	t.Setenv("TS_AUTHKEY", "tskey-x")
	t.Setenv("R2_ACCESS_KEY", "a")
	t.Setenv("R2_SECRET_KEY", "b")
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	withSecrets(t)

	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Server.MCPort != 25565 {
		t.Errorf("mc port = %d", cfg.Server.MCPort)
	}
	if cfg.Server.AssetBucket != "mc-assets" || cfg.Server.WorldBucket != "mc-worlds" {
		t.Errorf("buckets = %q / %q", cfg.Server.AssetBucket, cfg.Server.WorldBucket)
	}
	if cfg.Server.IdleGrace.Std() != 10*time.Minute {
		t.Errorf("idle grace = %s", cfg.Server.IdleGrace.Std())
	}
	if cfg.Server.ProvisionTimeout.Std() != 15*time.Minute {
		t.Errorf("provision timeout = %s", cfg.Server.ProvisionTimeout.Std())
	}
	if cfg.Hetzner.Image != "ubuntu-24.04" {
		t.Errorf("image = %q", cfg.Hetzner.Image)
	}
	if cfg.Secrets.HetznerToken != "h" {
		t.Errorf("secrets not read from env")
	}
	if cfg.DefaultRegion() != "eu" {
		t.Errorf("default region = %q", cfg.DefaultRegion())
	}
}

func TestLoadRejectsMissingSecrets(t *testing.T) {
	// No env at all.
	for _, key := range []string{"HETZNER_TOKEN", "CF_TOKEN", "TS_AUTHKEY", "R2_ACCESS_KEY", "R2_SECRET_KEY"} {
		t.Setenv(key, "")
	}
	if _, err := Load(write(t, minimal)); err == nil {
		t.Fatal("expected an error when secrets are missing")
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	withSecrets(t)
	body := `
server:
  idle_grace: "ten minutes"
regions:
  eu: {location: nbg1, server_type: cx32, hourly_rate: 0.01}
cloudflare: {zone_id: z, record_name: mc.test}
hetzner: {ssh_key_id: 1, ssh_private_key: /dev/null}
r2: {account_id: a}
`
	if _, err := Load(write(t, body)); err == nil {
		t.Fatal("expected a duration parse error")
	}
}

func TestRegionLookup(t *testing.T) {
	withSecrets(t)
	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if _, err := cfg.Region("eu"); err != nil {
		t.Fatalf("eu should exist: %v", err)
	}
	if _, err := cfg.Region("mars"); err == nil {
		t.Fatal("unknown region should be an error")
	}
}
