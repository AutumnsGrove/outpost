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

	// These keys are read by the orchestrator and by a friend's Minecraft client.
	// They are a fresh start (PLAN.md decision 6), but the layout is load-bearing.
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

func TestRenderKeepsThePublicPortSurfaceSmall(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Only Minecraft faces the internet. SSH and RCON are tailnet-only, so a
	// rule opening 22 is a bug even though it looks harmless.
	if strings.Contains(out, "ufw allow OpenSSH") || strings.Contains(out, "allow 22") {
		t.Fatal("cloud-init opens SSH to the internet; reach it over the tailnet")
	}
	if !strings.Contains(out, "ufw allow 25565/tcp") {
		t.Fatal("Minecraft's port is not opened")
	}
}

func TestRenderJoinsTheTailnetEphemerally(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "--ephemeral") {
		t.Fatal("the tailnet join is not ephemeral: every session would leave a dead device")
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
