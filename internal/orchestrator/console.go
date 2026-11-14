package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"outpost/internal/rcon"
	"outpost/internal/store"
)

// Exec runs one RCON command and returns its output. Tests replace it.
type Exec func(ctx context.Context, addr, password, cmd string) (string, error)

const (
	// maxConsoleCommand keeps a pasted novel out of the audit trail.
	maxConsoleCommand = 1000
	// maxConsoleOutput is what a phone is sent. `help` and some mod commands
	// reply with thousands of lines.
	maxConsoleOutput = 8 << 10
	// consoleTimeout is the whole round trip. rcon.Command already gives up on
	// silence, and a command that wedges the main thread must come back as a
	// timeout rather than hold an HTTP request open.
	consoleTimeout = 20 * time.Second
)

// ErrNotRunning is what the console answers when there is nothing to talk to.
var ErrNotRunning = errors.New("server is not running")

// ErrRefused is wrapped by every command the console will not send.
var ErrRefused = errors.New("command refused")

// refused matches the commands that would take the server down underneath the
// orchestrator. `stop` is the obvious one: the next probe fails, the failure
// counter runs out, and the daemon "correctly" saves the world and deletes the
// VPS — a terrible way to find out. Matching anywhere in the line, not only at
// the start, is what stops `execute run stop` getting round it.
var refused = regexp.MustCompile(`(?i)(^|[\s/;])(stop|save-off)($|[\s;])`)

func execRCON(ctx context.Context, addr, password, cmd string) (string, error) {
	c, err := rcon.Dial(ctx, addr, password)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.Command(ctx, cmd)
}

// ConsoleResult is a command's output, cut to size.
type ConsoleResult struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
}

// Live reports whether there is a server to talk to.
func (o *Orchestrator) Live() bool {
	st, err := o.store.GetState()
	return err == nil && (st.State == store.Running || st.State == store.Idle)
}

// Console sends one command to the running server over RCON.
//
// It is authority over the game and over nothing else: RCON only ever reaches
// Minecraft, and nothing here shells out. who says where the command came from
// ("page", "api", "cli", "roster") and lands in the events trail with the
// command, so "who opped the griefer" has an answer.
func (o *Orchestrator) Console(ctx context.Context, who, cmd string) (ConsoleResult, error) {
	cmd = strings.TrimSpace(cmd)
	cmd = strings.TrimPrefix(cmd, "/")
	switch {
	case cmd == "":
		return ConsoleResult{}, fmt.Errorf("%w: empty command", ErrRefused)
	case len(cmd) > maxConsoleCommand:
		return ConsoleResult{}, fmt.Errorf("%w: longer than %d characters", ErrRefused, maxConsoleCommand)
	case strings.ContainsAny(cmd, "\r\n\x00"):
		return ConsoleResult{}, fmt.Errorf("%w: one command at a time", ErrRefused)
	case refused.MatchString(cmd):
		_ = o.store.Log("console-refused", "%s (by %s)", cmd, who)
		return ConsoleResult{}, fmt.Errorf("%w: stop and save-off would end the session underneath the orchestrator; use Save and stop", ErrRefused)
	}

	st, err := o.store.GetState()
	if err != nil {
		return ConsoleResult{}, err
	}
	if st.State != store.Running && st.State != store.Idle {
		return ConsoleResult{}, fmt.Errorf("%w (state is %s)", ErrNotRunning, st.State)
	}

	_ = o.store.Log("console", "%s (by %s)", cmd, who)

	ctx, cancel := context.WithTimeout(ctx, consoleTimeout)
	defer cancel()

	addr := net.JoinHostPort(o.dialHost(ctx, st), fmt.Sprint(rconPort))
	out, err := o.deps.Exec(ctx, addr, st.RCONPassword, cmd)
	if err != nil {
		return ConsoleResult{}, fmt.Errorf("rcon unreachable: %w", err)
	}

	res := ConsoleResult{Output: out}
	if len(out) > maxConsoleOutput {
		res.Output = out[:maxConsoleOutput] + fmt.Sprintf("\n… [truncated: %d more bytes]", len(out)-maxConsoleOutput)
		res.Truncated = true
	}
	return res, nil
}

// OnlineNames is who the last successful probe saw, or nil when nothing is live.
func (o *Orchestrator) OnlineNames() []string {
	if !o.Live() {
		return nil
	}
	v, _ := o.store.MetaGet("online_names")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}
