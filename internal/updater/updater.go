// Package updater rebuilds Outpost from its git checkout and restarts the unit.
//
// This exists so a phone can update the daemon: Meridian's executor runs
// `outpost update` as `autumn`, and the status page has a button that does the
// same thing. Both paths end up here.
//
// Three rules, each because of a way this can go wrong:
//
//  1. Never swap in a binary that failed to build. `Restart=always` saves you
//     from a process that dies; it does not save you from one that starts and
//     loops. The build goes to `outpost.new` and only replaces the real binary
//     after it succeeds.
//  2. `git pull --ff-only`. If somebody edited the checkout on the box, this
//     fails loudly instead of creating a merge commit nobody can explain.
//  3. Restart is a separate step from build. The web handler has to answer the
//     browser before systemd kills the process writing that answer.
package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs a command and returns its combined output. It exists so the
// update logic can be tested without git, go or systemd.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// Exec is the real Runner.
type Exec struct{}

// Run executes name with args in dir.
func (Exec) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Update is one checkout that can be rebuilt in place.
type Update struct {
	// Dir is the git checkout, normally ~/outpost.
	Dir string
	// Binary is the running executable, replaced only after a good build.
	Binary string
	// Service is the systemd unit to restart, e.g. outpost.service.
	Service string
	// Runner defaults to Exec.
	Runner Runner
	Log    *slog.Logger
}

// Result describes what PullAndBuild did.
type Result struct {
	Before  string // short commit before the pull
	After   string // short commit after the pull
	Changed bool   // false when the checkout was already current
	Built   bool   // false when there was nothing to build
}

func (u Update) runner() Runner {
	if u.Runner != nil {
		return u.Runner
	}
	return Exec{}
}

func (u Update) log() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// PullAndBuild fast-forwards the checkout and replaces the binary if the build
// works. It never restarts anything: the caller decides when.
func (u Update) PullAndBuild(ctx context.Context) (Result, error) {
	var res Result
	if u.Dir == "" || u.Binary == "" {
		return res, errors.New("updater: no checkout directory or binary path")
	}
	if _, err := os.Stat(filepath.Join(u.Dir, ".git")); err != nil {
		return res, fmt.Errorf("updater: %s is not a git checkout (%w)", u.Dir, err)
	}

	r := u.runner()

	before, err := r.Run(ctx, u.Dir, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return res, err
	}
	res.Before = strings.TrimSpace(before)
	res.After = res.Before

	u.log().Info("pulling", "dir", u.Dir, "commit", res.Before)
	if _, err := r.Run(ctx, u.Dir, "git", "pull", "--ff-only"); err != nil {
		return res, fmt.Errorf("git pull failed (has the checkout been edited on this box?): %w", err)
	}

	after, err := r.Run(ctx, u.Dir, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return res, err
	}
	res.After = strings.TrimSpace(after)
	res.Changed = res.After != res.Before
	if !res.Changed {
		u.log().Info("already up to date", "commit", res.After)
		return res, nil
	}

	tmp := u.Binary + ".new"
	u.log().Info("building", "commit", res.After, "output", tmp)
	if _, err := r.Run(ctx, u.Dir, "go", "build", "-o", tmp, "./cmd/outpost"); err != nil {
		_ = os.Remove(tmp)
		return res, fmt.Errorf("build failed, keeping %s at %s: %w", u.Binary, res.Before, err)
	}

	if err := os.Rename(tmp, u.Binary); err != nil {
		_ = os.Remove(tmp)
		return res, fmt.Errorf("install new binary: %w", err)
	}
	res.Built = true
	u.log().Info("installed", "binary", u.Binary, "commit", res.After)
	return res, nil
}

// Restart asks systemd to restart the unit. It needs passwordless sudo for this
// one command, which the homelab box already grants `autumn`.
func (u Update) Restart(ctx context.Context) error {
	if u.Service == "" {
		return errors.New("updater: no unit to restart")
	}
	u.log().Info("restarting", "unit", u.Service)
	if _, err := u.runner().Run(ctx, "", "sudo", "-n", "systemctl", "restart", u.Service); err != nil {
		return err
	}
	return nil
}

// ForExecutable fills Dir, Binary and Service from the running process, so the
// daemon and the CLI need no configuration to update themselves.
func (u Update) ForExecutable(service string) (Update, error) {
	exe, err := os.Executable()
	if err != nil {
		return u, fmt.Errorf("locate executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return u, fmt.Errorf("resolve executable: %w", err)
	}
	u.Dir = filepath.Dir(exe)
	u.Binary = exe
	if service != "" {
		u.Service = service
	}
	return u, nil
}
