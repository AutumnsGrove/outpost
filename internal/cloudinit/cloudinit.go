// Package cloudinit renders the user-data that turns a bare Ubuntu image into a
// running Minecraft server.
package cloudinit

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed cloud-init.yaml.tmpl
var tmpl string

var parsed = template.Must(template.New("cloud-init").Parse(tmpl))

var (
	safeSecret = regexp.MustCompile(`^[A-Za-z0-9._~+=/:-]+$`)
	safeName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Data is everything the template needs. Secrets arrive here already read from
// the orchestrator's environment and are never written to disk anywhere else.
type Data struct {
	Hostname         string
	MCPort           int
	RCONPassword     string
	TailscaleAuthKey string
	TailscaleTags    []string
	R2AccountID      string
	R2AccessKey      string
	R2SecretKey      string
	AssetBucket      string
	WorldBucket      string
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
	// These land unquoted in shell commands and an INI file. An allowlist rather
	// than a blacklist: the real values are alphanumeric plus a few separators,
	// and a `$`, backtick or `;` in a pasted key is a typo to be caught here, not
	// a command to be run as root on a fresh server.
	for name, v := range map[string]string{
		"tailscale auth key": d.TailscaleAuthKey,
		"r2 access key":      d.R2AccessKey,
		"r2 secret key":      d.R2SecretKey,
		"rcon password":      d.RCONPassword,
	} {
		if !safeSecret.MatchString(v) {
			return fmt.Errorf("cloud-init: %s has characters outside A-Z a-z 0-9 . _ ~ + = / : -", name)
		}
	}
	for _, v := range []string{d.Hostname, d.AssetBucket, d.WorldBucket, d.R2AccountID} {
		if !safeName.MatchString(v) {
			return fmt.Errorf("cloud-init: %q is not a plain name (letters, digits, - . _)", v)
		}
	}
	for _, tag := range d.TailscaleTags {
		if !safeName.MatchString(strings.ReplaceAll(tag, ":", "-")) {
			return fmt.Errorf("cloud-init: tailscale tag %q is not a plain name", tag)
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
