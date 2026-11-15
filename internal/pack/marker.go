package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// installMarker is a file in the pack directory that says an install is rewriting
// a profile right now.
//
// A file rather than a field because the two callers are different processes:
// the daemon installs (from the page) and `outpost start` rents from a shell. A
// start that lands mid-install would pull a profile whose keys are half old and
// half new, which is the fifteen-minute-timeout-with-a-bill case `requireAssets`
// exists to prevent.
const installMarker = ".installing"

// staleInstall is how long the marker can go without being refreshed before a
// start stops trusting it. Progress refreshes it, so this measures silence rather
// than total duration: a slow install on a home connection stays marked, a daemon
// that died stops blocking rentals.
const staleInstall = 30 * time.Minute

// MarkInstalling records an install in progress. clear removes the marker and
// touch refreshes it; the caller must touch it as the install progresses, because
// freshness is what says "this is still happening".
func MarkInstalling(dir, profile string) (clear func(), touch func(), err error) {
	noop := func() {}
	if dir == "" {
		return noop, noop, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return noop, noop, err
	}
	path := filepath.Join(dir, installMarker)
	body := fmt.Sprintf("%s\n%s\n", profile, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return noop, noop, err
	}
	return func() { os.Remove(path) }, func() {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	}, nil
}

// InstallingNow reports whether a profile is being installed, and which.
func InstallingNow(dir string) (profile string, since time.Time, ok bool) {
	if dir == "" {
		return "", time.Time{}, false
	}
	path := filepath.Join(dir, installMarker)
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, false
	}
	// Refreshed by progress: silence for half an hour means the daemon died, not
	// that a big pack is still downloading.
	if time.Since(info.ModTime()) > staleInstall {
		return "", time.Time{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, false
	}
	lines := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)
	profile = strings.TrimSpace(lines[0])
	if len(lines) == 2 {
		since, _ = time.Parse(time.RFC3339, strings.TrimSpace(lines[1]))
	}
	return profile, since, true
}
