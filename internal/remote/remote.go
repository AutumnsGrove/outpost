// Package remote runs commands on the ephemeral VPS.
//
// Everything here goes over the tailnet by MagicDNS name, so no SSH port is
// exposed to the internet and no host keys are pinned to an address that will
// not exist in an hour.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Runner shells out to the system ssh client.
type Runner struct {
	user string
	key  string
}

// New returns a runner that authenticates as user with the key at keyPath.
func New(user, keyPath string) *Runner {
	return &Runner{user: user, key: keyPath}
}

// Run executes cmd on host and returns combined stdout and stderr.
//
// A non-zero exit is an error that carries the remote output, because "rclone
// said no" is the only useful thing to see in the log.
func (r *Runner) Run(ctx context.Context, host, cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	args := []string{
		"-i", r.key,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "BatchMode=yes",
		fmt.Sprintf("%s@%s", r.user, host),
		cmd,
	}

	var out bytes.Buffer
	c := exec.CommandContext(ctx, "ssh", args...)
	c.Stdout = &out
	c.Stderr = &out

	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("ssh %s: %w: %s", host, err, out.String())
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
