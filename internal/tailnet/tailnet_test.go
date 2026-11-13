package tailnet

import "testing"

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
