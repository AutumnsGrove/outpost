// Package remote runs commands on the ephemeral VPS.
//
// Everything here goes over the tailnet by MagicDNS name, so no SSH port is
// exposed to the internet and no host keys are pinned to an address that will
// not exist in an hour.
//
// This speaks SSH itself rather than execing the system client, which it used to
// do. The daemon runs in a distroless container (PLAN 38): there is no `ssh`
// binary to exec and no `~/.ssh` to read, so "shell out to ssh" stopped being a
// thing the deploy could do. The library also removes three host-shaped
// assumptions along the way — no known_hosts to get wrong on a machine that is
// rebuilt every session, no ssh_config or agent to disagree with, and a timeout
// that is the context's rather than a flag's.
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshPort is the port every Hetzner image listens on. A variable so a test can
// point at an in-process server rather than at localhost:22.
var sshPort = "22"

// Runner runs commands as user with the private key at keyPath.
type Runner struct {
	user    string
	keyPath string
}

// New returns a runner that authenticates as user with the key at keyPath.
func New(user, keyPath string) *Runner {
	return &Runner{user: user, keyPath: keyPath}
}

// Run executes cmd on host and returns combined stdout and stderr.
//
// A non-zero exit is an error that carries the remote output, because "rclone
// said no" is the only useful thing to see in the log.
func (r *Runner) Run(ctx context.Context, host, cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	signer, err := r.signer()
	if err != nil {
		return "", err
	}

	client, err := dial(ctx, host, r.user, signer)
	if err != nil {
		return "", err
	}
	defer client.Close()

	// A wedged session is otherwise unbounded: the library has no equivalent of
	// ServerAliveInterval, so the context closes the transport instead.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh %s: open session: %w", host, err)
	}
	defer session.Close()

	var out bytes.Buffer
	session.Stdout = &out
	session.Stderr = &out

	if err := session.Run(cmd); err != nil {
		if ctx.Err() != nil {
			return out.String(), fmt.Errorf("ssh %s: %w", host, ctx.Err())
		}
		return out.String(), fmt.Errorf("ssh %s: %w: %s", host, err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// Ready reports whether SSH on host answers at all. Used while waiting for a
// freshly created VPS to finish booting.
func (r *Runner) Ready(ctx context.Context, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	_, err := r.Run(ctx, host, "true")
	return err == nil
}

// signer reads and parses the private key.
//
// It is read per call rather than at startup so that a key replaced on disk (or a
// profile whose key appears later) does not need a restart of the daemon — this
// runs a handful of times per session, not in a loop.
func (r *Runner) signer() (ssh.Signer, error) {
	path, err := expandPath(r.keyPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// Named, not wrapped: the container's usual version of this is "the key is
		// not in the mounted data directory", and the path is the whole answer.
		return nil, fmt.Errorf("read ssh private key %s: %w (a container reads it from the mounted data folder, not from ~/.ssh)", path, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse ssh private key %s: %w", path, err)
	}
	return signer, nil
}

// expandPath turns "~/x" into an absolute path. Config files are written for a
// shell, and a here-doc or a YAML value keeps the tilde.
func expandPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("no ssh private key is configured (hetzner.ssh_private_key)")
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("expand %s: no home directory in this environment; set hetzner.ssh_private_key to an absolute path (the container reads it from the mounted data folder)", path)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}

// dial opens one SSH connection. The host key is not verified, and cannot be: a
// VPS is created fresh for each session with a host key nobody has seen, so there
// is nothing to compare it against. What actually protects this channel is the
// tailnet (the host is only reachable through it) plus the key that is the only
// way to authenticate.
func dial(ctx context.Context, host, user string, signer ssh.Signer) (*ssh.Client, error) {
	addr := net.JoinHostPort(host, sshPort)
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // see above: nothing to pin
		Timeout:         10 * time.Second,
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	// The handshake has no context of its own, so a stalled peer is bounded by
	// closing the connection when the caller gives up.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	return ssh.NewClient(c, chans, reqs), nil
}
