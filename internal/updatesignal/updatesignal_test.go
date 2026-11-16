package updatesignal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The whole contract with the watcher is these file names and the one word the
// daemon may write. Changing either silently breaks the host script, so it is
// asserted here rather than discovered on the potato.
func TestTheFileVocabularyIsTheContract(t *testing.T) {
	for name, want := range map[string]string{
		RequestFile:  "requested",
		CheckFile:    "check",
		AvailableLog: "available.json",
		ResultFile:   "result",
		HeartbeatLog: "heartbeat",
	} {
		if name != want {
			t.Errorf("const %q = %q, the watcher script says %q", name, name, want)
		}
	}
	if Latest != "latest" {
		t.Fatalf("the request word is %q; deploy/watcher/update.sh refuses anything but 'latest'", Latest)
	}
}

func TestRequestWritesTheOnlyWordItMay(t *testing.T) {
	m := Mailbox{Dir: t.TempDir()}
	if !m.Configured() {
		t.Fatal("a mailbox with a directory is configured")
	}
	if err := m.Request(); err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(m.Dir, RequestFile))
	if err != nil {
		t.Fatalf("read the request: %v", err)
	}
	if string(raw) != "latest\n" {
		t.Fatalf("request = %q", raw)
	}
	// No temporary file left behind for the watcher to trip over.
	if _, err := os.Stat(filepath.Join(m.Dir, RequestFile+".tmp")); err == nil {
		t.Fatal("an atomic write left its temporary file in the mailbox")
	}
}

func TestAZeroMailboxIsNotAnErrorUntilAskedToChangeSomething(t *testing.T) {
	m := Mailbox{}
	st, err := m.Status()
	if err != nil {
		t.Fatalf("status of an unconfigured mailbox: %v", err)
	}
	if st.Configured {
		t.Fatal("a zero mailbox reported itself configured")
	}
	if err := m.Request(); err == nil {
		t.Fatal("asking a mailbox that does not exist should be an error, not silence")
	}
}

func TestStatusReadsWhatTheWatcherWrote(t *testing.T) {
	dir := t.TempDir()
	m := Mailbox{Dir: dir}
	files := map[string]string{
		AvailableLog: `{"current":"aaaaaaaaaaaa","latest":"bbbbbbbbbbbb","latestDigest":"sha256:bbbb","checkedAt":"2026-11-16T10:00:00Z","updateAvailable":true}`,
		ResultFile:   `{"status":"ok","detail":"updated to bbbbbbbbbbbb","target":"ghcr.io/autumnsgrove/outpost@sha256:bbbb","finishedAt":"2026-11-16T10:01:00Z"}`,
		HeartbeatLog: "2026-11-16T10:00:30Z\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	st, err := m.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Available == nil || !st.Available.UpdateAvailable || st.Available.Latest != "bbbbbbbbbbbb" {
		t.Fatalf("available = %+v", st.Available)
	}
	if st.Result == nil || st.Result.Status != "ok" {
		t.Fatalf("result = %+v", st.Result)
	}
	if st.Heartbeat != "2026-11-16T10:00:30Z" {
		t.Fatalf("heartbeat = %q", st.Heartbeat)
	}
}

// A mailbox half-written by a killed watcher must read as "nothing to say": the
// page saying "update available" from a corrupt file is worse than saying nothing.
func TestStatusIgnoresFilesItCannotParse(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		AvailableLog: `{"latest":`,
		ResultFile:   `<html>`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := Mailbox{Dir: dir}.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Available != nil || st.Result != nil {
		t.Fatalf("unparseable files became %+v / %+v", st.Available, st.Result)
	}
}

func TestMaybeCheckOnlyAsksWhenTheAnswerIsStale(t *testing.T) {
	dir := t.TempDir()
	m := Mailbox{Dir: dir}
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

	// Nothing known yet: ask.
	m.MaybeCheck(now, 6*time.Hour)
	if _, err := os.Stat(filepath.Join(dir, CheckFile)); err != nil {
		t.Fatal("a fresh mailbox should be asked once")
	}
	_ = os.Remove(filepath.Join(dir, CheckFile))

	// A fresh answer: do not ask again. A page that polls every five seconds must
	// not become a registry lookup every five seconds.
	body := `{"current":"aaaaaaaaaaaa","latest":"aaaaaaaaaaaa","checkedAt":"2026-11-16T09:59:00Z","updateAvailable":false}`
	if err := os.WriteFile(filepath.Join(dir, AvailableLog), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m.MaybeCheck(now, 6*time.Hour)
	if _, err := os.Stat(filepath.Join(dir, CheckFile)); err == nil {
		t.Fatal("a fresh answer was asked for again")
	}

	// A stale answer, but the watcher looked a moment ago: wait. Without this, a
	// registry outage plus a five-second page poll is a GHCR request every five
	// seconds.
	if err := os.WriteFile(filepath.Join(dir, HeartbeatLog), []byte(now.Add(7*time.Hour-time.Minute).Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.MaybeCheck(now.Add(7*time.Hour), 6*time.Hour)
	if _, err := os.Stat(filepath.Join(dir, CheckFile)); err == nil {
		t.Fatal("asked again while the watcher had just looked")
	}

	// A stale one, and the watcher has not looked recently: ask.
	if err := os.WriteFile(filepath.Join(dir, HeartbeatLog), []byte(now.Add(-time.Hour).Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.MaybeCheck(now.Add(7*time.Hour), 6*time.Hour)
	if _, err := os.Stat(filepath.Join(dir, CheckFile)); err != nil {
		t.Fatal("a stale answer should be refreshed")
	}
}

func TestMaybeCheckIsSilentWithoutAMailbox(t *testing.T) {
	// Not a panic, not an error, not a file in the current directory.
	Mailbox{}.MaybeCheck(time.Now(), time.Hour)
}
