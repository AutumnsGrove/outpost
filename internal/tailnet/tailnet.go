// Package tailnet answers "what is this node's address?" without asking DNS.
//
// MagicDNS is the right idea and a bad dependency for a control loop. A node
// that joined thirty seconds ago can still answer NXDOMAIN for minutes because
// a resolver cached the negative result, and the orchestrator reads that as
// "the server never came up" — on the first real session that mistake cost
// three minutes of provisioning and nearly a whole VPS.
//
// `tailscale status --json` is local, instant, and truthful, so that is what
// this asks instead. When it cannot answer (no tailscale, no such peer) the
// caller falls back to the name it already had.
package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// node is one machine in the tailnet, as `tailscale status --json` describes it.
type node struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

// Resolve returns the IPv4 tailnet address of host.
func Resolve(ctx context.Context, host string) (string, error) {
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale status: %w", err)
	}
	return ParseStatus(out, host)
}

// ParseStatus finds host in `tailscale status --json` and returns its first
// IPv4 address. It is exported so the parsing can be tested without a tailnet.
func ParseStatus(raw []byte, host string) (string, error) {
	var status struct {
		Self node            `json:"Self"`
		Peer map[string]node `json:"Peer"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return "", fmt.Errorf("parse tailscale status: %w", err)
	}

	host = strings.TrimSuffix(host, ".")
	nodes := []node{status.Self}
	for _, p := range status.Peer {
		nodes = append(nodes, p)
	}

	for _, n := range nodes {
		dns := strings.TrimSuffix(n.DNSName, ".")
		if !strings.EqualFold(n.HostName, host) && !strings.EqualFold(dns, host) && !strings.HasPrefix(strings.ToLower(dns), strings.ToLower(host)+".") {
			continue
		}
		for _, ip := range n.TailscaleIPs {
			// IPv4 only: the VPS also has an IPv6 tailnet address, and every
			// consumer here (RCON, ssh) is happier with one family.
			if !strings.Contains(ip, ":") {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no tailnet node named %q", host)
}
