package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := "database: " + filepath.Join(dir, "outpost.db") + "\n" +
		"cloudflare: {zone_id: z, record_name: mc.test}\n" +
		"hetzner: {ssh_key_id: 1}\n" +
		"r2: {account_id: a}\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearSecrets(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HETZNER_TOKEN", "CF_TOKEN", "TS_AUTHKEY", "R2_ACCESS_KEY", "R2_SECRET_KEY", "OUTPOST_TOKEN"} {
		t.Setenv(k, "")
	}
}

// requiresCloud once carried the value "true", which setup() does not act on, so
// every command that rents or deletes servers ran with no credential check and
// found out about a missing token at the first API call — after renting.
func TestEveryCommandThatTouchesTheCloudValidatesItsCredentials(t *testing.T) {
	clearSecrets(t)
	cfg := writeConfig(t)

	for _, name := range []string{"serve", "start", "stop", "tick", "reset"} {
		root := rootCmd()
		root.SetArgs([]string{name, "--config", cfg})
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "missing required environment variables") {
			t.Errorf("`outpost %s` with no credentials = %v, want a missing-credentials error", name, err)
		}
	}
}

func TestStatusNeedsNoCredentials(t *testing.T) {
	clearSecrets(t)
	root := rootCmd()
	root.SetArgs([]string{"status", "--config", writeConfig(t)})
	if err := root.Execute(); err != nil {
		t.Fatalf("status should work on a box with nothing exported: %v", err)
	}
}

func TestEveryCommandDeclaresAKnownCredentialRequirement(t *testing.T) {
	for _, c := range rootCmd().Commands() {
		if v, ok := c.Annotations[requiresCloud]; ok && v != "all" && v != "r2" {
			t.Errorf("`outpost %s` has requiresCloud=%q, which setup() ignores", c.Name(), v)
		}
	}
}
