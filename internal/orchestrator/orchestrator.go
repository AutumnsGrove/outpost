// Package orchestrator owns Outpost's state machine.
//
// One loop, one clock, one opinion. Every state change happens inside Tick, and
// Tick is written so that running it twice in a row is harmless — a crashed
// process, a lost network call, or a human hitting Ctrl-C must never leave a
// server running with nobody watching it.
//
// The dependencies are interfaces so the state machine can be tested without
// renting anything from Hetzner.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"outpost/internal/cloudinit"
	"outpost/internal/config"
	"outpost/internal/dns"
	"outpost/internal/hetzner"
	"outpost/internal/rcon"
	"outpost/internal/remote"
	"outpost/internal/store"
	"outpost/internal/tailnet"
)

// rconPort is fixed by server.properties.
const rconPort = 25575

// maxRCONFailures is the fallback for a config that does not set
// server.rcon_failure_limit. At the default 20s tick this is five minutes of a
// wedged server before it is taken down and its world saved.
const maxRCONFailures = 15

// maxWorldSaveAttempts is how many times the shutdown path will retry the world
// upload before giving up. Giving up leaves the VPS alive on purpose: a running
// server you pay for is better than a deleted world.
const maxWorldSaveAttempts = 3

// Servers is the Hetzner surface Outpost needs.
type Servers interface {
	Create(ctx context.Context, spec hetzner.CreateRequest) (*hetzner.Server, error)
	Get(ctx context.Context, id string) (*hetzner.Server, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]hetzner.Server, error)
}

// Records is the DNS surface Outpost needs.
type Records interface {
	SetA(ctx context.Context, ip string) error
}

// Remote is the SSH surface Outpost needs.
type Remote interface {
	Run(ctx context.Context, host, cmd string) (string, error)
}

// Probe asks a running server who is online.
type Probe func(ctx context.Context, addr, password string) (rcon.Players, error)

// Deps is everything the state machine touches. Production fills it with real
// clients; tests fill it with fakes.
type Deps struct {
	Servers Servers
	DNS     Records
	Remote  Remote
	Probe   Probe
	Now     func() time.Time

	// Resolve turns the VPS's tailnet name into an address without DNS. See
	// internal/tailnet for why that matters.
	Resolve func(ctx context.Context, host string) (string, error)
}

// Orchestrator drives the lifecycle.
type Orchestrator struct {
	cfg   *config.Config
	store *store.Store
	deps  Deps
	log   *slog.Logger
}

// New wires an Orchestrator to the real world.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Orchestrator {
	return &Orchestrator{
		cfg:   cfg,
		store: st,
		log:   log,
		deps: Deps{
			Servers: hetzner.New(cfg.Secrets.HetznerToken),
			DNS:     dns.New(cfg.Secrets.CloudflareToken, cfg.Cloudflare.ZoneID, cfg.Cloudflare.RecordName),
			Remote:  remote.New(cfg.Hetzner.SSHUser, cfg.Hetzner.SSHPrivateKey),
			Probe:   probe,
			Now:     func() time.Time { return time.Now().UTC() },
			Resolve: tailnet.Resolve,
		},
	}
}

// NewWith wires an Orchestrator to supplied dependencies. Any nil field falls
// back to the production default.
func NewWith(cfg *config.Config, st *store.Store, log *slog.Logger, deps Deps) *Orchestrator {
	o := New(cfg, st, log)
	if deps.Servers != nil {
		o.deps.Servers = deps.Servers
	}
	if deps.DNS != nil {
		o.deps.DNS = deps.DNS
	}
	if deps.Remote != nil {
		o.deps.Remote = deps.Remote
	}
	if deps.Probe != nil {
		o.deps.Probe = deps.Probe
	}
	if deps.Now != nil {
		o.deps.Now = deps.Now
	}
	if deps.Resolve != nil {
		o.deps.Resolve = deps.Resolve
	}
	return o
}

func (o *Orchestrator) now() time.Time { return o.deps.Now() }

