package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"outpost/internal/store"
)

// ErrHoldTooLong is what a hold beyond server.max_hold answers.
var ErrHoldTooLong = errors.New("hold is longer than the maximum")

// ErrNoHold is what CancelHold answers when there is nothing to drop.
var ErrNoHold = errors.New("no hold is set")

// ConfirmHoldAbove is where a hold stops being a convenience and starts being
// something to say out loud twice. Forty cents is cheaper than a managed server
// and 25× a normal session, which is exactly the sort of thing a typo should not
// be able to buy. Both the CLI and the in-game command measure against it, so it
// lives here rather than in either of them.
const ConfirmHoldAbove = 12 * time.Hour

// HoldResult is a hold that took effect, with the numbers to quote back.
type HoldResult struct {
	Until   time.Time
	Length  time.Duration
	CostEUR float64
	Rate    float64
}

// Hold keeps the server up with nobody on it until the given duration has
// passed.
//
// This is the one deliberate exception to the premise the whole project rests
// on — a server that stops existing when nobody plays — so it is bounded,
// priced, and recorded. Three rules, in order:
//
//   - it cannot ask for more than server.max_hold (a typo must not become five
//     euros a month);
//   - it only applies to a server that is already up (renting one is `start`,
//     not `hold`);
//   - it runs on the daemon's clock, not the VPS's, so a hold survives a restart
//     of either.
//
// The write takes the state lock the same way Stop does: Tick writes back the
// whole row it read at the start of its pass, so a hold set mid-pass would
// otherwise be overwritten twenty seconds later (PLAN.md, decision 20).
func (o *Orchestrator) Hold(ctx context.Context, d time.Duration, who string) (HoldResult, error) {
	if d <= 0 {
		return HoldResult{}, fmt.Errorf("hold needs a positive duration, got %s", d)
	}
	max := o.HoldMax()
	if d > max {
		return HoldResult{}, fmt.Errorf("%w (server.max_hold is %s, asked for %s)", ErrHoldTooLong, ShortDuration(max), ShortDuration(d))
	}
	if d < time.Minute {
		return HoldResult{}, fmt.Errorf("hold needs to be at least a minute; tick interval is %s", ShortDuration(o.cfg.Server.TickInterval.Std()))
	}

	unlock, err := o.lockForWrite(ctx)
	if err != nil {
		return HoldResult{}, err
	}
	defer unlock()

	st, err := o.store.GetState()
	if err != nil {
		return HoldResult{}, err
	}
	if st.State != store.Running && st.State != store.Idle {
		return HoldResult{}, fmt.Errorf("%w (state is %s)", ErrNotRunning, st.State)
	}

	until := o.now().Add(d)
	next := st
	next.HoldUntil = until
	// A hold that starts while the server is already IDLE puts it back to
	// RUNNING: "idle" means "counting down to shutdown", and it is not.
	next.State = store.Running
	if err := o.store.SetState(next); err != nil {
		return HoldResult{}, err
	}

	res := HoldResult{Until: until, Length: d, Rate: o.rateFor(st.Region)}
	res.CostEUR = res.Rate * d.Hours()

	o.log.Info("hold set", "until", until, "length", d, "by", who, "cost_eur", res.CostEUR)
	_ = o.store.Log("hold", "%s with nobody on, until %s, about %.2f EUR (by %s)",
		ShortDuration(d), until.Format("2006-01-02 15:04 UTC"), res.CostEUR, who)
	return res, nil
}

