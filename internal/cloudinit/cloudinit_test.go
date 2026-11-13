package cloudinit

import (
	"os"
	"os/exec"
	"path/filepath"
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

// The firewall and server.properties must agree about the port, or a custom
// mc_port yields a server that is up and unreachable.
func TestRenderOpensTheConfiguredPort(t *testing.T) {
	d := sample()
	d.MCPort = 25570
	out, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ufw allow 25570/tcp") || strings.Contains(out, "ufw allow 25565/tcp") {
		t.Fatal("firewall does not follow server.mc_port")
	}
	if !strings.Contains(out, "server-port=25570") {
		t.Fatal("server.properties does not follow server.mc_port")
	}
}

func TestValidateRejectsShellMetacharacters(t *testing.T) {
	for _, bad := range []string{"a$(reboot)", "a`id`", "a;b", "a&b", "a|b", "a\\b", "a#b", "a>b"} {
		d := sample()
		d.TailscaleAuthKey = bad
		if _, err := Render(d); err == nil {
			t.Fatalf("secret %q was accepted; it would be run as root", bad)
		}
	}
	d := sample()
	d.AssetBucket = "x; rm -rf /"
	if _, err := Render(d); err == nil {
		t.Fatal("a bucket name with shell syntax was accepted")
	}
}

// restoreScript pulls the world-restore step out of the rendered cloud-config.
func restoreScript(t *testing.T) string {
	t.Helper()
	out, err := Render(sample())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Runcmd []any `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Runcmd {
		if cmd, ok := c.(string); ok && strings.Contains(cmd, "lsf -R") {
			return cmd
		}
	}
	t.Fatal("no world-restore step in runcmd")
	return ""
}

// runRestore executes the REAL restore step under a sandbox: paths are
// redirected into a temp dir and rclone, sudo and chown are stubs. What it
// proves is the decision logic, which is the part that can wipe a world.
//
//	list: "fail" | "empty" | "present"      what `rclone lsf -R` does
//	copy: "ok" | "fail"                      what `rclone copyto` does
//	tar:  "good" | "bad"                     whether the downloaded archive extracts
func runRestore(t *testing.T, list, copy, tarball string) (noStart bool, worldExists bool, output string) {
	t.Helper()
	dir := t.TempDir()
	mc := filepath.Join(dir, "minecraft")
	bin := filepath.Join(dir, "bin")
	flag := filepath.Join(dir, "no-start")
	stage := filepath.Join(dir, "stage")
	for _, d := range []string{mc, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A pre-existing partial world, as left by an interrupted earlier step.
	if err := os.MkdirAll(filepath.Join(mc, "world"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A real archive for the success case, garbage for the corrupt one.
	src := filepath.Join(dir, "src")
	_ = os.MkdirAll(filepath.Join(src, "world"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "world", "level.dat"), []byte("real"), 0o644)
	archive := filepath.Join(dir, "world.tar.gz")
	if err := exec.Command("tar", "-czf", archive, "-C", src, "world").Run(); err != nil {
		t.Fatal(err)
	}
	if tarball == "bad" {
		_ = os.WriteFile(archive, []byte("not a tarball"), 0o644)
	}

	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("sudo", `shift 2; exec "$@"`) // sudo -u minecraft <cmd>
	stub("chown", `exit 0`)
	stub("rclone", `
case "$1" in
  lsf)
    case "`+list+`" in
      fail) echo "Failed to lsf: AccessDenied" >&2; exit 1;;
      empty) exit 0;;
      present) printf 'backups/world-1.tar.gz\ncurrent/world.tar.gz\n'; exit 0;;
    esac;;
  copyto)
    [ "`+copy+`" = fail ] && { echo "copyto: connection reset" >&2; exit 1; }
    cp "`+archive+`" "$3"; exit 0;;
esac`)

	script := restoreScript(t)
	script = strings.ReplaceAll(script, "/run/outpost-no-start", flag)
	script = strings.ReplaceAll(script, "/opt/minecraft", mc)
	script = strings.ReplaceAll(script, "/tmp/world", stage)

	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("script exited non-zero (allowed): %v", err)
	}
	_, flagErr := os.Stat(flag)
	_, worldErr := os.Stat(filepath.Join(mc, "world"))
	return flagErr == nil, worldErr == nil, string(out)
}

// A blip while checking for the world must never turn into a blank world that
// then overwrites the real one at shutdown.
func TestRestoreRefusesToStartWhenTheWorldBucketCannotBeListed(t *testing.T) {
	noStart, _, out := runRestore(t, "fail", "ok", "good")
	if !noStart {
		t.Fatalf("Minecraft would start on a blank world after a failed listing:\n%s", out)
	}
}

func TestRestoreRefusesToStartWhenAnExistingWorldCannotBeDownloaded(t *testing.T) {
	noStart, _, out := runRestore(t, "present", "fail", "good")
	if !noStart {
		t.Fatalf("a failed download fell through to a fresh world:\n%s", out)
	}
}

func TestRestoreRefusesToStartOnACorruptWorldAndRemovesTheWreckage(t *testing.T) {
	noStart, worldExists, out := runRestore(t, "present", "ok", "bad")
	if !noStart {
		t.Fatalf("a corrupt archive fell through to a fresh world:\n%s", out)
	}
	if worldExists {
		t.Fatalf("a half-extracted world was left in place for Minecraft to load:\n%s", out)
	}
}

func TestRestoreGeneratesAWorldOnlyWhenNoneExists(t *testing.T) {
	noStart, _, out := runRestore(t, "empty", "ok", "good")
	if noStart {
		t.Fatalf("a verifiably empty bucket must still allow a first world:\n%s", out)
	}
	if !strings.Contains(out, "no world in R2") {
		t.Fatalf("expected the first-world message, got:\n%s", out)
	}
}

func TestRestoreRestoresARealWorld(t *testing.T) {
	noStart, worldExists, out := runRestore(t, "present", "ok", "good")
	if noStart || !worldExists || !strings.Contains(out, "world restored") {
		t.Fatalf("noStart=%v worldExists=%v\n%s", noStart, worldExists, out)
	}
}

func TestMinecraftStartIsGuardedByTheRestoreFlag(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatal(err)
	}
	guard := strings.Index(out, "if [ -e /run/outpost-no-start ]")
	start := strings.Index(out, "systemctl start minecraft")
	if guard < 0 || start < 0 || guard > start {
		t.Fatal("systemctl start minecraft is not behind the no-start guard")
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

func TestRenderCanActuallyRestoreAWorld(t *testing.T) {
	out, err := Render(sample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The rclone runs as `minecraft`, so the staging directory must belong to
	// it before the copy, not after.
	if !strings.Contains(out, "chown minecraft:minecraft /tmp/world") {
		t.Fatal("nothing takes ownership of /tmp/world; the restore will fail with EACCES")
	}
	// And it must not swallow the reason: a silent restore failure overwrites a
	// real world with a fresh one and says "no world in R2".
	restore := out[strings.Index(out, "/tmp/world"):]
	if strings.Contains(restore[:600], "2>/dev/null") {
		t.Fatal("the world restore discards stderr; a failure would look like an empty bucket")
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