// dialHost is the address to actually connect to.
//
// MagicDNS is one cached NXDOMAIN away from lying, and the orchestrator reads
// that as "the server never came up". Ask the local tailnet first; fall back to
// the name so a host without the tailscale CLI still works.
func (o *Orchestrator) dialHost(ctx context.Context, st store.ServerState) string {
	if o.deps.Resolve == nil {
		return st.TailnetHost
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ip, err := o.deps.Resolve(ctx, st.TailnetHost); err == nil && ip != "" {
		return ip
	}
	return st.TailnetHost
}

// DefaultRegion is the region `outpost start` uses when told nothing else. The
// HTTP API asks this rather than hard-coding one, so a config without "eu"
// does not try to rent in a region nobody configured.
func (o *Orchestrator) DefaultRegion() string { return o.cfg.DefaultRegion() }

// rconFailureLimit tolerates a config built without defaults (tests, mostly).
func (o *Orchestrator) rconFailureLimit() int {
	if n := o.cfg.Server.RCONFailureLimit; n > 0 {
		return n
	}
	return maxRCONFailures
}

// Status is the snapshot the API and the CLI both render.
type Status struct {
	State        string  `json:"state"`
	Region       string  `json:"region"`
	ServerType   string  `json:"serverType"`
	ServerID     string  `json:"serverId"`
	ServerIP     string  `json:"serverIp"`
	Hostname     string  `json:"hostname"`
	Players      int     `json:"players"`
	UptimeSec    int64   `json:"uptimeSeconds"`
	IdleSec      int64   `json:"idleSeconds"`
	TTLSec       int64   `json:"ttlSeconds"`
	StartedAt    string  `json:"startedAt"`
	HourlyRate   float64 `json:"hourlyRate"`
	SessionCost  float64 `json:"sessionCost"`
	TotalHours   float64 `json:"totalHours"`
	TotalCost    float64 `json:"totalCost"`
	LastError    string  `json:"lastError"`
	DNSName      string  `json:"dnsName"`
	IdleGraceSec int64   `json:"idleGraceSeconds"`
	IdleLimitSec int64   `json:"idleLimitSeconds"`
}

// Status reads the current state and decorates it with costs and timers.
//
// Money is euros, because that is the only currency Hetzner bills in. The
// numbers come from config's hourly_rate, so they are an estimate for the
// readout and never the bill.
func (o *Orchestrator) Status(ctx context.Context) (Status, error) {
	st, err := o.store.GetState()
	if err != nil {
		return Status{}, err
	}

	out := Status{
		State:        string(st.State),
		Region:       st.Region,
		ServerType:   st.ServerType,
		ServerID:     st.VPSID,
		ServerIP:     st.VPSIP,
		Hostname:     st.TailnetHost,
		Players:      st.Players,
		LastError:    st.LastError,
		DNSName:      o.cfg.Cloudflare.RecordName,
		IdleGraceSec: int64(o.cfg.Server.IdleGrace.Std().Seconds()),
		IdleLimitSec: int64(o.cfg.Server.IdleTimeout.Std().Seconds()),
	}

	if !st.StartedAt.IsZero() {
		out.StartedAt = st.StartedAt.Format(time.RFC3339)
		out.UptimeSec = int64(o.now().Sub(st.StartedAt).Seconds())
	}
	if reg, ok := o.cfg.Regions[st.Region]; ok {
		out.HourlyRate = reg.HourlyRate
		out.SessionCost = reg.HourlyRate * float64(out.UptimeSec) / 3600
	}
	if !st.IdleSince.IsZero() {
		out.IdleSec = int64(o.now().Sub(st.IdleSince).Seconds())
		limit := int64(o.cfg.Server.IdleGrace.Std().Seconds() + o.cfg.Server.IdleTimeout.Std().Seconds())
		if remaining := limit - out.IdleSec; remaining > 0 {
			out.TTLSec = remaining
		}
	}

	if hours, cost, _, err := o.store.Totals(); err == nil {
		out.TotalHours = hours
		out.TotalCost = cost
	}
	return out, nil
}

// Start rents a VPS. Provisioning itself is Tick's job, so a crash between "we
// asked Hetzner" and "Hetzner answered" cannot lose the server: the vps_id is
// committed as soon as it exists.
func (o *Orchestrator) Start(ctx context.Context, region string) (store.ServerState, error) {
	// Renting is the one action that costs money the instant it succeeds, so it
	// is serialised across processes before anything else is read: `outpost
	// serve` and an `outpost start` shell must not both pass the state check.
	release, err := o.store.LockStart()
	if err != nil {
		return store.ServerState{}, err
	}
	defer release()

	st, err := o.store.GetState()
	if err != nil {
		return st, err
	}
	switch {
	case st.State == store.Error && st.VPSID != "":
		// ERROR is where a failed world save leaves a live, billing server.
		// Renting a second one would orphan the first and its unsaved world.
		return st, fmt.Errorf("state is ERROR with server %s still running; run `outpost reset` (or `outpost stop`) first", st.VPSID)
	case st.State != store.Offline && st.State != store.Error:
		return st, fmt.Errorf("server is %s; stop it first", st.State)
	}

	// Ask Hetzner what exists, not just what SQLite remembers. A restored or
	// lost outpost.db must not turn into two rented servers.
	stray, err := o.deps.Servers.List(ctx)
	if err != nil {
		return st, fmt.Errorf("check for existing servers: %w", err)
	}
	if len(stray) > 0 {
		return st, fmt.Errorf("%d outpost server(s) already exist in Hetzner (%s); run `outpost reset` first", len(stray), stray[0].Name)
	}

	reg, err := o.cfg.Region(region)
	if err != nil {
		return st, err
	}

	password, err := randomHex(24)
	if err != nil {
		return st, err
	}
	suffix, err := randomHex(3)
	if err != nil {
		return st, err
	}
	hostname := strings.ToLower(fmt.Sprintf("%s-%s-%s", o.cfg.Hetzner.ServerNameBase, region, suffix))

	userData, err := cloudinit.Render(cloudinit.Data{
		Hostname:         hostname,
		MCPort:           o.cfg.Server.MCPort,
		RCONPassword:     password,
		TailscaleAuthKey: o.cfg.Secrets.TailscaleAuthKey,
		TailscaleTags:    o.cfg.Tailscale.Tags,
		R2AccountID:      o.cfg.R2.AccountID,
		R2AccessKey:      o.cfg.Secrets.R2AccessKey,
		R2SecretKey:      o.cfg.Secrets.R2SecretKey,
		AssetBucket:      o.cfg.Server.AssetBucket,
		WorldBucket:      o.cfg.Server.WorldBucket,
	})
	if err != nil {
		return st, err
	}

	o.log.Info("renting server", "region", region, "type", reg.ServerType, "hostname", hostname)

	srv, serverType, err := o.rent(ctx, hostname, reg, userData)
	if err != nil {
		return st, err
	}

	st = store.ServerState{
		State:        store.Provisioning,
		VPSID:        fmt.Sprint(srv.ID),
		VPSIP:        srv.PublicNet.IPv4.IP,
		TailnetHost:  hostname,
		Region:       region,
		ServerType:   serverType,
		RCONPassword: password,
		StartedAt:    o.now(),
	}
	if err := o.store.SetState(st); err != nil {
		// The server exists and we cannot record it. Say so loudly: this is the
		// one case where a human has to go and delete something by hand.
		return st, fmt.Errorf("server %d created but state write failed: %w", srv.ID, err)
	}
	_ = o.store.MetaSet("world_saved", "")
	_ = o.store.MetaSet("save_attempts", "0")
	_ = o.store.MetaSet("rcon_failures", "0")
	_ = o.store.MetaSet("ever_running", "")

	if _, err := o.store.StartSession(st.StartedAt, region, serverType, fmt.Sprint(srv.ID)); err != nil {
		return st, err
	}
	_ = o.store.Log("start", "rented %s in %s (%s)", hostname, reg.Location, serverType)

	return st, nil
}

// rent creates the server, walking through the configured fallback sizes when
// Hetzner has no capacity for the preferred one. Thin stock is the normal state
// of the world Outpost lives in, not an exceptional condition.
func (o *Orchestrator) rent(ctx context.Context, hostname string, reg config.Region, userData string) (*hetzner.Server, string, error) {
	types := append([]string{reg.ServerType}, reg.FallbackTypes...)

	var lastErr error
	for _, serverType := range types {
		if serverType == "" {
			continue
		}
		srv, err := o.deps.Servers.Create(ctx, hetzner.CreateRequest{
			Name:       hostname,
			ServerType: serverType,
			Location:   reg.Location,
			Image:      o.cfg.Hetzner.Image,
			SSHKeyID:   o.cfg.Hetzner.SSHKeyID,
			SnapshotID: o.cfg.Hetzner.SnapshotID,
			FirewallID: o.cfg.Hetzner.FirewallID,
			UserData:   userData,
		})
		if err == nil {
			if serverType != reg.ServerType {
				o.log.Info("fell back to another size", "type", serverType, "wanted", reg.ServerType)
				_ = o.store.Log("fallback", "%s has no capacity; rented %s instead", reg.ServerType, serverType)
			}
			_ = o.store.MetaSet("rented_type", serverType)
			return srv, serverType, nil
		}
		if !errors.Is(err, hetzner.ErrUnavailable) {
			return nil, "", err
		}
		lastErr = err
		o.log.Warn("no capacity", "type", serverType, "location", reg.Location)
		_ = o.store.Log("no-capacity", "%s in %s: %v", serverType, reg.Location, err)
	}

	return nil, "", fmt.Errorf("no capacity in %s for any of %v: %w", reg.Location, types, lastErr)
}

// Stop asks for a graceful shutdown. Tick does the work.
func (o *Orchestrator) Stop(ctx context.Context) error {
	st, err := o.store.GetState()
	if err != nil {
		return err
	}
	switch st.State {
	case store.Offline:
		return fmt.Errorf("server is already offline")
	case store.Terminating:
		return nil
	}
	_ = o.store.Log("stop", "manual shutdown requested")
	return o.transition(st, store.Terminating)
}

// Tick advances the machine by one step. Safe to call from any state, as often
// as you like.
func (o *Orchestrator) Tick(ctx context.Context) error {
	st, err := o.store.GetState()
	if err != nil {
		return err
	}

	switch st.State {
	case store.Offline, store.Error:
		return nil
	case store.Provisioning:
		return o.tickProvisioning(ctx, st)
	case store.Running, store.Idle:
		return o.tickLive(ctx, st)
	case store.Terminating:
		return o.tickTerminating(ctx, st)
	default:
		return fmt.Errorf("unknown state %q", st.State)
	}
}

// Run ticks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) {
	ticker := time.NewTicker(o.cfg.Server.TickInterval.Std())
	defer ticker.Stop()

	for {
		if err := o.Tick(ctx); err != nil {
			o.log.Error("tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *Orchestrator) tickProvisioning(ctx context.Context, st store.ServerState) error {
	srv, err := o.deps.Servers.Get(ctx, st.VPSID)
	if errors.Is(err, hetzner.ErrNotFound) {
		o.log.Warn("vps disappeared during provisioning", "id", st.VPSID)
		return o.finalize(st, "vps vanished during provisioning")
	}
	if err != nil {
		return err
	}

	if elapsed := o.now().Sub(st.StartedAt); elapsed > o.cfg.Server.ProvisionTimeout.Std() {
		o.log.Error("provisioning timed out", "id", st.VPSID, "elapsed", elapsed)
		st.LastError = fmt.Sprintf("provisioning timed out after %s", elapsed.Round(time.Second))
		_ = o.store.SetState(st)
		_ = o.store.Log("provision-timeout", "%s", st.LastError)
		return o.transition(st, store.Terminating)
	}

	// RCON answering is the only proof that the server is usable: systemd can
	// say "active" while Fabric is still loading.
	host := o.dialHost(ctx, st)
	addr := net.JoinHostPort(host, fmt.Sprint(rconPort))
	players, err := o.deps.Probe(ctx, addr, st.RCONPassword)
	if err != nil {
		o.log.Debug("not ready yet", "host", st.TailnetHost, "err", err)
		return nil
	}

	next := st
	next.State = store.Running
	next.Players = players.Online
	next.VPSIP = srv.PublicNet.IPv4.IP
	next.LastError = ""
	// Record that this VPS actually served, so the shutdown path can tell a
	// server with a world from one that never got past its own boot.
	_ = o.store.MetaSet("ever_running", st.VPSID)
	// Record a server that came up with nobody on it, so the idle clock starts
	// now rather than one tick later.
	next.IdleSince = time.Time{}
	if players.Online == 0 {
		next.IdleSince = o.now()
	}
	if err := o.store.SetState(next); err != nil {
		return err
	}

	if err := o.deps.DNS.SetA(ctx, next.VPSIP); err != nil {
		// Not fatal: the server is up, the IP can be handed out by mouth.
		o.log.Error("dns update failed", "err", err)
		_ = o.store.Log("dns-error", "%v", err)
	} else {
		next.DNSValue = next.VPSIP
		_ = o.store.SetState(next)
	}

	o.log.Info("server ready", "host", st.TailnetHost, "ip", next.VPSIP)
	_ = o.store.Log("ready", "%s is up at %s", st.TailnetHost, next.VPSIP)
	return nil
}

func (o *Orchestrator) tickLive(ctx context.Context, st store.ServerState) error {
	srv, err := o.deps.Servers.Get(ctx, st.VPSID)
	if errors.Is(err, hetzner.ErrNotFound) {
		o.log.Warn("vps vanished while live", "id", st.VPSID)
		return o.finalize(st, "vps vanished while live")
	}
	if err != nil {
		return err
	}
	if srv.Status == "off" || srv.Status == "deleting" {
		o.log.Warn("vps is not running", "id", st.VPSID, "status", srv.Status)
		return o.transition(st, store.Terminating)
	}

	addr := net.JoinHostPort(o.dialHost(ctx, st), fmt.Sprint(rconPort))
	players, probeErr := o.deps.Probe(ctx, addr, st.RCONPassword)

	if probeErr != nil {
		failures, _ := o.store.MetaInt("rcon_failures")
		failures++
		_ = o.store.MetaSet("rcon_failures", fmt.Sprint(failures))
		o.log.Warn("rcon probe failed", "host", st.TailnetHost, "attempt", failures, "err", probeErr)

		if failures >= o.rconFailureLimit() {
			_ = o.store.MetaSet("rcon_failures", "0")
			_ = o.store.Log("rcon-dead", "%d consecutive failures, shutting down", failures)
			st.LastError = fmt.Sprintf("rcon unreachable: %v", probeErr)
			_ = o.store.SetState(st)
			return o.transition(st, store.Terminating)
		}
		return nil
	}
	_ = o.store.MetaSet("rcon_failures", "0")

	next := st
	next.Players = players.Online
	next.LastError = ""

	if sess, err := o.store.CurrentSession(); err == nil && sess != nil {
		_ = o.store.UpdateSessionPlayers(sess.ID, players.Online)
	}

	grace := o.cfg.Server.IdleGrace.Std()
	limit := grace + o.cfg.Server.IdleTimeout.Std()

	if players.Online > 0 {
		if st.State == store.Idle {
			o.log.Info("players returned", "players", players.Online)
			_ = o.store.Log("resume", "%d player(s) joined, back to running", players.Online)
		}
		next.State = store.Running
		next.IdleSince = time.Time{}
		return o.store.SetState(next)
	}

	// Nobody on. Start the clock, or read it.
	if st.IdleSince.IsZero() {
		next.IdleSince = o.now()
		next.State = store.Running
		o.log.Info("server is empty, idle clock started")
		_ = o.store.Log("empty", "no players, idle clock started")
		return o.store.SetState(next)
	}

	idleFor := o.now().Sub(st.IdleSince)
	switch {
	case idleFor >= limit:
		o.log.Info("idle limit reached", "idle", idleFor.Round(time.Second))
		_ = o.store.Log("idle-limit", "empty for %s, terminating", idleFor.Round(time.Second))
		_ = o.store.SetState(next)
		return o.transition(next, store.Terminating)
	case idleFor >= grace:
		if st.State != store.Idle {
			o.log.Info("server idle", "idle", idleFor.Round(time.Second))
			_ = o.store.Log("idle", "empty for %s", idleFor.Round(time.Second))
		}
		next.State = store.Idle
	}
	return o.store.SetState(next)
}

func (o *Orchestrator) tickTerminating(ctx context.Context, st store.ServerState) error {
	if st.VPSID == "" {
		return o.finalize(st, "nothing to terminate")
	}

	if _, err := o.deps.Servers.Get(ctx, st.VPSID); errors.Is(err, hetzner.ErrNotFound) {
		return o.finalize(st, "vps already gone")
	} else if err != nil {
		return err
	}

	saved, err := o.store.MetaGet("world_saved")
	if err != nil {
		return err
	}

	if saved != st.VPSID {
		skip, err := o.nothingToSave(ctx, st)
		if err != nil {
			return err
		}
		if !skip {
			done, err := o.archiveWorld(ctx, st)
			if err != nil {
				return err
			}
			if !done {
				// The world is not in R2 yet. Stop here rather than deleting the
				// server: the next tick retries the save.
				return nil
			}
		}
	}

	if err := o.deps.Servers.Delete(ctx, st.VPSID); err != nil {
		return err
	}
	o.log.Info("vps deleted", "id", st.VPSID)
	_ = o.store.Log("deleted", "vps %s deleted", st.VPSID)

	// Confirm it is gone before claiming OFFLINE, otherwise a failed delete gets
	// forgotten and keeps billing.
	if _, err := o.deps.Servers.Get(ctx, st.VPSID); err == nil {
		return fmt.Errorf("vps %s still present after delete", st.VPSID)
	} else if !errors.Is(err, hetzner.ErrNotFound) {
		return err
	}

	return o.finalize(st, "terminated")
}

// nothingToSave decides whether a terminating server has a world worth
// archiving. Two cases say no: it never answered RCON, so Minecraft never
// started (the provisioning-timeout path), or the world directory is not there
// (a boot that died before the R2 pull finished).
//
// Anything uncertain — SSH down, unrecognised output — returns false, because
// trying to save and failing is recoverable and deleting an unbacked-up world
// is not.
func (o *Orchestrator) nothingToSave(ctx context.Context, st store.ServerState) (bool, error) {
	everRunning, err := o.store.MetaGet("ever_running")
	if err != nil {
		return false, err
	}
	if everRunning != st.VPSID {
		o.log.Info("no world to save: server never reached RUNNING", "id", st.VPSID)
		_ = o.store.Log("no-world", "vps %s never reached RUNNING, deleting without a save", st.VPSID)
		return true, nil
	}

	out, err := o.deps.Remote.Run(ctx, o.dialHost(ctx, st),
		"test -d /opt/minecraft/world && echo outpost=world-present || echo outpost=world-absent")
	if err != nil {
		return false, nil
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "outpost=world-absent" {
			o.log.Warn("no world directory on the vps, deleting without a save", "id", st.VPSID)
			_ = o.store.Log("no-world", "vps %s has no world directory, deleting without a save", st.VPSID)
			return true, nil
		}
	}
	return false, nil
}

// archiveWorld stops the server, uploads the world to R2 and records the backup.
// done is false when the attempt failed and the caller should retry on the next
// tick; after maxWorldSaveAttempts it moves the machine to ERROR and leaves the
// VPS running, on purpose.
func (o *Orchestrator) archiveWorld(ctx context.Context, st store.ServerState) (done bool, err error) {
	size, err := o.persistWorld(ctx, st)
	if err != nil {
		attempts, _ := o.store.MetaInt("save_attempts")
		attempts++
		_ = o.store.MetaSet("save_attempts", fmt.Sprint(attempts))
		o.log.Error("world save failed", "attempt", attempts, "err", err)

		if attempts >= maxWorldSaveAttempts {
			// Deliberately leave the VPS running. A server you are still paying
			// for is recoverable; a deleted world is not.
			st.LastError = fmt.Sprintf("world save failed after %d attempts: %v", attempts, err)
			_ = o.store.SetState(st)
			_ = o.store.Log("save-failed", "%v (vps %s left running)", err, st.VPSID)
			return false, o.transition(st, store.Error)
		}
		return false, nil
	}

	if sess, _ := o.store.CurrentSession(); sess != nil {
		_ = o.store.RecordBackup(size, sess.ID, "shutdown")
	}
	if err := o.store.MetaSet("world_saved", st.VPSID); err != nil {
		return false, err
	}
	_ = o.store.Log("saved", "world uploaded, %d bytes", size)
	return true, nil
}

// persistWorld stops Minecraft cleanly and ships the world to R2.
func (o *Orchestrator) persistWorld(ctx context.Context, st store.ServerState) (int64, error) {
	host := o.dialHost(ctx, st)
	addr := net.JoinHostPort(host, fmt.Sprint(rconPort))

	if c, err := rcon.Dial(ctx, addr, st.RCONPassword); err == nil {
		_, _ = c.Command(ctx, "save-all flush")
		_, _ = c.Command(ctx, "stop")
		_ = c.Close()
	} else {
		o.log.Warn("could not ask minecraft to save, archiving anyway", "err", err)
	}

	// Give the JVM time to exit so the tar is not of a half-written region file.
	//
	// The bracket in [f]abric... is load-bearing: this command's own text
	// contains "fabric-server-launch", so a plain `pgrep -f` matches the shell
	// running it and the loop always runs its full two minutes.
	wait := `for i in $(seq 1 60); do pgrep -f '[f]abric-server-launch' >/dev/null || exit 0; sleep 2; done; exit 1`
	if _, err := o.deps.Remote.Run(ctx, host, wait); err != nil {
		o.log.Warn("minecraft did not stop cleanly", "err", err)
	}

	// rclone runs on the VPS, so the world crosses the network once (out to R2)
	// instead of twice (down to the Potato and back up).
	script := fmt.Sprintf(`set -e
cd /opt/minecraft
tar -czf /tmp/outpost-world.tar.gz world
SIZE=$(stat -c%%s /tmp/outpost-world.tar.gz)
STAMP=$(date -u +%%Y%%m%%dT%%H%%M%%SZ)
rclone copyto /tmp/outpost-world.tar.gz r2:%[1]s/current/world.tar.gz --retries 3
rclone copyto /tmp/outpost-world.tar.gz r2:%[1]s/backups/world-$STAMP.tar.gz --retries 3
rclone delete r2:%[1]s/backups/ --include "world-*.tar.gz" --min-age 14d || true
rm -f /tmp/outpost-world.tar.gz
echo SIZE=$SIZE`, o.cfg.Server.WorldBucket)

	out, err := o.deps.Remote.Run(ctx, host, script)
	if err != nil {
		return 0, err
	}

	var size int64
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "SIZE="); ok {
			_, _ = fmt.Sscanf(v, "%d", &size)
		}
	}
	if size == 0 {
		return 0, fmt.Errorf("world upload reported no size: %s", strings.TrimSpace(out))
	}

	_ = o.store.MetaSet("save_attempts", "0")
	return size, nil
}

// probe opens RCON, asks for the player list and hangs up. Short-lived on
// purpose: a long-lived connection is state that can go stale.
func probe(ctx context.Context, addr, password string) (rcon.Players, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	c, err := rcon.Dial(ctx, addr, password)
	if err != nil {
		return rcon.Players{}, err
	}
	defer c.Close()

	return c.List(ctx)
}

// finalize closes the books and returns to OFFLINE.
func (o *Orchestrator) finalize(st store.ServerState, reason string) error {
	if sess, err := o.store.CurrentSession(); err == nil && sess != nil && sess.EndedAt.IsZero() {
		duration := int64(o.now().Sub(sess.StartedAt).Seconds())
		if duration < 0 {
			duration = 0
		}
		rate := 0.0
		if reg, ok := o.cfg.Regions[sess.Region]; ok {
			rate = reg.HourlyRate
		}
		cost := rate * float64(duration) / 3600
		if err := o.store.EndSession(sess.ID, duration, cost, sess.MaxPlayers); err != nil {
			return err
		}
		o.log.Info("session closed", "seconds", duration, "cost_usd", cost)
		_ = o.store.Log("session", "%dm, $%.4f", duration/60, cost)
	}

	if err := o.store.Reset(); err != nil {
		return err
	}
	_ = o.store.MetaSet("world_saved", "")
	_ = o.store.MetaSet("rcon_failures", "0")

	o.log.Info("offline", "reason", reason)
	return o.store.Log("offline", "%s", reason)
}

// transition moves to a new state, recording why.
func (o *Orchestrator) transition(st store.ServerState, to store.State) error {
	st.State = to
	return o.store.SetState(st)
}

// Reset deletes any Outpost-labelled server that exists and returns to OFFLINE.
// The escape hatch for when something has gone properly wrong.
func (o *Orchestrator) Reset(ctx context.Context) error {
	st, err := o.store.GetState()
	if err != nil {
		return err
	}
	if st.VPSID != "" {
		if err := o.deps.Servers.Delete(ctx, st.VPSID); err != nil {
			return err
		}
	}

	servers, err := o.deps.Servers.List(ctx)
	if err != nil {
		return err
	}
	for _, s := range servers {
		o.log.Warn("deleting stray server", "id", s.ID, "name", s.Name, "status", s.Status)
		if err := o.deps.Servers.Delete(ctx, fmt.Sprint(s.ID)); err != nil {
			return err
		}
	}

	_ = o.store.Log("reset", "deleted %d stray server(s)", len(servers))
	return o.finalize(st, "manual reset")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
