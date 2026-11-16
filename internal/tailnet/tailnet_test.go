package tailnet

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shape taken from `tailscale status --json` on the Le Potato, trimmed to the
// fields Resolve reads. Note the IPv6 address comes first, as it does in the
// real output.
const statusJSON = `{
  "Self": {
    "HostName": "lepotato",
    "DNSName": "lepotato.taild95077.ts.net.",
    "TailscaleIPs": ["100.81.103.51", "fd7a:115c:a1e0::1"]
  },
  "Peer": {
    "nodekey:aaa": {
      "HostName": "outpost-eu-6ddd31",
      "DNSName": "outpost-eu-6ddd31.taild95077.ts.net.",
      "TailscaleIPs": ["100.93.226.83", "fd7a:115c:a1e0::2"]
    },
    "nodekey:bbb": {
      "HostName": "mini",
      "DNSName": "mini.taild95077.ts.net.",
      "TailscaleIPs": ["100.114.120.17", "fd7a:115c:a1e0::3"]
    }
  }
}`

func TestParseStatusFindsAPeerByHostname(t *testing.T) {
	got, err := ParseStatus([]byte(statusJSON), "outpost-eu-6ddd31")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != "100.93.226.83" {
		t.Fatalf("ip = %q, want the IPv4 tailnet address", got)
	}
}

func TestParseStatusAcceptsAFullyQualifiedName(t *testing.T) {
	for _, name := range []string{
		"outpost-eu-6ddd31.",
		"outpost-eu-6ddd31.taild95077.ts.net.",
		"outpost-eu-6ddd31.taild95077.ts.net",
	} {
		got, err := ParseStatus([]byte(statusJSON), name)
		if err != nil || got != "100.93.226.83" {
			t.Errorf("%q → %q (%v)", name, got, err)
		}
	}
}

func TestParseStatusFindsSelf(t *testing.T) {
	got, err := ParseStatus([]byte(statusJSON), "lepotato")
	if err != nil || got != "100.81.103.51" {
		t.Fatalf("%q (%v)", got, err)
	}
}

func TestParseStatusReportsAnUnknownNode(t *testing.T) {
	if _, err := ParseStatus([]byte(statusJSON), "outpost-eu-gone"); err == nil {
		t.Fatal("a node that is not in the tailnet should be an error")
	}
}

func TestParseStatusRejectsGarbage(t *testing.T) {
	if _, err := ParseStatus([]byte("<html>"), "anything"); err == nil {
		t.Fatal("non-JSON should be an error")
	}
}

// serveLocalAPI puts the status document behind a unix socket that looks like
// tailscaled's, and points the package at it.
func serveLocalAPI(t *testing.T, body string, status int) string {
	t.Helper()
	// A unix socket path has a length limit (~104 bytes on macOS), and t.TempDir()
	// under a deep /var/folders path can exceed it. A short directory of our own
	// and a long-enough name is not worth the portability risk, so the socket goes
	// in the shortest temp root available.
	dir, err := os.MkdirTemp("", "ts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "sock")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The LocalAPI's own protection: a request without the socket's Host header
		// did not come from a local client.
		if r.Host != "local-tailscaled.sock" {
			http.Error(w, "unexpected Host "+r.Host, http.StatusForbidden)
			return
		}
		if r.URL.Path != "/localapi/v0/status" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv(SocketEnv, path)
	return path
}

func TestResolveAsksTheLocalAPISocket(t *testing.T) {
	serveLocalAPI(t, statusJSON, http.StatusOK)

	got, err := Resolve(context.Background(), "outpost-eu-6ddd31")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "100.93.226.83" {
		t.Fatalf("ip = %q, want the IPv4 tailnet address", got)
	}
}

// A socket that is there but answers with an error is still a failure: the
// fallback is tried, and with no CLI to try the message has to name both, because
// a container debugging session otherwise starts by guessing which one ran.
func TestResolveReportsAnErrorFromTheLocalAPI(t *testing.T) {
	serveLocalAPI(t, "no", http.StatusInternalServerError)
	t.Setenv("PATH", t.TempDir()) // no tailscale binary to fall back to

	_, err := Resolve(context.Background(), "outpost-eu-6ddd31")
	if err == nil {
		t.Fatal("a 500 from the LocalAPI should be an error")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "LocalAPI") {
		t.Fatalf("error = %q, want it to carry the LocalAPI's answer", err)
	}
}

// The CLI is the fallback, and it is the only path on a box with no socket where
// the socket was expected. A binary of our own makes that testable without a real
// tailnet.
func TestResolveFallsBackToTheCLI(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tailscale")
	script := "#!/bin/sh\n[ \"$1\" = status ] || exit 2\necho '" + statusJSON + "'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// No socket where the package looks: the default path, and nothing listening.
	t.Setenv(SocketEnv, filepath.Join(dir, "missing.sock"))

	got, err := Resolve(context.Background(), "mini")
	if err != nil {
		t.Fatalf("resolve via the CLI: %v", err)
	}
	if got != "100.114.120.17" {
		t.Fatalf("ip = %q", got)
	}
}

func TestResolveSaysBothWaysFailed(t *testing.T) {
	t.Setenv(SocketEnv, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("PATH", t.TempDir()) // no tailscale binary either

	_, err := Resolve(context.Background(), "mini")
	if err == nil {
		t.Fatal("no socket and no CLI should be an error")
	}
	// The message has to name both, or a container debugging session starts by
	// guessing which one was even tried.
	if !strings.Contains(err.Error(), "tailscale status") || !strings.Contains(err.Error(), "LocalAPI") {
		t.Fatalf("error = %q", err)
	}
}
