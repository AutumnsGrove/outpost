package api

import (
	"context"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"outpost/internal/hetzner"
	"outpost/internal/orchestrator"
	"outpost/internal/roster"
	"outpost/internal/store"
)

// The DTOs exist because store's types carry no JSON tags: encoded directly they
// came out as {"At": ..., "Kind": ...}, which is not what the page reads, and
// the page's "Recent" list rendered blank rows. Naming the wire shape here keeps
// the database's field names out of the contract.

type eventDTO struct {
	ID      int64  `json:"id"`
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type sessionDTO struct {
	ID              int64  `json:"id"`
	StartedAt       string `json:"startedAt"`
	EndedAt         string `json:"endedAt"`
	DurationSeconds int64  `json:"durationSeconds"`
	// Cost is in euros: the only currency Hetzner bills in. The column is still
	// called cost_usd from before that was noticed.
	Cost       float64 `json:"cost"`
	MaxPlayers int     `json:"maxPlayers"`
	Region     string  `json:"region"`
	ServerType string  `json:"serverType"`
}

type backupDTO struct {
	ID        int64  `json:"id"`
	At        string `json:"at"`
	SizeBytes int64  `json:"sizeBytes"`
	Kind      string `json:"kind"`
}

type totalsDTO struct {
	Hours    float64 `json:"hours"`
	Cost     float64 `json:"cost"`
	Sessions int     `json:"sessions"`
}

type buildDTO struct {
	Commit   string `json:"commit"`
	Built    string `json:"built"`
	Modified bool   `json:"modified"`
}

// dnsDTO answers "can people find the server?". State is one of:
//
//	ok       the record points at the live server
//	pending  a server is up and the record has not caught up (retried every pass)
//	idle     no server; the record still points at the last one, which is intended
//	unset    nothing has ever been published
type dnsDTO struct {
	Name     string `json:"name"`
	Points   string `json:"pointsAt"`
	ServerIP string `json:"serverIp"`
	State    string `json:"state"`
}

type playersDTO struct {
	Online []string `json:"online"`
	// Roster is whether the whitelist could be read at all. When false the page
	// hides the controls rather than offering buttons that cannot work.
	Roster    bool     `json:"roster"`
	Whitelist []string `json:"whitelist"`
	Ops       []string `json:"ops"`
	// RosterError is set when R2 answered badly, so "nobody is whitelisted" is
	// never shown for "could not read the whitelist".
	RosterError string `json:"rosterError,omitempty"`
}

// planDTO is one rentable size, with the price the page shows beside it.
type planDTO struct {
	Type  string `json:"type"`
	VCPU  int    `json:"vcpu"`
	RAMGB int    `json:"ramGb"`
	// HourlyRate is EUR per hour, list price including the IPv4 a server needs.
	HourlyRate float64 `json:"hourlyRate"`
	ARM        bool    `json:"arm"`
	// Fits is false when the active pack asks for more RAM than this plan has:
	// the page greys it out with the reason rather than letting Start refuse it.
	Fits bool `json:"fits"`
	// Known is false for a configured plan this build's table does not price.
	Known bool `json:"known"`
}

type overviewResponse struct {
	Status   statusResponse      `json:"status"`
	Health   orchestrator.Health `json:"health"`
	Events   []eventDTO          `json:"events"`
	Sessions []sessionDTO        `json:"sessions"`
	Totals   totalsDTO           `json:"totals"`
	Backups  []backupDTO         `json:"backups"`
	DNS      dnsDTO              `json:"dns"`
	Players  playersDTO          `json:"players"`
	Build    buildDTO            `json:"build"`
	// CanUpdate is whether this binary sits in a git checkout and the page may
	// offer the Update button at all.
	CanUpdate bool `json:"canUpdate"`
	// Plans is what Start can rent, smallest first, and SelectedType is the one
	// the picker opens on.
	Plans        []planDTO `json:"plans"`
	SelectedType string    `json:"selectedType"`
	// PackMemoryGB is what the active pack asks for, so the page can say why a
	// plan is greyed out.
	PackMemoryGB int `json:"packMemoryGb"`
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func eventDTOs(in []store.Event) []eventDTO {
	out := make([]eventDTO, 0, len(in))
	for _, e := range in {
		out = append(out, eventDTO{ID: e.ID, At: stamp(e.At), Kind: e.Kind, Message: e.Message})
	}
	return out
}

func sessionDTOs(in []store.Session) []sessionDTO {
	out := make([]sessionDTO, 0, len(in))
	for _, s := range in {
		out = append(out, sessionDTO{
			ID: s.ID, StartedAt: stamp(s.StartedAt), EndedAt: stamp(s.EndedAt),
			DurationSeconds: s.DurationSeconds, Cost: s.CostUSD, MaxPlayers: s.MaxPlayers,
			Region: s.Region, ServerType: s.ServerType,
		})
	}
	return out
}

func backupDTOs(in []store.Backup) []backupDTO {
	out := make([]backupDTO, 0, len(in))
	for _, b := range in {
		out = append(out, backupDTO{ID: b.ID, At: stamp(b.At), SizeBytes: b.SizeBytes, Kind: b.Kind})
	}
	return out
}

// buildInfo reads the commit the binary was built from out of the Go toolchain's
// own stamp, which `go build` inside a git checkout adds for free — and the
// Potato builds inside one. "Did my update land?" then needs no ldflags.
func buildInfo() buildDTO {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildDTO{Commit: "unknown"}
	}
	out := buildDTO{Commit: "dev"}
	for _, kv := range info.Settings {
		switch kv.Key {
		case "vcs.revision":
			if len(kv.Value) > 7 {
				out.Commit = kv.Value[:7]
			} else if kv.Value != "" {
				out.Commit = kv.Value
			}
		case "vcs.time":
			out.Built = kv.Value
		case "vcs.modified":
			out.Modified = kv.Value == "true"
		}
	}
	return out
}

// rosterCache keeps the page's 5-second poll from becoming a 5-second R2 read.
// Writes invalidate it, so a change shows on the very next refresh.
type rosterCache struct {
	mu   sync.Mutex
	at   time.Time
	snap roster.Snapshot
	err  error
}

const rosterTTL = 30 * time.Second

func (c *rosterCache) invalidate() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

func (c *rosterCache) get(ctx context.Context, r *roster.Roster) (roster.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < rosterTTL {
		return c.snap, c.err
	}
	c.snap, c.err = r.List(ctx)
	c.at = time.Now()
	return c.snap, c.err
}

// How much of each list the overview carries. These are sized to what the page
// draws rather than to what the database has: seven events, five finished
// sessions, one current backup and three older ones. The multiple is slack for a
// shutdown in progress (the hero reads the event log to tell whether the save
// landed) so a poll every five seconds stays a small, bounded response instead of
// a history dump — it is a phone on a tailnet that may be relayed, not a dashboard
// on a LAN.
const (
	overviewEvents   = 20
	overviewSessions = 6
	overviewBackups  = 4
)

// overview is everything the page shows, in one request.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status, err := s.orch.Status(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	state, err := s.store.GetState()
	if err != nil {
		s.fail(w, err)
		return
	}
	events, err := s.store.RecentEvents(overviewEvents)
	if err != nil {
		s.fail(w, err)
		return
	}
	sessions, err := s.store.RecentSessions(overviewSessions)
	if err != nil {
		s.fail(w, err)
		return
	}
	backups, err := s.store.RecentBackups(overviewBackups)
	if err != nil {
		s.fail(w, err)
		return
	}
	hours, cost, count, err := s.store.Totals()
	if err != nil {
		s.fail(w, err)
		return
	}

	health := s.orch.Health()
	out := overviewResponse{
		Status:    statusResponse{Status: status, FormToken: s.formToken},
		Health:    health,
		Events:    eventDTOs(events),
		Sessions:  sessionDTOs(sessions),
		Totals:    totalsDTO{Hours: hours, Cost: cost, Sessions: count},
		Backups:   backupDTOs(backups),
		DNS:       dnsState(status.DNSName, state, health.LastDNS),
		Players:   s.playersState(ctx),
		Build:     buildInfo(),
		CanUpdate: s.update != nil,
	}
	out.Plans, out.SelectedType, out.PackMemoryGB = planChoices(ctx, s.orch)
	writeJSON(w, http.StatusOK, out)
}

