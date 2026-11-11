package cloudinit

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func sample() Data {
	return Data{
		Hostname:         "outpost-eu-abc123",
		MCPort:           25565,
		RCONPassword:     "deadbeef",
		TailscaleAuthKey: "tskey-auth-abc",
		TailscaleTags:    []string{"tag:outpost"},
		R2AccountID:      "accountid",
		R2AccessKey:      "access",
		R2SecretKey:      "secret",
		AssetBucket:      "mc-assets",
		WorldBucket:      "mc-worlds",
	}
}

func TestRenderIsValidCloudConfig(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The single most useful assertion here: cloud-init fails the whole boot on
	// a YAML error, and a YAML error is exactly what template edits cause.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("rendered cloud-config is not valid YAML: %v\n%s", err, out)
	}

	if doc["hostname"] != "outpost-eu-abc123" {
		t.Fatalf("hostname not rendered: %v", doc["hostname"])
	}
	if _, ok := doc["runcmd"]; !ok {
		t.Fatal("runcmd missing")
	}
	if strings.Contains(out, "{{") {
		t.Fatal("template directives left in output")
	}
}

func TestRenderKeepsV1BucketLayout(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The 188 mods and the existing world live at these exact keys. Changing
	// them silently breaks a working world, so pin them.
	for _, want := range []string{
		"r2:mc-assets/server/",
		"r2:mc-assets/mods/",
		"r2:mc-assets/scripts/",
		"r2:mc-worlds/current/world.tar.gz",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cloud-init no longer references %s", want)
		}
	}
}

func TestRenderTurnsRCONOn(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "enable-rcon=true") {
		t.Fatal("RCON is not enabled; the orchestrator would never see the server")
	}
	if !strings.Contains(out, "rcon.password=deadbeef") {
		t.Fatal("session RCON password not written")
	}
}

func TestValidateRejectsMissingData(t *testing.T) {
	cases := map[string]func(*Data){
		"hostname":          func(d *Data) { d.Hostname = "" },
		"rcon":              func(d *Data) { d.RCONPassword = "" },
		"tailscale":         func(d *Data) { d.TailscaleAuthKey = "" },
		"r2":                func(d *Data) { d.R2SecretKey = "" },
		"port":              func(d *Data) { d.MCPort = 0 },
		"bucket":            func(d *Data) { d.WorldBucket = "" },
		"secret whitespace": func(d *Data) { d.RCONPassword = "has space" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := sample()
			mutate(&d)
			if _, err := Render(d); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
