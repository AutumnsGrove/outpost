package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner answers git/go/systemctl from a script instead of running them.
type fakeRunner struct {
	commits  []string // successive `rev-parse` answers
	pulled   bool
	buildErr error
	restart  []string
	commands []string
}

func (f *fakeRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	f.commands = append(f.commands, strings.Join(append([]string{name}, args...), " "))

	switch {
	case name == "git" && len(args) > 0 && args[0] == "rev-parse":
		if len(f.commits) == 0 {
			return "aaaaaaa\n", nil
		}
		c := f.commits[0]
		if len(f.commits) > 1 {
			f.commits = f.commits[1:]
		}
		return c + "\n", nil
	case name == "git" && len(args) > 0 && args[0] == "pull":
		f.pulled = true
		return "", nil
	case name == "go":
		if f.buildErr != nil {
			return "", f.buildErr
		}
		// The real build writes the file named after -o; mimic that so the
		// rename has something to rename.
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				return "", os.WriteFile(args[i+1], []byte("new binary"), 0o755)
			}
		}
		return "", errors.New("no -o in the build arguments")
	case name == "sudo":
		f.restart = append(f.restart, strings.Join(args, " "))
		return "", nil
	}
	return "", nil
}

func checkout(t *testing.T, commits ...string) (dir, binary string, r *fakeRunner) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(dir, "outpost")
	if err := os.WriteFile(binary, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, binary, &fakeRunner{commits: commits}
}

func TestPullAndBuildReplacesTheBinary(t *testing.T) {
	dir, binary, r := checkout(t, "aaaaaaa", "bbbbbbb")
	u := Update{Dir: dir, Binary: binary, Service: "outpost.service", Runner: r}

	res, err := u.PullAndBuild(context.Background())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !res.Changed || !res.Built {
		t.Fatalf("result = %+v, want changed and built", res)
	}
	if res.Before != "aaaaaaa" || res.After != "bbbbbbb" {
		t.Fatalf("commits = %s → %s", res.Before, res.After)
	}

	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("binary = %q, want the rebuilt one", got)
	}
	if _, err := os.Stat(binary + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the temporary build artefact was left behind")
	}
	if !r.pulled {
		t.Fatal("git pull was never run")
	}
}

func TestPullAndBuildKeepsTheOldBinaryWhenTheBuildFails(t *testing.T) {
	dir, binary, r := checkout(t, "aaaaaaa", "bbbbbbb")
	r.buildErr = errors.New("compile error in cmd/outpost")

	u := Update{Dir: dir, Binary: binary, Service: "outpost.service", Runner: r}
	if _, err := u.PullAndBuild(context.Background()); err == nil {
		t.Fatal("a failed build should be an error")
	}

	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Fatalf("binary = %q, the failed build must not be installed", got)
	}
	if r.restart != nil {
		t.Fatal("a failed build must not restart the service")
	}
}

func TestPullAndBuildSkipsWhenAlreadyCurrent(t *testing.T) {
	dir, binary, r := checkout(t, "aaaaaaa")
	u := Update{Dir: dir, Binary: binary, Service: "outpost.service", Runner: r}

	res, err := u.PullAndBuild(context.Background())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if res.Changed || res.Built {
		t.Fatalf("result = %+v, want nothing to do", res)
	}
	for _, c := range r.commands {
		if strings.HasPrefix(c, "go build") {
			t.Fatal("rebuilt an unchanged checkout")
		}
	}
}

func TestPullAndBuildRefusesANonCheckout(t *testing.T) {
	dir := t.TempDir()
	u := Update{Dir: dir, Binary: filepath.Join(dir, "outpost"), Runner: &fakeRunner{}}
	if _, err := u.PullAndBuild(context.Background()); err == nil {
		t.Fatal("a directory that is not a git checkout should be refused")
	}
}

func TestRestartUsesTheUnit(t *testing.T) {
	_, _, r := checkout(t)
	u := Update{Dir: "/tmp", Binary: "/tmp/outpost", Service: "outpost.service", Runner: r}
	if err := u.Restart(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if len(r.restart) != 1 || r.restart[0] != "-n systemctl restart outpost.service" {
		t.Fatalf("restart commands = %v", r.restart)
	}
}
