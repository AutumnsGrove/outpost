package api

import (
	"context"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

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
		out = append(out, backupDTO{At: stamp(b.At), SizeBytes: b.SizeBytes, Kind: b.Kind})
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
	events, err := s.store.RecentEvents(40)
	if err != nil {
		s.fail(w, err)
		return
	}
	sessions, err := s.store.RecentSessions(8)
	if err != nil {
		s.fail(w, err)
		return
	}
	backups, err := s.store.RecentBackups(5)
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
