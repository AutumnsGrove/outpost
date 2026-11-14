package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"outpost/internal/orchestrator"
)

// The in-game /outpost command surface.
//
// Minecraft has no plugin API on Fabric, so `/outpost` is registered by a small
// server-side mod. That mod is a pipe and nothing else: it reads
// config/outpost.json for a URL and a token, sends {token, player, command,
// arg}, and prints whatever string comes back. Every rule — what a hold may be,
// what it costs, who is allowed — lives here in Go, where it has tests and where
// `curl` can exercise it without a Minecraft client anywhere in sight.
//
// Two locks on the door, in this order:
//
//   - the token, which is generated per session next to the RCON password and
//     written into the VPS by cloud-init. The jar ships in the public mods/
//     prefix, so a long-lived secret inside it would be a published secret.
//   - the ops list in R2, for hold and cancel. The daemon cannot see the
//     server's own /op state and must not trust the caller to report it.
//
// Neither is authentication. The tailnet is, as everywhere else here: the token
// says "this came from this session's jar", not "this player is who they say".

// ConfirmHoldAbove is where a hold stops being a convenience and starts being
// something to say out loud. See orchestrator.ConfirmHoldAbove, which owns the
// number because the CLI asks the same question.
const ConfirmHoldAbove = orchestrator.ConfirmHoldAbove

// pendingHoldTTL is how long a first half of a two-step hold remembers itself.
const pendingHoldTTL = 60 * time.Second

// gameMaxReply is what a chat window will take. Longer replies are cut: a wall
// of text in chat is unreadable and, on a slow client, unkind.
const gameMaxReply = 1024

type pendingHold struct {
	arg     string
	expires time.Time
}

// gameRequest is what the mod sends. Arg is the raw text after the subcommand so
// that the mod needs no opinion about splitting: `/outpost hold 48h confirm`
// arrives as command="hold", arg="48h confirm".
type gameRequest struct {
	Token   string `json:"token"`
	Player  string `json:"player"`
	Command string `json:"command"`
	Arg     string `json:"arg"`
}

// gameReply is what the mod prints. Message is already chat-shaped: short lines,
// no markup, no colours to parse. OK is false for a command that ran and refused
// — the token was good and the answer is still "no".
type gameReply struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// pending maps a player to the hold they have asked for but not confirmed.
// In memory on purpose: it is a sixty-second nicety, and a daemon restart in the
// middle of it should mean "ask again", not "resume a hold nobody re-read".
type pendingStore struct {
	mu sync.Mutex
	m  map[string]pendingHold
}

func (p *pendingStore) put(player, arg string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]pendingHold{}
	}
	p.m[strings.ToLower(player)] = pendingHold{arg: arg, expires: now.Add(pendingHoldTTL)}
}

func (p *pendingStore) take(player, arg string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToLower(player)
	held, ok := p.m[key]
	if !ok {
		return false
	}
	delete(p.m, key)
	return held.arg == arg && now.Before(held.expires)
}

// gameCommand is POST /api/game/command.
func (s *Server) gameCommand(w http.ResponseWriter, r *http.Request) {
	var body gameRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}

	st, err := s.store.GetState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot read state"})
		return
	}
	// Constant time, and refusal is the same for "no token configured" as for
	// "wrong token": a prober learns nothing about which it was.
	if st.GameToken == "" || subtle.ConstantTimeCompare([]byte(body.Token), []byte(st.GameToken)) != 1 {
		s.log.Warn("game command with a bad token", "player", body.Player, "command", body.Command)
		_ = s.store.Log("game-refused", "bad token for %q by %q", body.Command, body.Player)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad token"})
		return
	}
	if body.Player == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no player"})
		return
	}

	reply := s.runGameCommand(r, body)
	if len(reply.Message) > gameMaxReply {
		reply.Message = reply.Message[:gameMaxReply] + "\n…"
	}
	// 200 even when OK is false: the command was understood and answered, and
	// the mod's job is to print the sentence either way.
	writeJSON(w, http.StatusOK, reply)
}

