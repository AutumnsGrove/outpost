package roster

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"outpost/internal/publish"
	"outpost/internal/r2"
)

type memBlobs struct {
	data map[string][]byte
	puts int
}

func (m *memBlobs) Get(_ context.Context, _, key string) ([]byte, error) {
	b, ok := m.data[key]
	if !ok {
		return nil, r2.ErrNotFound
	}
	return b, nil
}

func (m *memBlobs) Put(_ context.Context, _, key string, r io.Reader, _ int64, _ string) error {
	b, _ := io.ReadAll(r)
	m.data[key] = b
	m.puts++
	return nil
}

func fakeMojang(_ context.Context, name string) (publish.Player, error) {
	if !publish.ValidName(name) {
		return publish.Player{}, errors.New("bad name")
	}
	// Canonicalise the spelling, the way Mojang does.
	canon := map[string]string{"autumnsadventure": "AutumnsAdventure", "felixfurr": "FelixFurr"}
	n := canon[strings.ToLower(name)]
	if n == "" {
		n = name
	}
	return publish.Player{UUID: "00000000-0000-0000-0000-" + strings.Repeat("0", 11) + string(rune('a'+len(n)%20)), Name: n}, nil
}

func newRoster() (*Roster, *memBlobs) {
	b := &memBlobs{data: map[string][]byte{}}
	return New(b, "mc-assets", fakeMojang), b
}

func TestAddWritesTheBootSourceAndIsIdempotent(t *testing.T) {
	r, blobs := newRoster()
	ctx := context.Background()

	p, added, err := r.Add(ctx, "autumnsadventure")
	if err != nil || !added || p.Name != "AutumnsAdventure" {
		t.Fatalf("add = %+v added=%v err=%v", p, added, err)
	}
	if !strings.Contains(string(blobs.data["server/whitelist.json"]), "AutumnsAdventure") {
		t.Fatal("the whitelist in R2 — what the next boot restores — was not written")
	}

	if _, added, _ := r.Add(ctx, "AutumnsAdventure"); added {
		t.Fatal("adding the same player twice should be a no-op")
	}
	if blobs.puts != 1 {
		t.Fatalf("puts = %d; a no-op must not rewrite the file", blobs.puts)
	}
}

func TestOpNeedsAWhitelistedPlayerAndUsesPublishsLevel(t *testing.T) {
	r, blobs := newRoster()
	ctx := context.Background()

	if _, _, err := r.Op(ctx, "Stranger"); !errors.Is(err, ErrNotWhitelisted) {
		t.Fatalf("op of a stranger = %v, want ErrNotWhitelisted", err)
	}
	if _, _, err := r.Add(ctx, "FelixFurr"); err != nil {
		t.Fatal(err)
	}
	name, changed, err := r.Op(ctx, "felixfurr")
	if err != nil || !changed || name != "FelixFurr" {
		t.Fatalf("op = %q changed=%v err=%v", name, changed, err)
	}
	ops := string(blobs.data["server/ops.json"])
	if !strings.Contains(ops, `"level": 4`) || !strings.Contains(ops, "bypassesPlayerLimit") {
		t.Fatalf("ops.json entry does not match what publish writes:\n%s", ops)
	}
}

func TestRemoveAlsoDropsOperatorStatus(t *testing.T) {
	r, _ := newRoster()
	ctx := context.Background()
	_, _, _ = r.Add(ctx, "FelixFurr")
	_, _, _ = r.Op(ctx, "FelixFurr")

	if _, changed, err := r.Remove(ctx, "felixfurr"); err != nil || !changed {
		t.Fatalf("remove: changed=%v err=%v", changed, err)
	}
	snap, err := r.List(ctx)
	if err != nil || len(snap.Whitelist) != 0 || len(snap.Ops) != 0 {
		t.Fatalf("after remove: %+v err=%v; an op who cannot join must not linger", snap, err)
	}
	if _, changed, _ := r.Remove(ctx, "FelixFurr"); changed {
		t.Fatal("removing someone who is not listed should report no change")
	}
}

