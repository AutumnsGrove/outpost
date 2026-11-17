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

// javaMajors are the JDK packages Ubuntu 24.04 has (8 and 11 in universe). A
// table rather than a check for positivity because the value becomes part of a
// package name, and the difference between a wrong package and a right one is a
// boot that fails on "no installation candidate".
var javaMajors = map[int]bool{8: true, 11: true, 17: true, 21: true}

// javaTarballs are the majors this image installs from Adoptium because Ubuntu
// has no package for them. A Minecraft version can need one of these: 26.x needs
// Java 25, and 24.04's newest JDK is 21, so the apt line would be "no installation
// candidate" after the orchestrator has already paid for a VPS.
//
// Two tables rather than one with a "how" column: the template needs exactly this
// decision — package or tarball — and a map to a bool is the smallest thing that
// cannot be half-filled.
var javaTarballs = map[int]bool{25: true}

// JavaTarball reports whether this JDK comes from Adoptium rather than apt.
func (d Data) JavaTarball() bool { return javaTarballs[d.Java] }

// Data is everything the template needs. Secrets arrive here already read from
// the orchestrator's environment and are never written to disk anywhere else.
type Data struct {
	Hostname string
	// Profile is which pack+world pair this VPS is for. It scopes every R2 key
	// the server reads and the shutdown upload writes, so two profiles never see
	// each other's mods or world.
	Profile string
	// Java is the JDK major version to install: the one the profile's pack says
	// its Minecraft version needs. It is passed in rather than looked up here so
	// that a boot and the `pack.json` the pack wrote are one fact, not two.
	Java             int
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
	if d.Profile == "" {
		missing = append(missing, "profile")
	}
	if d.Java == 0 {
		missing = append(missing, "java version")
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
	// A JDK is either a package name in the template ("openjdk-<n>-jdk-headless")
	// or an Adoptium tarball. Only the majors here are accepted, so a pack cannot
	// name one that does not exist and fail the boot on "no installation candidate"
	// — or on a 404 from Adoptium.
	if _, ok := javaMajors[d.Java]; !ok {
		if _, ok := javaTarballs[d.Java]; !ok {
			return fmt.Errorf("cloud-init: Java %d is not a JDK this image installs (Ubuntu packages 8, 11, 17, 21; Adoptium serves 25)", d.Java)
		}
	}
	for _, v := range []string{d.Hostname, d.Profile, d.AssetBucket, d.WorldBucket, d.R2AccountID} {
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
