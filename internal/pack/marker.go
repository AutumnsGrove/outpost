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

// staleInstall is how old the marker can be before a start stops trusting it. An
// install takes minutes; a marker older than this means the daemon died in the
// middle rather than that it is working.
const staleInstall = 30 * time.Minute

// MarkInstalling records an install in progress and returns the function that
// clears it. The profile is in the file so the refusal can name what is busy.
func MarkInstalling(dir, profile string) (func(), error) {
	if dir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, err
	}
	body := fmt.Sprintf("%s\n%s\n", profile, time.Now().UTC().Format(time.RFC3339))
	path := filepath.Join(dir, installMarker)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return func() {}, err
	}
	return func() { os.Remove(path) }, nil
}

// InstallingNow reports whether a profile is being installed, and which.
func InstallingNow(dir string) (profile string, since time.Time, ok bool) {
	if dir == "" {
		return "", time.Time{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, installMarker))
	if err != nil {
		return "", time.Time{}, false
	}
	lines := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)
	profile = strings.TrimSpace(lines[0])
	if len(lines) == 2 {
		since, _ = time.Parse(time.RFC3339, strings.TrimSpace(lines[1]))
	}
	if since.IsZero() || time.Since(since) > staleInstall {
		return "", time.Time{}, false
	}
	return profile, since, true
}