func dnsState(name string, st store.ServerState, last string) dnsDTO {
	d := dnsDTO{Name: name, ServerIP: st.VPSIP, Points: last}
	if st.DNSValue != "" {
		d.Points = st.DNSValue
	}
	live := st.State == store.Running || st.State == store.Idle
	switch {
	case live && st.DNSValue != "" && st.DNSValue == st.VPSIP:
		d.State = "ok"
	case live:
		d.State = "pending"
	case d.Points != "":
		d.State = "idle"
	default:
		d.State = "unset"
	}
	return d
}

func (s *Server) playersState(ctx context.Context) playersDTO {
	p := playersDTO{Online: s.orch.OnlineNames(), Whitelist: []string{}, Ops: []string{}}
	if p.Online == nil {
		p.Online = []string{}
	}
	if s.roster == nil {
		return p
	}
	snap, err := s.cache.get(ctx, s.roster)
	if err != nil {
		p.RosterError = err.Error()
		return p
	}
	p.Roster = true
	p.Whitelist, p.Ops = snap.Whitelist, snap.Ops
	return p
}

// planChoices answers the picker: every plan this build can price, plus the
// selected one when the table does not know it, each marked with whether the
// active pack fits.
//
// The selected plan falls back to the cheapest one that holds the pack, because a
// picker that opens on a plan Start would refuse is a trap: someone presses the
// big button, and the refusal reads like a bug rather than like a choice.
func planChoices(ctx context.Context, orch *orchestrator.Orchestrator) ([]planDTO, string, int) {
	want, _ := orch.PackMemoryGB(ctx)
	fits := func(gb int) bool { return want == 0 || gb >= want }

	var out []planDTO
	for _, p := range hetzner.Plans() {
		out = append(out, planDTO{
			Type: p.Type, VCPU: p.VCPU, RAMGB: p.RAMGB, HourlyRate: p.HourlyEUR,
			ARM: p.ARM, Fits: fits(p.RAMGB), Known: true,
		})
	}

	selected := orch.SelectedServerType()
	if sel, known := hetzner.Lookup(selected); !known {
		// A configured plan the table cannot price is still rentable. Offering it
		// with the price left out beats making the picker disagree with config.
		out = append(out, planDTO{Type: selected, Fits: true})
	} else if !fits(sel.RAMGB) {
		if best, ok := hetzner.SmallestFit(want); ok {
			selected = best.Type
		}
	}
	return out, selected, want
}
