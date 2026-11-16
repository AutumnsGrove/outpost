// Package tailnet answers "what is this node's address?" without asking DNS.
//
// MagicDNS is the right idea and a bad dependency for a control loop. A node
// that joined thirty seconds ago can still answer NXDOMAIN for minutes because
// a resolver cached the negative result, and the orchestrator reads that as
// "the server never came up" — on the first real session that mistake cost
// three minutes of provisioning and nearly a whole VPS.
//
// `tailscaled`'s LocalAPI is local, instant, and truthful, so that is what this
// asks: the same `/localapi/v0/status` document `tailscale status --json` prints,
// over the unix socket tailscaled listens on. The CLI is the fallback for a
// machine where the socket is not where expected — it is the same document either
// way, parsed by the same function. When neither can answer (no tailscale, no such
// peer) the caller falls back to the name it already had.
//
// The socket, not the CLI, is the primary path because the daemon runs in a
// container: a distroless image has no `tailscale` binary to execute, and mounting
// one in plus its dependencies is a heavier answer than reading the socket the
// binary would have read (PLAN 38).
package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultSocket is where tailscaled's LocalAPI listens. The container mounts the
// host's socket at the same path, so this is the one location that works both
// ways; SocketEnv overrides it for a test or a non-standard install.
const (
	DefaultSocket = "/var/run/tailscale/tailscaled.sock"
	SocketEnv     = "OUTPOST_TAILSCALED_SOCKET"
)

// localAPIHost is the Host header tailscaled's LocalAPI requires. It is not a
// real name and is never resolved: the transport below dials the unix socket for
// any host, and the header is what tells tailscaled this request came from its own
// socket rather than a browser on the box (the same DNS-rebinding protection
// `tailscale serve` has).
const localAPIHost = "local-tailscaled.sock"

// node is one machine in the tailnet, as `tailscale status --json` describes it.
type node struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

// Socket is the path tailscaled's LocalAPI listens on.
func Socket() string {
	if p := os.Getenv(SocketEnv); p != "" {
		return p
	}
	return DefaultSocket
}

// Resolve returns the IPv4 tailnet address of host.
func Resolve(ctx context.Context, host string) (string, error) {
	raw, err := localAPIStatus(ctx)
	if err == nil {
		// The document is complete: a peer that is missing from it is missing from
		// the tailnet, and shelling out to `tailscale status` would answer the same
		// thing more slowly.
		return ParseStatus(raw, host)
	}

	out, cliErr := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if cliErr != nil {
		return "", fmt.Errorf("tailscale status: %w (the LocalAPI at %s said: %v)", cliErr, Socket(), err)
	}
	return ParseStatus(out, host)
}

// localAPIStatus reads the status document over tailscaled's unix socket.
func localAPIStatus(ctx context.Context) ([]byte, error) {
	socket := Socket()
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
			// One socket, one request: nothing to keep alive and no proxy, because a
			// proxy environment variable must not redirect a unix socket.
			DisableKeepAlives: true,
			Proxy:             nil,
		},
	}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+localAPIHost+"/localapi/v0/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask %s: %w", socket, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", socket, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s: %.160s", socket, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// ParseStatus finds host in a status document and returns its first IPv4 address.
// It is exported so the parsing can be tested without a tailnet.
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