// CancelHold drops a hold, and starts the idle clock from zero rather than
// resuming whatever it was doing before: the time spent held was not time spent
// waiting for the server to die.
func (o *Orchestrator) CancelHold(ctx context.Context, who string) (time.Time, error) {
	unlock, err := o.lockForWrite(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer unlock()

	st, err := o.store.GetState()
	if err != nil {
		return time.Time{}, err
	}
	if st.HoldUntil.IsZero() {
		return time.Time{}, ErrNoHold
	}

	was := st.HoldUntil
	next := st
	next.HoldUntil = time.Time{}
	if st.Players == 0 {
		next.IdleSince = o.now()
	}
	if err := o.store.SetState(next); err != nil {
		return time.Time{}, err
	}

	o.log.Info("hold cancelled", "was", was, "by", who)
	_ = o.store.Log("hold-cancelled", "hold until %s dropped (by %s)", was.Format(time.RFC3339), who)
	return was, nil
}

// HoldMax is the configured ceiling on a hold, defaulting to 72h for a config
// built without defaults (tests, mostly).
func (o *Orchestrator) HoldMax() time.Duration {
	if d := o.cfg.Server.MaxHold.Std(); d > 0 {
		return d
	}
	return 72 * time.Hour
}

// Rate is the hourly rate for whatever region the current server is in. With
// nothing rented it answers for the default region, so "the rate here" is a real
// number before a server exists rather than EUR 0/h.
func (o *Orchestrator) Rate() float64 {
	region := o.cfg.DefaultRegion()
	if st, err := o.store.GetState(); err == nil && st.Region != "" {
		region = st.Region
	}
	return o.rateFor(region)
}

// rateFor is the configured hourly rate for a region, or zero for a region the
// config no longer knows.
func (o *Orchestrator) rateFor(region string) float64 {
	if reg, ok := o.cfg.Regions[region]; ok {
		return reg.HourlyRate
	}
	return 0
}

// holdActive reports whether a hold is currently in force, and how long is left.
func (o *Orchestrator) holdActive(st store.ServerState) (bool, time.Duration) {
	if st.HoldUntil.IsZero() {
		return false, 0
	}
	left := st.HoldUntil.Sub(o.now())
	if left <= 0 {
		return false, 0
	}
	return true, left
}

// HoldState is where a hold stands right now: where it ends, how long is left,
// and the ceiling it was measured against.
type HoldState struct {
	Active bool
	Until  time.Time
	Left   time.Duration
	Max    time.Duration
}

// HoldState reads the current hold. A hold that has run out but not yet been
// cleared by a tick reads as inactive, which is what every caller wants: the
// clock is the daemon's, not the VPS's.
func (o *Orchestrator) HoldState() (HoldState, error) {
	st, err := o.store.GetState()
	if err != nil {
		return HoldState{}, err
	}
	h := HoldState{Max: o.HoldMax()}
	if held, left := o.holdActive(st); held {
		h.Active = true
		h.Until = st.HoldUntil
		h.Left = left
	}
	return h, nil
}

// ShortDuration renders a length the way `/outpost hold` accepts it back: 48h,
// 2h30m, 90m. time.Duration's own "48h0m0s" reads like a log line in a chat box.
func ShortDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	d = d.Round(time.Minute)
	h := int64(d / time.Hour)
	m := int64((d % time.Hour) / time.Minute)
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// MonthStart is the first instant of the month containing t, in UTC.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Cost is the money readout: this session, this calendar month, and the rate
// the projection is drawn from.
type Cost struct {
	SessionHours  float64
	SessionCost   float64
	MonthHours    float64
	MonthCost     float64
	MonthSessions int
	HourlyRate    float64
	MonthStart    time.Time
	Live          bool
}

// Cost totals the month from the ledger plus the session that is running now.
//
// The month is the calendar month in UTC, because that is the timezone
// everything else here is stamped in and a cost that shifts with the reader's
// clock is worse than one that shifts with nobody's.
func (o *Orchestrator) Cost(ctx context.Context) (Cost, error) {
	st, err := o.store.GetState()
	if err != nil {
		return Cost{}, err
	}
	month := MonthStart(o.now())
	out := Cost{HourlyRate: o.rateFor(st.Region), MonthStart: month}

	hours, cost, sessions, err := o.store.TotalsSince(month)
	if err != nil {
		return Cost{}, err
	}
	out.MonthHours, out.MonthCost, out.MonthSessions = hours, cost, sessions

	// The live session is not in the ledger yet, so add it on top. A session
	// that straddles a month boundary counts in the month it started; at forty
	// cents a session the proration rule would cost more code than the error is
	// worth.
	if !st.StartedAt.IsZero() && (st.State == store.Running || st.State == store.Idle) {
		out.Live = true
		out.SessionHours = o.now().Sub(st.StartedAt).Hours()
		if out.SessionHours < 0 {
			out.SessionHours = 0
		}
		out.SessionCost = out.HourlyRate * out.SessionHours
		if !st.StartedAt.Before(month) {
			out.MonthHours += out.SessionHours
			out.MonthCost += out.SessionCost
			out.MonthSessions++
		}
	}
	return out, nil
}
