// Package cloudinit renders the user-data that turns a bare Ubuntu image into a
// running Minecraft server.
package cloudinit

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed cloud-init.yaml.tmpl
var tmpl string

var parsed = template.Must(template.New("cloud-init").Parse(tmpl))

// Data is everything the template needs. Secrets arrive here already read from
// the orchestrator's environment and are never written to disk anywhere else.
type Data struct {
	Hostname       string
	MCPort         int
	RCONPassword   string
	TailscaleAuthKey string
	TailscaleTags  []string
	R2AccountID    string
	R2AccessKey    string
	R2SecretKey    string
	AssetBucket    string
	WorldBucket    string
}

// Validate catches the mistakes that would otherwise show up as a half-built
// server twenty minutes later.
func (d Data) Validate() error {
	missing := []string{}
	if d.Hostname == "" {
		missing = append(missing, "hostname")
	}
	if d.MCPort == 0 {
		missing = append(missing, "mc port")
	}
	if d.RCONPassword == "" {
		missing = append(missing, "rcon password")
	}
	if d.TailscaleAuthKey == "" {
		missing = append(missing, "tailscale auth key")
	}
	if d.R2AccountID == "" || d.R2AccessKey == "" || d.R2SecretKey == "" {
		missing = append(missing, "r2 credentials")
	}
	if d.AssetBucket == "" || d.WorldBucket == "" {
		missing = append(missing, "bucket names")
	}
	if len(missing) > 0 {
		return fmt.Errorf("cloud-init: missing %s", strings.Join(missing, ", "))
	}
	for _, v := range []string{d.TailscaleAuthKey, d.R2AccessKey, d.R2SecretKey, d.RCONPassword} {
		if strings.ContainsAny(v, " \t\n'\"") {
			return fmt.Errorf("cloud-init: secret values must not contain whitespace or quotes")
		}
	}
	return nil
}

// Render produces the cloud-config document.
func Render(d Data) (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := parsed.Execute(&sb, d); err != nil {
		return "", fmt.Errorf("render cloud-init: %w", err)
	}
	return sb.String(), nil
}