// runGameCommand dispatches one command and returns the line to print.
func (s *Server) runGameCommand(r *http.Request, body gameRequest) gameReply {
	ctx := r.Context()

	switch strings.ToLower(strings.TrimSpace(body.Command)) {
	case "cost":
		cost, err := s.orch.Cost(ctx)
		if err != nil {
			return gameReply{Message: "cannot read the cost right now."}
		}
		return gameReply{OK: true, Message: formatCost(cost)}

	case "status":
		status, err := s.orch.Status(ctx)
		if err != nil {
			return gameReply{Message: "cannot read the status right now."}
		}
		hold, err := s.orch.HoldState()
		if err != nil {
			return gameReply{Message: "cannot read the status right now."}
		}
		return gameReply{OK: true, Message: formatStatus(status, hold)}

	case "hold":
		if !s.gameOp(ctx, body.Player) {
			_ = s.store.Log("game-refused", "hold by %q, not an operator", body.Player)
			return gameReply{Message: "only operators can hold the server open."}
		}
		return s.gameHold(r, body)

	case "cancel":
		if !s.gameOp(ctx, body.Player) {
			_ = s.store.Log("game-refused", "cancel by %q, not an operator", body.Player)
			return gameReply{Message: "only operators can drop a hold."}
		}
		was, err := s.orch.CancelHold(ctx, "game:"+body.Player)
		switch {
		case errors.Is(err, orchestrator.ErrNoHold):
			return gameReply{Message: "no hold is set."}
		case err != nil:
			return gameReply{Message: "could not drop the hold — " + err.Error()}
		}
		return gameReply{OK: true, Message: s.cancelMessage(ctx, was)}

	default:
		return gameReply{Message: "commands: cost, status, hold <48h>, cancel"}
	}
}

// gameHold is the two-step hold: above ConfirmHoldAbove, the first ask states
// the price and the second does it.
func (s *Server) gameHold(r *http.Request, body gameRequest) gameReply {
	ctx := r.Context()
	fields := strings.Fields(body.Arg)
	if len(fields) == 0 || len(fields) > 2 {
		return gameReply{Message: fmt.Sprintf(
			"usage: /outpost hold 48h, up to %s.",
			orchestrator.ShortDuration(s.orch.HoldMax()))}
	}
	d, err := time.ParseDuration(fields[0])
	if err != nil {
		return gameReply{Message: "give a length with a unit — 90m, 12h, 48h."}
	}
	if d < time.Minute {
		return gameReply{Message: "a hold needs to be at least a minute."}
	}
	// The cap is checked before the confirmation step, not after: offering
	// "reply confirm" for something that will then be refused is a worse answer
	// than refusing it now.
	if max := s.orch.HoldMax(); d > max {
		return gameReply{Message: fmt.Sprintf(
			"%s is longer than the maximum hold of %s.",
			orchestrator.ShortDuration(d), orchestrator.ShortDuration(max))}
	}
	confirm := len(fields) == 2 && strings.EqualFold(fields[1], "confirm")

	rate := s.orch.Rate()
	projected := rate * d.Hours()

	if confirm {
		if !s.pending.take(body.Player, fields[0], time.Now()) {
			return gameReply{Message: fmt.Sprintf(
				"nothing to confirm. Ask for the hold first — /outpost hold %s.", fields[0])}
		}
	} else if d > ConfirmHoldAbove {
		s.pending.put(body.Player, fields[0], time.Now())
		return gameReply{Message: fmt.Sprintf(
			"%s with nobody on is about %s.\nReply /outpost hold %s confirm to do it.",
			orchestrator.ShortDuration(d), money(projected), fields[0])}
	}

	res, err := s.orch.Hold(ctx, d, "game:"+body.Player)
	if err != nil {
		return gameReply{Message: holdRefusal(err)}
	}
	return gameReply{OK: true, Message: fmt.Sprintf(
		"Held until %s — %s with nobody on.\nThat is about %s, at %s/h.",
		res.Until.UTC().Format("2 Jan 15:04 UTC"), orchestrator.ShortDuration(res.Length),
		money(res.CostEUR), hourly(res.Rate))}
}

// gameOp asks R2 whether this player is an operator.
//
// The mod already refuses a non-op at permission level 2 before it sends
// anything, and this is the second half of that: the daemon does not take the
// caller's word for it, and the same check runs for curl. With no R2 credentials
// there is no roster to read, and the token is the only gate — which is the
// pre-existing behaviour of the player list, not a new hole.
func (s *Server) gameOp(ctx context.Context, player string) bool {
	if s.roster == nil {
		return true
	}
	snap, err := s.roster.List(ctx)
	if err != nil {
		s.log.Warn("cannot read the ops list for a game command", "err", err)
		return false
	}
	for _, op := range snap.Ops {
		if strings.EqualFold(op, player) {
			return true
		}
	}
	return false
}

