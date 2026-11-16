// Package updatesignal is the mailbox between the daemon and the host update
// watcher.
//
// It exists because the daemon can no longer update itself: it is a container,
// built by CI and pulled by digest (PLAN 38). The thing that replaces the old
// in-process `git pull && go build && systemctl restart` is a script on the host
// with the privileges that need — it can pull an image and recreate a container,
// which the daemon inside one must not be able to do (no docker socket, read-only
// filesystem).
//
// So the two sides talk through a directory both can see, and only through it:
//
//	requested      daemon writes; "please update to the newest image"
//	check          daemon writes; "resolve the newest digest, change nothing"
//	available.json watcher writes; newest digest, whether it differs, when checked
//	result         watcher writes; what the last update did, or why it did not
//	heartbeat      watcher writes; when it last looked at the directory
//
// The files are the API's own JSON shape (camelCase, like every DTO), so the
// overview can embed what it reads without a second set of tags — the watcher's
// script writes exactly these keys.
//
// The files are tiny and the vocabulary is closed, which is the point: the daemon
// cannot name an image (it may only write the literal word "latest"), and it never
// reads a command from the watcher — the worst a compromised daemon can do is ask
// for the newest published build of its own repository.
package updatesignal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names, which the watcher's script also hardcodes. They are the contract.
const (
	RequestFile  = "requested"
	CheckFile    = "check"
	AvailableLog = "available.json"
	ResultFile   = "result"
	HeartbeatLog = "heartbeat"
)

// Latest is the only request the daemon may write.
const Latest = "latest"

// Available is what the watcher last saw on the registry.
type Available struct {
	// Current and Latest are short digests, for a page that has to show them side
	// by side without a 64-character wall.
	Current string `json:"current"`
	Latest  string `json:"latest"`
	// LatestDigest is the full sha256, which is what a pin is made of.
	LatestDigest string `json:"latestDigest"`
	CheckedAt    string `json:"checkedAt"`
	// UpdateAvailable is the watcher's answer to "is this worth doing".
	UpdateAvailable bool `json:"updateAvailable"`
}

// Result is the outcome of one update run.
type Result struct {
	Status     string `json:"status"` // ok | failed | skipped
	Detail     string `json:"detail"`
	Target     string `json:"target"`
	FinishedAt string `json:"finishedAt"`
}

// Status is everything the page may show about updates.
type Status struct {
	// Configured is false when the daemon has no mailbox: a binary built from a
	// checkout and run by hand, which is the development shape.
	Configured bool `json:"configured"`
	// Dir is the mailbox on disk, so a person debugging a stuck update has the path
	// in the page rather than in a unit file.
	Dir       string     `json:"dir,omitempty"`
	Available *Available `json:"available,omitempty"`
	Result    *Result    `json:"result,omitempty"`
	// Heartbeat is when the watcher last looked. A watcher that has never run, or
	// one that stopped, is the failure this field exists to make visible: the
	// update button would otherwise be a request nobody reads.
	Heartbeat string `json:"heartbeat,omitempty"`
}

// Mailbox is the directory. A zero Mailbox is not configured, and every method
// tolerates that: the CLI does not have one, and the daemon may be started without
// the mount.
type Mailbox struct {
	Dir string
}

// Configured reports whether there is a mailbox at all.
func (m Mailbox) Configured() bool { return m.Dir != "" }

// Request asks the watcher to update to the newest image.
func (m Mailbox) Request() error {
	if !m.Configured() {
		return errors.New("no update mailbox is configured, so there is nobody to ask: this daemon updates with the image it was built from")
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", m.Dir, err)
	}
	// The literal word, not a digest: the daemon cannot name an image. The watcher
	// refuses anything else, which is what makes that a real boundary rather than a
	// convention.
	return writeAtomic(filepath.Join(m.Dir, RequestFile), Latest+"\n")
}

// Check asks the watcher to resolve the newest digest without changing anything.
// The watcher writes available.json as a result.
func (m Mailbox) Check() error {
	if !m.Configured() {
		return errors.New("no update mailbox is configured")
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", m.Dir, err)
	}
	return writeAtomic(filepath.Join(m.Dir, CheckFile), Latest+"\n")
}

// CheckFloor is the shortest interval between two checks the page may ask for. The
// heartbeat — which every watcher run writes, including one that failed — is what
// bounds it: without a floor, a registry outage would turn a five-second page poll
// into a GHCR request every five seconds, because the answer never gets fresher.
const CheckFloor = 10 * time.Minute

// MaybeCheck writes the check signal when the watcher's answer is missing or older
// than maxAge, so a page reload does not turn into a registry lookup on every poll.
//
// It never returns an error worth showing: not being able to ask is not a reason
// for the overview to fail, and the update button reports its own trouble.
func (m Mailbox) MaybeCheck(now time.Time, maxAge time.Duration) {
	if !m.Configured() {
		return
	}
	st, err := m.Status()
	if err != nil {
		return
	}
	if st.Available != nil {
		if at, err := time.Parse(time.RFC3339, st.Available.CheckedAt); err == nil && now.Sub(at) < maxAge {
			return
		}
	}
	if at, err := time.Parse(time.RFC3339, st.Heartbeat); err == nil && now.Sub(at) < CheckFloor {
		return
	}
	_ = m.Check()
}

// Status reads the mailbox. A missing mailbox, a missing file and an unreadable
// file are all "nothing to say" — the page should show no update line rather than
// an error about a file it cannot help with.
func (m Mailbox) Status() (Status, error) {
	st := Status{Configured: m.Configured(), Dir: m.Dir}
	if !m.Configured() {
		return st, nil
	}
	if b, err := os.ReadFile(filepath.Join(m.Dir, HeartbeatLog)); err == nil {
		st.Heartbeat = strings.TrimSpace(string(b))
	}
	st.Available = readJSON[Available](filepath.Join(m.Dir, AvailableLog))
	st.Result = readJSON[Result](filepath.Join(m.Dir, ResultFile))
	return st, nil
}

// readJSON decodes one file, and answers nil for "no file", "not JSON" and "wrong
// shape" alike: a corrupt mailbox entry is not something the page can act on, and
// pretending there is an update (or that there was a successful one) would be
// worse than saying nothing.
func readJSON[T any](path string) *T {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return &v
}

// writeAtomic writes a whole file or nothing: the watcher reacts to a file
// appearing, so it must never see half of one.
func writeAtomic(path, body string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
