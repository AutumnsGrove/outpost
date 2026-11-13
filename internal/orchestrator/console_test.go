package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"outpost/internal/store"
)

type execRecorder struct {
	cmds []string
	out  string
	err  error
}

func (e *execRecorder) run(_ context.Context, _, _, cmd string) (string, error) {
	e.cmds = append(e.cmds, cmd)
	return e.out, e.err
}

func consoleHarness(t *testing.T) (*harness, *execRecorder) {
	t.Helper()
	h := runningHarness(t)
	rec := &execRecorder{out: "There are 0 of a max of 20 players online:"}
	h.o.deps.Exec = rec.run
	return h, rec
}

func TestConsoleRunsACommandAndAuditsIt(t *testing.T) {
	h, rec := consoleHarness(t)

	res, err := h.o.Console(context.Background(), "page", "/list")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output == "" || len(rec.cmds) != 1 || rec.cmds[0] != "list" {
		t.Fatalf("output=%q cmds=%v; a leading slash should be dropped", res.Output, rec.cmds)
	}

	events, _ := h.store.RecentEvents(5)
	found := false
	for _, e := range events {
		if e.Kind == "console" && strings.Contains(e.Message, "list (by page)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit row for the command: %+v", events)
	}
}

// Anything that ends the session from inside the game would be read by the
// orchestrator as a dead server and answered by saving and deleting it.
func TestConsoleRefusesWhatWouldEndTheSession(t *testing.T) {
	h, rec := consoleHarness(t)

	for _, cmd := range []string{
		"stop", "/stop", "STOP", "save-off", "  stop  ",
		"execute run stop",
		"execute as @a run stop",
		"execute as @a run save-off",
		"say hi; stop",
	} {
		_, err := h.o.Console(context.Background(), "page", cmd)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%q was not refused: %v", cmd, err)
		}
	}
	if len(rec.cmds) != 0 {
		t.Fatalf("refused commands reached the server: %v", rec.cmds)
	}

	// Ordinary commands that merely contain the letters are fine.
	for _, cmd := range []string{"op Stopwatch", "whitelist add Stopper", "say stopwatch", "time set day", "save-all", "save-on"} {
		if _, err := h.o.Console(context.Background(), "page", cmd); err != nil {
			t.Errorf("%q was refused: %v", cmd, err)
		}
	}
}

func TestConsoleRefusesEmptyMultilineAndHugeCommands(t *testing.T) {
	h, rec := consoleHarness(t)
	for _, cmd := range []string{"", "   ", "list\nstop", "say " + strings.Repeat("x", 2000)} {
		if _, err := h.o.Console(context.Background(), "page", cmd); !errors.Is(err, ErrRefused) {
			t.Errorf("%.20q: err=%v, want refusal", cmd, err)
		}
	}
	if len(rec.cmds) != 0 {
		t.Fatalf("reached the server: %v", rec.cmds)
	}
}

func TestConsoleSaysPlainlyWhenThereIsNoServer(t *testing.T) {
	h := newHarness(t) // OFFLINE
	rec := &execRecorder{}
	h.o.deps.Exec = rec.run

	_, err := h.o.Console(context.Background(), "cli", "list")
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
	if len(rec.cmds) != 0 {
		t.Fatal("a command was sent with no server")
	}
}

func TestConsoleReportsUnreachableRCONReadably(t *testing.T) {
	h, rec := consoleHarness(t)
	rec.err = errors.New("dial rcon: connection refused")

	_, err := h.o.Console(context.Background(), "page", "list")
	if err == nil || !strings.Contains(err.Error(), "rcon unreachable") {
		t.Fatalf("err = %v, want a readable rcon-unreachable error", err)
	}
	if errors.Is(err, ErrNotRunning) || errors.Is(err, ErrRefused) {
		t.Fatal("an unreachable server was reported as a refusal")
	}
}

func TestConsoleTruncatesAndSaysSo(t *testing.T) {
	h, rec := consoleHarness(t)
	rec.out = strings.Repeat("a long line of help text\n", 2000) // ~50 KB

	res, err := h.o.Console(context.Background(), "page", "help")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Output) > maxConsoleOutput+100 {
		t.Fatalf("truncated=%v len=%d", res.Truncated, len(res.Output))
	}
	if !strings.Contains(res.Output, "[truncated:") {
		t.Fatal("the cut is silent; it should say how much was dropped")
	}
}

func TestConsoleWorksWhileIdle(t *testing.T) {
	h, _ := consoleHarness(t)
	st, _ := h.store.GetState()
	st.State = store.Idle
	_ = h.store.SetState(st)
	if _, err := h.o.Console(context.Background(), "page", "list"); err != nil {
		t.Fatalf("console while IDLE: %v", err)
	}
}

func TestOnlineNamesComeFromTheProbe(t *testing.T) {
	h, _ := consoleHarness(t)
	_ = h.store.MetaSet("online_names", "Autumn,Felix")
	if got := h.o.OnlineNames(); len(got) != 2 || got[0] != "Autumn" {
		t.Fatalf("names = %v", got)
	}
	_ = h.o.Stop(context.Background())
	_ = h.o.Reset(context.Background(), true)
	if got := h.o.OnlineNames(); got != nil {
		t.Fatalf("names after the server is gone = %v", got)
	}
}
