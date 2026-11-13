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

func TestRenderHandsMinecraftOwnershipBeforeThePull(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The rclone pull runs as `minecraft`, and a root-owned /opt/minecraft makes
	// every copy fail with EACCES. Order matters, so assert the order.
	chown := strings.Index(out, "chown -R minecraft:minecraft /opt/minecraft")
	pull := strings.Index(out, "rclone copy r2:mc-assets/server/")
	if chown < 0 || pull < 0 {
		t.Fatalf("chown=%d pull=%d", chown, pull)
	}
	if chown > pull {
		t.Fatal("the minecraft user owns /opt/minecraft only after the pull; every file copy will fail")
	}
}

// tailscaleJoin returns the rendered `tailscale up` command, so assertions do
// not trip over the comments that explain it.
func tailscaleJoin(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- tailscale up") {
			return line
		}
	}
	t.Fatal("no tailscale up command in the rendered cloud-config")
	return ""
}

func TestRenderOmitsTagsUnlessConfigured(t *testing.T) {
	untagged := sample()
	untagged.TailscaleTags = nil
	out, err := Render(untagged)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(tailscaleJoin(t, out), "--advertise-tags") {
		t.Fatal("an untagged key cannot advertise a tag; the join would fail")
	}

	tagged := sample()
	tagged.TailscaleTags = []string{"tag:outpost"}
	out, err = Render(tagged)
	if err != nil {
		t.Fatalf("render tagged: %v", err)
	}
	if !strings.Contains(tailscaleJoin(t, out), "--advertise-tags=tag:outpost") {
		t.Fatal("configured tags were not passed through")
	}
}

func TestRenderDoesNotPassUnsupportedTailscaleFlags(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// tailscale 1.104.6 rejects --ephemeral outright and skips the join, leaving
	// the VPS unreachable over the tailnet. Ephemerality belongs on the auth key.
	join := tailscaleJoin(t, out)
	for _, flag := range []string{"--ephemeral", "--accept-routes", "--shielded"} {
		if strings.Contains(join, flag) {
			t.Errorf("cloud-init passes %s to `tailscale up`; 1.104.6 does not accept it", flag)
		}
	}
}

func TestRenderSetsUpAnRcloneThatWorksWithR2(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Three separate ways the world upload has failed against real R2:
	//   * `acl = private` — canned ACLs are a 403 on PUT, reads still work.
	//   * Ubuntu's rclone 1.60 — 501 NotImplemented on upload.
	//   * no_check_bucket — a bucket-scoped token 403s the HEAD, rclone then
	//     tries CreateBucket and dies AccessDenied.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "acl") {
			t.Fatalf("rclone config sets an ACL: %q", line)
		}
	}
	if !strings.Contains(out, "no_check_bucket = true") {
		t.Error("rclone config is missing no_check_bucket; a scoped token cannot pass the bucket check")
	}
	if !strings.Contains(out, "downloads.rclone.org") {
		t.Error("cloud-init does not install a current rclone; Ubuntu noble ships 1.60, which R2 rejects with 501")
	}
	if strings.Contains(out, "  - rclone\n") {
		t.Error("cloud-init installs rclone from apt, which is too old for R2")
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