func TestDeopKeepsThePlayerOnTheWhitelist(t *testing.T) {
	r, _ := newRoster()
	ctx := context.Background()
	_, _, _ = r.Add(ctx, "FelixFurr")
	_, _, _ = r.Op(ctx, "FelixFurr")
	if _, changed, err := r.Deop(ctx, "FelixFurr"); err != nil || !changed {
		t.Fatalf("deop: %v %v", changed, err)
	}
	snap, _ := r.List(ctx)
	if len(snap.Whitelist) != 1 || len(snap.Ops) != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

// A corrupt file must stop the edit. Reading it as empty and writing back one
// name would silently drop everyone else from the whitelist.
func TestACorruptWhitelistIsNotOverwritten(t *testing.T) {
	r, blobs := newRoster()
	blobs.data["server/whitelist.json"] = []byte(`[{"uuid": "x", "name": "Keep Me"`) // truncated

	if _, _, err := r.Add(context.Background(), "FelixFurr"); err == nil {
		t.Fatal("an add over a corrupt whitelist succeeded")
	}
	if !strings.Contains(string(blobs.data["server/whitelist.json"]), "Keep Me") {
		t.Fatal("the corrupt file was overwritten")
	}
}

func TestListWithNothingPublishedIsEmptyNotAnError(t *testing.T) {
	r, _ := newRoster()
	snap, err := r.List(context.Background())
	if err != nil || len(snap.Whitelist) != 0 {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
}

func TestApplyWritesR2BeforeTellingTheServer(t *testing.T) {
	r, blobs := newRoster()
	ctx := context.Background()

	var sent []string
	var r2HadItWhenSent bool
	send := func(_ context.Context, cmd string) error {
		sent = append(sent, cmd)
		r2HadItWhenSent = strings.Contains(string(blobs.data["server/whitelist.json"]), "FelixFurr")
		return nil
	}

	res, err := r.Apply(ctx, ActionAdd, "felixfurr", true, send)
	if err != nil || !res.Applied || !res.Changed {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(sent) != 1 || sent[0] != "whitelist add FelixFurr" {
		t.Fatalf("sent = %v; the canonical name should be used", sent)
	}
	if !r2HadItWhenSent {
		t.Fatal("the server was told before R2 was written: a failed write would leave a change that dies with the VPS")
	}
}

func TestApplyWithNoServerOnlyWritesR2(t *testing.T) {
	r, blobs := newRoster()
	res, err := r.Apply(context.Background(), ActionAdd, "FelixFurr", false, func(context.Context, string) error {
		t.Fatal("a command was sent with no server")
		return nil
	})
	if err != nil || res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(string(blobs.data["server/whitelist.json"]), "FelixFurr") {
		t.Fatal("not persisted")
	}
	if !strings.Contains(res.Message, "next starts") {
		t.Fatalf("message should say when it takes effect: %q", res.Message)
	}
}

func TestApplyKeepsTheR2ChangeWhenTheServerCannotBeReached(t *testing.T) {
	r, blobs := newRoster()
	res, err := r.Apply(context.Background(), ActionAdd, "FelixFurr", true, func(context.Context, string) error {
		return errors.New("rcon unreachable")
	})
	if err != nil {
		t.Fatalf("a live-apply failure must not fail the change: %v", err)
	}
	if res.Applied || !strings.Contains(res.Message, "could not be told") {
		t.Fatalf("res=%+v", res)
	}
	if !strings.Contains(string(blobs.data["server/whitelist.json"]), "FelixFurr") {
		t.Fatal("the persistent half was lost")
	}
}

// Names go into RCON commands. Only a real account name may reach them.
func TestApplyRefusesNamesThatCouldCarryACommand(t *testing.T) {
	r, _ := newRoster()
	for _, bad := range []string{"a b", "x\nstop", "Felix;stop", "ab", strings.Repeat("a", 17), "../etc", "Felix Furr", ""} {
		_, err := r.Apply(context.Background(), ActionAdd, bad, true, func(context.Context, string) error {
			t.Fatalf("%q reached the server", bad)
			return nil
		})
		if err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
