// Package config loads Outpost's configuration.
//
// Non-secret settings live in config.yaml. Secrets come from the environment
// only, so the config file can be committed and copied between machines without
// anyone having to remember to redact it.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a string like "10m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Std returns the underlying time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Region is one place Outpost is willing to rent a server.
type Region struct {
	Location   string  `yaml:"location"`
	ServerType string  `yaml:"server_type"`
	HourlyRate float64 `yaml:"hourly_rate"`
}

// Server holds the lifecycle knobs.
type Server struct {
	// AssetBucket and WorldBucket are the R2 buckets . They keep v1's key
	// layout on purpose: the 188 mods and the existing world load unchanged.
	AssetBucket string `yaml:"asset_bucket"`
	WorldBucket string `yaml:"world_bucket"`

	MCPort int `yaml:"mc_port"`

	// IdleGrace is how long the server stays up with nobody on it before the
	// state flips to IDLE. IdleTimeout is how much longer than that it survives
	// before it is terminated. Total time billed after the last logout is
	// IdleGrace + IdleTimeout.
	IdleGrace   Duration `yaml:"idle_grace"`
	IdleTimeout Duration `yaml:"idle_timeout"`

	// ProvisionTimeout is how long to wait for RCON to answer before deciding
	// the VPS is a lemon and tearing it down.
	ProvisionTimeout Duration `yaml:"provision_timeout"`

	// TickInterval is the reconcile period.
	TickInterval Duration `yaml:"tick_interval"`
}

// Cloudflare identifies the DNS record to keep pointed at the current VPS.
type Cloudflare struct {
	ZoneID     string `yaml:"zone_id"`
	RecordName string `yaml:"record_name"`
}

// Hetzner holds everything needed to rent and reach a VPS.
type Hetzner struct {
	Image          string `yaml:"image"`
	SSHKeyID       int    `yaml:"ssh_key_id"`
	SSHPrivateKey  string `yaml:"ssh_private_key"`
	SSHUser        string `yaml:"ssh_user"`
	SnapshotID     int    `yaml:"snapshot_id"`
	ServerNameBase string `yaml:"server_name_base"`
}

// R2 is used by the VPS (via rclone) and by the orchestrator (to size backups).
type R2 struct {
	AccountID string `yaml:"account_id"`
}

// Tailscale joins the VPS to the tailnet so RCON and SSH never touch the
// public internet.
type Tailscale struct {
	Tags []string `yaml:"tags"`
}

// Secrets are read from the environment and never from the file.
type Secrets struct {
	HetznerToken     string
	CloudflareToken  string
	TailscaleAuthKey string
	R2AccessKey      string
	R2SecretKey      string
	APIToken         string
}

// Config is the whole thing.
type Config struct {
	Listen     string            `yaml:"listen"`
	Database   string            `yaml:"database"`
	Server     Server            `yaml:"server"`
	Regions    map[string]Region `yaml:"regions"`
	Cloudflare Cloudflare        `yaml:"cloudflare"`
	Hetzner    Hetzner           `yaml:"hetzner"`
	R2         R2                `yaml:"r2"`
	Tailscale  Tailscale         `yaml:"tailscale"`

	Secrets Secrets `yaml:"-"`
}

// Load reads path, applies defaults, then pulls secrets from the environment.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	c.applyDefaults()
	c.Secrets = Secrets{
		HetznerToken:     os.Getenv("HETZNER_TOKEN"),
		CloudflareToken:  os.Getenv("CF_TOKEN"),
		TailscaleAuthKey: os.Getenv("TS_AUTHKEY"),
		R2AccessKey:      os.Getenv("R2_ACCESS_KEY"),
		R2SecretKey:      os.Getenv("R2_SECRET_KEY"),
		APIToken:         os.Getenv("OUTPOST_TOKEN"),
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8896"
	}
	if c.Database == "" {
		c.Database = "outpost.db"
	}
	if c.Server.AssetBucket == "" {
		c.Server.AssetBucket = "mc-assets"
	}
	if c.Server.WorldBucket == "" {
		c.Server.WorldBucket = "mc-worlds"
	}
	if c.Server.MCPort == 0 {
		c.Server.MCPort = 25565
	}
	if c.Server.IdleGrace == 0 {
		c.Server.IdleGrace = Duration(10 * time.Minute)
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = Duration(20 * time.Minute)
	}
	if c.Server.ProvisionTimeout == 0 {
		c.Server.ProvisionTimeout = Duration(15 * time.Minute)
	}
	if c.Server.TickInterval == 0 {
		c.Server.TickInterval = Duration(20 * time.Second)
	}
	if c.Hetzner.Image == "" {
		c.Hetzner.Image = "ubuntu-24.04"
	}
	if c.Hetzner.SSHUser == "" {
		c.Hetzner.SSHUser = "root"
	}
	if c.Hetzner.ServerNameBase == "" {
		c.Hetzner.ServerNameBase = "outpost"
	}
	if c.Cloudflare.RecordName == "" {
		c.Cloudflare.RecordName = "mc.example.com"
	}
	if len(c.Regions) == 0 {
		// Estimates only, and Hetzner reprices: the 2026 hikes moved these more
		// than once. `hourly_rate` feeds the cost readout in `outpost status`,
		// never the actual bill. Check the console for the real rate and for
		// stock before trusting the defaults.
		c.Regions = map[string]Region{
			"eu": {Location: "nbg1", ServerType: "cx22", HourlyRate: 0.0063},
			"us": {Location: "ash", ServerType: "cx22", HourlyRate: 0.0063},
		}
	}
	if len(c.Tailscale.Tags) == 0 {
		c.Tailscale.Tags = []string{"tag:outpost"}
	}
}

func (c *Config) validate() error {
	missing := []string{}
	if c.Secrets.HetznerToken == "" {
		missing = append(missing, "HETZNER_TOKEN")
	}
	if c.Secrets.CloudflareToken == "" {
		missing = append(missing, "CF_TOKEN")
	}
	if c.Secrets.TailscaleAuthKey == "" {
		missing = append(missing, "TS_AUTHKEY")
	}
	if c.Secrets.R2AccessKey == "" {
		missing = append(missing, "R2_ACCESS_KEY")
	}
	if c.Secrets.R2SecretKey == "" {
		missing = append(missing, "R2_SECRET_KEY")
	}
	if c.R2.AccountID == "" {
		return fmt.Errorf("r2.account_id is required")
	}
	if c.Cloudflare.ZoneID == "" {
		return fmt.Errorf("cloudflare.zone_id is required")
	}
	if c.Hetzner.SSHKeyID == 0 {
		return fmt.Errorf("hetzner.ssh_key_id is required")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %v", missing)
	}
	if len(c.Regions) == 0 {
		return fmt.Errorf("at least one region is required")
	}
	return nil
}

// Region returns the named region, or an error naming the ones that exist.
func (c *Config) Region(name string) (Region, error) {
	r, ok := c.Regions[name]
	if !ok {
		return Region{}, fmt.Errorf("unknown region %q", name)
	}
	return r, nil
}

// DefaultRegion returns "eu" if configured, else the first region by name.
func (c *Config) DefaultRegion() string {
	if _, ok := c.Regions["eu"]; ok {
		return "eu"
	}
	for name := range c.Regions {
		return name
	}
	return ""
}