// currency is "EUR" rather than the euro sign: the mod prints into Minecraft's
// chat font, and a glyph that falls back to a box in a cost readout is worse
// than three extra letters.
func money(v float64) string {
	switch {
	case v == 0:
		return "EUR 0"
	case v < 0.01:
		return fmt.Sprintf("EUR %.4f", v)
	default:
		return fmt.Sprintf("EUR %.2f", v)
	}
}

// hourly is the rate, always at four decimals: Hetzner prices by the fraction of
// a cent (EUR 0.0104/h), and rounding that to "EUR 0.01" in a cost readout is the
// one number a player is most likely to check.
func hourly(v float64) string {
	return fmt.Sprintf("EUR %.4f", v)
}

func hours(v float64) string {
	return orchestrator.ShortDuration(time.Duration(v * float64(time.Hour)))
}

// formatCost is "/outpost cost": this session, this month, the rate, and what a
// couple more hours would add. Three short lines, because chat is narrow.
func formatCost(c orchestrator.Cost) string {
	rate := hourly(c.HourlyRate)
	if !c.Live {
		return fmt.Sprintf("Nothing is running right now.\nThis month: %s, %s over %d session(s).\nThe rate here is %s/h.",
			hours(c.MonthHours), money(c.MonthCost), c.MonthSessions, rate)
	}
	return fmt.Sprintf("This session: %s, %s.\nThis month: %s, %s over %d session(s).\n%s/h — two more hours would add %s.",
		hours(c.SessionHours), money(c.SessionCost),
		hours(c.MonthHours), money(c.MonthCost), c.MonthSessions,
		rate, money(c.HourlyRate*2))
}

// cancelMessage says what dropping a hold leaves behind: the ordinary clock,
// measured from now.
func (s *Server) cancelMessage(ctx context.Context, was time.Time) string {
	window := "a while"
	if st, err := s.orch.Status(ctx); err == nil {
		window = orchestrator.ShortDuration(time.Duration(st.IdleGraceSec+st.IdleLimitSec) * time.Second)
	}
	return fmt.Sprintf("Hold dropped (was until %s).\nThe server shuts down %s after the last player leaves.",
		was.UTC().Format("2 Jan 15:04 UTC"), window)
}

// formatStatus is "/outpost status": state, players, shutdown, and any hold.
func formatStatus(st orchestrator.Status, hold orchestrator.HoldState) string {
	if st.State == "OFFLINE" {
		return "Nothing is running. Someone has to start it from the page or `outpost start`."
	}
	lines := []string{fmt.Sprintf("%s, %d player(s), up %s.",
		st.State, st.Players, orchestrator.ShortDuration(time.Duration(st.UptimeSec)*time.Second))}

	if hold.Active {
		lines = append(lines, fmt.Sprintf("Held until %s (%s left).",
			hold.Until.UTC().Format("2 Jan 15:04 UTC"),
			orchestrator.ShortDuration(hold.Left)))
	} else if st.State == "IDLE" && st.TTLSec > 0 {
		lines = append(lines, fmt.Sprintf("Shutting down in %s.",
			orchestrator.ShortDuration(time.Duration(st.TTLSec)*time.Second)))
	} else {
		lines = append(lines, fmt.Sprintf("No hold. Shuts down %s after the last player leaves.",
			orchestrator.ShortDuration(time.Duration(st.IdleGraceSec+st.IdleLimitSec)*time.Second)))
	}
	return strings.Join(lines, "\n")
}

// holdRefusal turns the orchestrator's errors into a sentence a player reads.
func holdRefusal(err error) string {
	switch {
	case errors.Is(err, orchestrator.ErrHoldTooLong):
		return "that is longer than the maximum hold — " + strings.TrimPrefix(err.Error(), orchestrator.ErrHoldTooLong.Error()+" ")
	case errors.Is(err, orchestrator.ErrNotRunning):
		return "there is no server to hold; start one first."
	default:
		return err.Error()
	}
}
