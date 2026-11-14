package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"outpost/internal/store"
)

// setGameToken puts a session token on the live state, the way Start does.
func setGameToken(t *testing.T, f *fixture, token string) {
	t.Helper()
	st, err := f.store.GetState()
	if err != nil {
		t.Fatal(err)
	}
	st.GameToken = token
	if err := f.store.SetState(st); err != nil {
		t.Fatal(err)
	}
}

// opFile writes ops.json into the fake bucket, which is what the daemon reads to
// decide who may hold the server open.
func opFile(t *testing.T, f *fixture, names ...string) {
	t.Helper()
	players := make([]map[string]any, 0, len(names))
	for i, n := range names {
		players = append(players, map[string]any{
			"uuid": fmt.Sprintf("0000000%d-0000-0000-0000-000000000000", i), "name": n, "level": 4,
		})
	}
	raw, _ := json.Marshal(players)
	f.blobs.data["server/ops.json"] = raw
}

func gameCommand(t *testing.T, f *fixture, token, player, command, arg string) (int, gameReply) {
	t.Helper()
	body, _ := json.Marshal(gameRequest{Token: token, Player: player, Command: command, Arg: arg})
	rec := f.do("POST", "/api/game/command", string(body), nil)
	var out gameReply
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestGameCommandRefusesABadToken(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")

	for _, token := range []string{"", "wrong", "session-toke"} {
		code, _ := gameCommand(t, f, token, "AutumnsAdventure", "cost", "")
		if code != http.StatusUnauthorized {
			t.Errorf("token %q = %d, want 401", token, code)
		}
	}

	// A session with no token at all refuses everything: an empty token must not
	// match an empty field (the constant-time compare is length-safe, but the
	// intent is worth a test of its own).
	setGameToken(t, f, "")
	if code, _ := gameCommand(t, f, "", "AutumnsAdventure", "cost", ""); code != http.StatusUnauthorized {
		t.Errorf("no configured token = %d, want 401", code)
	}
}

func TestCostAndStatusAreForEveryoneAndFitInChat(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")

	for _, cmd := range []string{"cost", "status"} {
		code, reply := gameCommand(t, f, "session-token", "SomeRandomPlayer", cmd, "")
		if code != http.StatusOK || !reply.OK {
			t.Fatalf("%s = %d %+v, want 200 ok", cmd, code, reply)
		}
		if !strings.Contains(strings.ToLower(reply.Message), "outpost") &&
			!strings.Contains(strings.ToLower(reply.Message), "this session") &&
			!strings.Contains(strings.ToLower(reply.Message), "running") {
			t.Errorf("%s reply reads oddly: %q", cmd, reply.Message)
		}
		// The acceptance criterion is "readable in a chat window: short lines".
		for _, line := range strings.Split(reply.Message, "\n") {
			if len(line) > 100 {
				t.Errorf("%s line is %d chars, too long for chat: %q", cmd, len(line), line)
			}
		}
		if len(strings.Split(reply.Message, "\n")) > 4 {
			t.Errorf("%s is %d lines, a wall of text in chat", cmd, len(strings.Split(reply.Message, "\n")))
		}
	}
}

func TestHoldIsOpOnlyAndSaysSo(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")
	opFile(t, f, "AutumnsAdventure")

	_, reply := gameCommand(t, f, "session-token", "Griefer", "hold", "1h")
	if reply.OK {
		t.Fatal("a non-op was allowed to hold the server open")
	}
	if !strings.Contains(reply.Message, "operators") {
		t.Errorf("refusal should name the reason: %q", reply.Message)
	}
	st, _ := f.store.GetState()
	if !st.HoldUntil.IsZero() {
		t.Fatal("a refused hold was recorded anyway")
	}

	_, reply = gameCommand(t, f, "session-token", "autumnsadventure", "hold", "1h")
	if !reply.OK {
		t.Fatalf("the operator was refused: %q", reply.Message)
	}
	st, _ = f.store.GetState()
	if st.HoldUntil.IsZero() {
		t.Fatal("hold not recorded")
	}
	if !strings.Contains(reply.Message, "Held until") || !strings.Contains(reply.Message, "EUR") {
		t.Errorf("the reply should name the time and the price: %q", reply.Message)
	}

	// And cancel is op-only too.
	if _, reply := gameCommand(t, f, "session-token", "Griefer", "cancel", ""); reply.OK {
		t.Fatal("a non-op was allowed to drop the hold")
	}
	if _, reply := gameCommand(t, f, "session-token", "AutumnsAdventure", "cancel", ""); !reply.OK {
		t.Fatalf("the operator could not cancel: %q", reply.Message)
	}
}

func TestALongHoldCostsTwoSteps(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")
	opFile(t, f, "AutumnsAdventure")

	// 48h: states the price, does nothing yet.
	_, reply := gameCommand(t, f, "session-token", "AutumnsAdventure", "hold", "48h")
	if reply.OK {
		t.Fatal("48h took effect without confirming")
	}
	if !strings.Contains(reply.Message, "confirm") || !strings.Contains(reply.Message, "EUR") {
		t.Errorf("the first half should ask and quote: %q", reply.Message)
	}
	st, _ := f.store.GetState()
	if !st.HoldUntil.IsZero() {
		t.Fatal("the unconfirmed hold was recorded")
	}

	// The second half does it.
	_, reply = gameCommand(t, f, "session-token", "AutumnsAdventure", "hold", "48h confirm")
	if !reply.OK {
		t.Fatalf("confirm was refused: %q", reply.Message)
	}
	st, _ = f.store.GetState()
	if st.HoldUntil.IsZero() {
		t.Fatal("confirmed hold not recorded")
	}
	if left := time.Until(st.HoldUntil); left < 47*time.Hour || left > 49*time.Hour {
		t.Errorf("hold is %s long, want about 48h", left)
	}

	// A confirm with no pending ask is refused rather than guessed at.
	_, reply = gameCommand(t, f, "session-token", "AutumnsAdventure", "hold", "36h confirm")
	if reply.OK {
		t.Fatalf("an unasked-for confirm took effect: %q", reply.Message)
	}
}

func TestShortHoldsTakeOneStep(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")
	opFile(t, f, "AutumnsAdventure")

	_, reply := gameCommand(t, f, "session-token", "AutumnsAdventure", "hold", "2h")
	if !reply.OK {
		t.Fatalf("2h should not need confirming: %q", reply.Message)
	}
}

func TestHoldRefusesWhatItCannotHonour(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")
	opFile(t, f, "AutumnsAdventure")

	cases := map[string]string{
		"":      "48h",          // no argument
		"48":    "unit",         // no unit — is that hours or days? refuse it
		"1000h": "maximum hold", // past the cap, refused before it offers to confirm
		"30s":   "at least",     // too short to mean anything
	}
	for arg, want := range cases {
		_, reply := gameCommand(t, f, "session-token", "AutumnsAdventure", "hold", arg)
		if reply.OK {
			t.Errorf("hold %q was accepted", arg)
			continue
		}
		if !strings.Contains(strings.ToLower(reply.Message), strings.ToLower(want)) {
			t.Errorf("hold %q = %q, want it to mention %q", arg, reply.Message, want)
		}
	}
}

func TestEveryGameCommandLeavesAnEvent(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")
	opFile(t, f, "AutumnsAdventure")

	gameCommand(t, f, "session-token", "AutumnsAdventure", "hold", "1h")
	gameCommand(t, f, "session-token", "AutumnsAdventure", "cancel", "")
	gameCommand(t, f, "session-token", "Griefer", "hold", "1h")

	events, err := f.store.RecentEvents(30)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range events {
		got[e.Kind] = true
	}
	for _, kind := range []string{"hold", "hold-cancelled", "game-refused"} {
		if !got[kind] {
			t.Errorf("no %q row in the events table (got %v)", kind, got)
		}
	}
}

func TestUnknownGameCommandListsTheSurface(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")

	_, reply := gameCommand(t, f, "session-token", "AutumnsAdventure", "nonsense", "")
	if reply.OK || !strings.Contains(reply.Message, "cost") || !strings.Contains(reply.Message, "hold") {
		t.Errorf("unknown command should list what exists: %q", reply.Message)
	}
}

// The page's overview carries the hold and the month, because /api/status is a
// frozen contract (see TestStatusKeepsTheShapeMeridianReads).
func TestOverviewShowsTheHoldAndTheMonth(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")
	opFile(t, f, "AutumnsAdventure")
	gameCommand(t, f, "session-token", "AutumnsAdventure", "hold", "2h")

	got := decode(t, f.do("GET", "/api/overview", "", nil))
	hold, ok := got["hold"].(map[string]any)
	if !ok {
		t.Fatalf("no hold in the overview: %v", got)
	}
	if active, _ := hold["active"].(bool); !active {
		t.Fatalf("hold should be active: %v", hold)
	}
	if secs, _ := hold["seconds"].(float64); secs < 7100 || secs > 7300 {
		t.Errorf("hold seconds = %v, want about 7200", hold["seconds"])
	}
	month, ok := got["month"].(map[string]any)
	if !ok {
		t.Fatalf("no month in the overview: %v", got)
	}
	if _, ok := month["cost"].(float64); !ok {
		t.Errorf("month should carry a cost: %v", month)
	}
}

func TestTheGameTokenIsNotOnThePage(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	setGameToken(t, f, "session-token")

	for _, path := range []string{"/api/status", "/api/overview"} {
		if body := f.do("GET", path, "", nil).Body.String(); strings.Contains(body, "session-token") {
			t.Errorf("%s leaks the game token", path)
		}
	}
}

// store.ServerState's hold must survive the round trip through SQLite, which is
// what makes it survive a daemon restart. The orchestrator tests cover the
// behaviour; this covers the column.
func TestHoldColumnRoundTrips(t *testing.T) {
	f := newFixture(t, "")
	until := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)
	if err := f.store.SetState(store.ServerState{State: store.Running, HoldUntil: until}); err != nil {
		t.Fatal(err)
	}
	st, err := f.store.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.HoldUntil.Equal(until) {
		t.Fatalf("hold_until = %s, want %s", st.HoldUntil, until)
	}
}
