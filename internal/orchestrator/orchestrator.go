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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"outpost/internal/cloudinit"
	"outpost/internal/config"
	"outpost/internal/dns"
	"outpost/internal/hetzner"
	"outpost/internal/pack"
	"outpost/internal/publish"
	"outpost/internal/r2"
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

// Assets is the asset bucket, asked one question: is there a server to boot for
// this profile? A missing one is refused before renting, because the alternative
// is a VPS that pulls nothing, generates a vanilla world, never answers RCON, and
// times out fifteen minutes later.
type Assets interface {
	Stat(ctx context.Context, bucket, key string) (r2.Info, error)
	// Get reads the profile's pack.json, which is where the pack says how much RAM
	// it wants: the number the plan picker defaults to and a start refuses below.
	Get(ctx context.Context, bucket, key string) ([]byte, error)
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
	Exec    Exec
	Assets  Assets
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

	// mu serialises Tick and Start inside one process, so the cross-process
	// file lock below never has to talk to itself.
	mu sync.Mutex
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
			Exec:    execRCON,
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
	if deps.Exec != nil {
		o.deps.Exec = deps.Exec
	}
	if deps.Assets != nil {
		o.deps.Assets = deps.Assets
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
// Money is euros, because that is the only currency Hetzner bills in. A running
// session is priced at the rate recorded on its row; with nothing rented it is
// priced at the plan the next start would choose. It is a readout, never a bill.
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
	// The rate the session was rented at, not the rate of whatever config says
	// today: Hetzner keeps a server on its original price, and a plan repricing
	// must not rewrite what a running session is costing.
	out.HourlyRate = o.hourlyRate(st)
	out.SessionCost = out.HourlyRate * float64(out.UptimeSec) / 3600
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

// Health is the control loop's own vital signs, for the page.
type Health struct {
	RCONFailures        int    `json:"rconFailures"`
	RCONFailureLimit    int    `json:"rconFailureLimit"`
	EverRunning         bool   `json:"everRunning"`
	ProvisionTimeoutSec int64  `json:"provisionTimeoutSeconds"`
	LastDNS             string `json:"lastDns"`
}

// Health reads the counters Tick keeps in the meta table.
func (o *Orchestrator) Health() Health {
	st, _ := o.store.GetState()
	failures, _ := o.store.MetaInt("rcon_failures")
	ran, _ := o.store.MetaGet("ever_running")
	last, _ := o.store.MetaGet("last_dns")
	return Health{
		RCONFailures:        failures,
		RCONFailureLimit:    o.rconFailureLimit(),
		EverRunning:         st.VPSID != "" && ran == st.VPSID,
		ProvisionTimeoutSec: int64(o.cfg.Server.ProvisionTimeout.Std().Seconds()),
		LastDNS:             last,
	}
}

// metaProfile is the active pack+world pair. It lives in the meta table rather
// than in config.yaml because the page switches it, and because it is state
// ("what am I playing") rather than configuration.
const metaProfile = "profile"

// metaServerType is the plan the last start rented. It is remembered rather than
// written back into config.yaml: the page picks, the choice sticks, and the file
// stays the source of defaults (PLAN 36).
const metaServerType = "server_type"

// ActiveProfile is the pack+world pair the next session boots.
func (o *Orchestrator) ActiveProfile() (string, error) {
	p, err := o.store.MetaGet(metaProfile)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(p) == "" {
		return publish.DefaultProfile, nil
	}
	return p, nil
}

// SetActiveProfile switches what the next session boots. It refuses while
// anything is rented: a running server already pulled one profile's assets and
// will upload its world to that profile, so a switch mid-session is a world in
// the wrong drawer.
func (o *Orchestrator) SetActiveProfile(profile string) error {
	if err := publish.ValidateProfile(profile); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	st, err := o.store.GetState()
	if err != nil {
		return err
	}
	if st.State != store.Offline {
		return fmt.Errorf("the server is %s; switch packs when it is offline", st.State)
	}
	return o.store.MetaSet(metaProfile, profile)
}

// requireAssets refuses to rent for a profile that has no server files in R2.
func (o *Orchestrator) requireAssets(ctx context.Context, profile string) error {
	assets := o.deps.Assets
	if assets == nil {
		c, err := r2.New(o.cfg.R2.AccountID, o.cfg.Secrets.R2AccessKey, o.cfg.Secrets.R2SecretKey)
		if err != nil {
			return err
		}
		assets = c
	}
	key := "profiles/" + profile + "/server/fabric-server-launch.jar"
	if _, err := assets.Stat(ctx, o.cfg.Server.AssetBucket, key); err != nil {
		if errors.Is(err, r2.ErrNotFound) {
			return fmt.Errorf("profile %q has no server installed (no %s in %s); publish a pack to it first — see the Modpack panel",
				profile, key, o.cfg.Server.AssetBucket)
		}
		return fmt.Errorf("check the %q profile's assets: %w", profile, err)
	}
	return nil
}

// Start rents a VPS. Provisioning itself is Tick's job, so a crash between "we
// asked Hetzner" and "Hetzner answered" cannot lose the server: the vps_id is
// committed as soon as it exists.
func (o *Orchestrator) Start(ctx context.Context, region, serverType string) (store.ServerState, error) {
	// Renting is the one action that costs money the instant it succeeds, so it
	// is serialised across processes before anything else is read: `outpost
	// serve` and an `outpost start` shell must not both pass the state check.
	o.mu.Lock()
	defer o.mu.Unlock()

	// A browser tab closed between "Hetzner accepted the order" and "Hetzner
	// answered" must not cancel the call that records the server. Renting runs
	// to completion on its own clock; the request that asked for it only waits.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()

	release, ok, err := o.store.TryLock()
	if err != nil {
		return store.ServerState{}, err
	}
	if !ok {
		return store.ServerState{}, fmt.Errorf("another outpost process is busy; try again")
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
		// `stop` is listed first on purpose: it retries the save, whereas `reset`
		// deletes the server and the world with it.
		return st, fmt.Errorf("state is ERROR with server %s still running; run `outpost stop` to retry the world save (`outpost reset --force` abandons it)", st.VPSID)
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
		return st, fmt.Errorf("%d outpost server(s) already exist in Hetzner (%s) that this database does not know about; look at it before deleting anything, then `outpost reset --force`", len(stray), stray[0].Name)
	}

	reg, err := o.cfg.Region(region)
	if err != nil {
		return st, err
	}

	// Which plan this session rents. The chosen one wins, then the one remembered
	// from last time, then the region's default — and the choice is remembered
	// here, so a page pick and a CLI start mean the same thing next time.
	plan, err := o.planFor(reg, serverType)
	if err != nil {
		return st, err
	}

	// A start that lands in the middle of an install would pull a profile whose
	// keys are half old and half new — the same failure as a missing profile, only
	// harder to see, so it is refused the same way.
	if profile, since, busy := pack.InstallingNow(o.cfg.Server.PackDir); busy {
		return st, fmt.Errorf("profile %q is being installed (%s); wait for it to finish before starting",
			profile, since.Local().Format("15:04"))
	}

	// Fixed for the whole session: the boot pulls this profile's assets and the
	// shutdown uploads this profile's world, so the pair cannot be half-switched.
	profile, err := o.ActiveProfile()
	if err != nil {
		return st, err
	}
	if err := o.requireAssets(ctx, profile); err != nil {
		return st, err
	}
	// A plan too small for the pack is a server that dies on its first save, so it
	// is refused here rather than discovered in a crash log.
	if err := o.requirePlanFit(ctx, profile, plan); err != nil {
		return st, err
	}
	st.Profile = profile

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
		Profile:          profile,
		Java:             o.profileJava(ctx, profile),
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

	o.log.Info("renting server", "region", region, "type", plan.Type, "hostname", hostname)

	srv, rentedType, err := o.rent(ctx, hostname, reg, plan.Type, userData)
	if err != nil {
		return st, err
	}

	st = store.ServerState{
		State:        store.Provisioning,
		VPSID:        fmt.Sprint(srv.ID),
		VPSIP:        srv.PublicNet.IPv4.IP,
		TailnetHost:  hostname,
		Region:       region,
		ServerType:   rentedType,
		RCONPassword: password,
		Profile:      profile,
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
	// Fresh rent, fresh bookkeeping. A missed-probe count carried over from the last
	// session asks this box for its log on the first tick — seconds after rent, when
	// cloud-init has not written one — and that early answer then suppresses the
	// question for the failure that actually matters. The summary belongs to the box
	// that produced it, so it goes too: attributing one box's crash to the next is
	// worse than saying nothing.
	_ = o.store.MetaSet("provision_misses", "0")
	_ = o.store.MetaSet("why_reported", "")
	_ = o.store.MetaSet("why_summary", "")
	_ = o.store.MetaSet("why_tries", "0")
	_ = o.store.MetaSet("why_tries_for", "")
	_ = o.store.MetaSet("why_summary_vps", "")

	// The rate of the plan that was actually rented, which is not always the one
	// asked for: a sold-out plan falls back, and a fallback size has its own price.
	rate, _ := hetzner.Rate(rentedType)
	if _, err := o.store.StartSession(st.StartedAt, region, rentedType, rate, fmt.Sprint(srv.ID)); err != nil {
		return st, err
	}
	_ = o.store.MetaSet(metaServerType, rentedType)
	_ = o.store.Log("start", "rented %s in %s (%s, %.4f EUR/h)", hostname, reg.Location, rentedType, rate)

	return st, nil
}

// rent creates the server, walking through the configured fallback sizes when
// Hetzner has no capacity for the preferred one. Thin stock is the normal state
// of the world Outpost lives in, not an exceptional condition.
func (o *Orchestrator) rent(ctx context.Context, hostname string, reg config.Region, plan string, userData string) (*hetzner.Server, string, error) {
	types := append([]string{plan}, reg.FallbackTypes...)

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
			// A transport error does not mean the order failed: Hetzner may have
			// placed it and lost the reply. Look before giving up, or the server
			// bills with nothing tracking it.
			if adopted := o.adoptByName(hostname); adopted != nil {
				o.log.Warn("create reported an error but the server exists, adopting it", "id", adopted.ID, "err", err)
				_ = o.store.Log("adopted", "create failed (%v) but %s exists; tracking it", err, hostname)
				_ = o.store.MetaSet("rented_type", serverType)
				return adopted, serverType, nil
			}
			return nil, "", err
		}
		lastErr = err
		o.log.Warn("no capacity", "type", serverType, "location", reg.Location)
		_ = o.store.Log("no-capacity", "%s in %s: %v", serverType, reg.Location, err)
	}

	return nil, "", fmt.Errorf("no capacity in %s for any of %v: %w", reg.Location, types, lastErr)
}

// adoptByName finds a server this process asked for by name. It uses its own
// short deadline because the caller's context may be the thing that just failed.
func (o *Orchestrator) adoptByName(hostname string) *hetzner.Server {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	servers, err := o.deps.Servers.List(ctx)
	if err != nil {
		return nil
	}
	for i := range servers {
		if servers[i].Name == hostname {
			return &servers[i]
		}
	}
	return nil
}

// lockTimeout bounds how long Stop and Reset wait for a tick that is mid-pass.
// A pass that is uploading a world can run for minutes; ten is generous and
// still ends, which a CLI blocked forever on a wedged holder would not.
const lockTimeout = 10 * time.Minute

// lockForWrite takes the in-process mutex and the cross-process file lock,
// waiting for both. Stop and Reset use it instead of TryLock because they must
// not be dropped, and must not interleave with a tick that holds a stale copy
// of the row: Tick writes back the whole state it read at the start of its
// pass, so an unlocked Stop that lands mid-tick is silently overwritten.
func (o *Orchestrator) lockForWrite(ctx context.Context) (func(), error) {
	o.mu.Lock()
	lctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	release, err := o.store.Lock(lctx)
	if err != nil {
		o.mu.Unlock()
		return nil, err
	}
	return func() {
		release()
		o.mu.Unlock()
	}, nil
}

// Stop asks for a graceful shutdown. Tick does the work.
func (o *Orchestrator) Stop(ctx context.Context) error {
	unlock, err := o.lockForWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()

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
	if st.State == store.Error {
		// Leaving ERROR is a fresh attempt at the save. The counter that put the
		// machine here would otherwise turn the retry into one try and straight
		// back to ERROR.
		_ = o.store.MetaSet("save_attempts", "0")
	}
	_ = o.store.Log("stop", "manual shutdown requested")
	return o.transition(st, store.Terminating)
}

// Tick advances the machine by one step. Safe to call from any state, as often
// as you like.
//
// Only one process ticks at a time. Two ticks racing on the same state is how a
// world gets saved twice and two backup rows appear for one shutdown: the first
// real session's `outpost stop` ran while `outpost serve` was mid-tick, and both
// of them archived the world.
func (o *Orchestrator) Tick(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	release, ok, err := o.store.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		o.log.Debug("another process is ticking, skipping this pass")
		return nil
	}
	defer release()

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
		reason := fmt.Sprintf("provisioning timed out after %s", elapsed.Round(time.Second))
		if summary := o.reportWhy(ctx, st, reason, true); summary != "" {
			// The log's answer beats the symptom: "timed out" alone is what this
			// session spent fifteen minutes learning nothing from.
			reason += ": " + summary
		}
		st.LastError = truncate(reason, 240)
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

		// The same question as a live failure, and the one that went unanswered for
		// fifteen minutes the first time a pack could not load: a server that never
		// comes up is a log nobody reads. Six misses is two minutes, which is past
		// any boot and short enough to be before somebody gives up.
		misses, _ := o.store.MetaInt("provision_misses")
		misses++
		_ = o.store.MetaSet("provision_misses", fmt.Sprint(misses))
		// "At least", not "exactly": the counter can start above zero (a new rent
		// after a session that never came up), and the once-per-VPS guard inside
		// reportWhy is what stops it repeating.
		if misses >= 6 {
			o.reportWhy(ctx, st, "the server has not answered RCON yet; it is still booting", false)
		}
		return nil
	}
	_ = o.store.MetaSet("provision_misses", "0")

	next := st
	next.State = store.Running
	next.Players = players.Online
	next.VPSIP = srv.PublicNet.IPv4.IP
	next.LastError = ""
	// Record that this VPS actually served, so the shutdown path can tell a
	// server with a world from one that never got past its own boot.
	_ = o.store.MetaSet("ever_running", st.VPSID)
	_ = o.store.MetaSet("online_names", strings.Join(players.Names, ","))
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
		_ = o.store.MetaSet("last_dns", next.VPSIP)
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

		if failures >= whyAfterFailures {
			if summary := o.reportWhy(ctx, st, "the server stopped answering RCON", false); summary != "" {
				// The hero shows lastError, so the reason a player was kicked belongs
				// there and not only in the event trail.
				st.LastError = truncate("the server stopped answering RCON: "+summary, 240)
				_ = o.store.SetState(st)
			}
		}

		if failures >= o.rconFailureLimit() {
			_ = o.store.MetaSet("rcon_failures", "0")
			_ = o.store.Log("rcon-dead", "%d consecutive failures, shutting down", failures)
			reason := fmt.Sprintf("rcon unreachable: %v", probeErr)
			// This is the terminal path, so it is the one that may call it a crash — and
			// the trail should say why the session ended, even when a slow-boot `why`
			// already reported something about the same box.
			if summary := o.reportWhy(ctx, st, "the server died and stopped answering RCON", true); summary != "" {
				reason = "the server died: " + summary
			}
			st.LastError = truncate(reason, 240)
			_ = o.store.SetState(st)
			return o.transition(st, store.Terminating)
		}
		return nil
	}
	_ = o.store.MetaSet("rcon_failures", "0")
	_ = o.store.MetaSet("online_names", strings.Join(players.Names, ","))

	next := st
	next.Players = players.Online
	next.LastError = ""

	// The record is set once, when RCON first answers. If that call failed the
	// server is up and nobody can find it by name, so try again every pass until
	// it takes: players use mc.<domain>, not the IP.
	if next.DNSValue != next.VPSIP && next.VPSIP != "" {
		if err := o.deps.DNS.SetA(ctx, next.VPSIP); err != nil {
			o.log.Warn("dns update still failing", "err", err)
		} else {
			next.DNSValue = next.VPSIP
			_ = o.store.MetaSet("last_dns", next.VPSIP)
			_ = o.store.Log("dns", "%s now points at %s", o.cfg.Cloudflare.RecordName, next.VPSIP)
		}
	}

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
	o.log.Info("vps delete requested", "id", st.VPSID)

	// Confirm it is gone before claiming OFFLINE, otherwise a failed delete gets
	// forgotten and keeps billing. Hetzner deletes asynchronously, so for a few
	// seconds the server is still listed with status "deleting": that is progress,
	// not a failure, and the next pass finishes the job.
	srv, err := o.deps.Servers.Get(ctx, st.VPSID)
	switch {
	case errors.Is(err, hetzner.ErrNotFound):
		_ = o.store.Log("deleted", "vps %s deleted", st.VPSID)
	case err != nil:
		return err
	case srv.Status == "deleting":
		o.log.Info("vps is being deleted", "id", st.VPSID)
		return nil
	default:
		return fmt.Errorf("vps %s still present after delete (status %s)", st.VPSID, srv.Status)
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

	// Wait for the JVM to exit so the tar is not of a half-written region file.
	//
	// Ask systemd, not pgrep: the unit is the one thing that knows when
	// start.sh's process is gone whatever the loader is called, and a pgrep
	// pattern has to match its own command line (the first real run burned the
	// whole two minutes on that). If the server is still up after the wait the
	// attempt fails rather than archiving a live world: this upload replaces
	// current/world.tar.gz, so a torn tar would overwrite the only good copy.
	wait := `for i in $(seq 1 60); do systemctl is-active --quiet minecraft || exit 0; sleep 2; done; exit 1`
	if _, err := o.deps.Remote.Run(ctx, host, wait); err != nil {
		return 0, fmt.Errorf("minecraft did not stop within two minutes, not archiving a live world: %w", err)
	}

	// rclone runs on the VPS, so the world crosses the network once (out to R2)
	// instead of twice (down to the Potato and back up).
	// The profile the session booted, not the profile that is active now: a switch
	// while this server was up must not file its world under the new one.
	profile := st.Profile
	if profile == "" {
		profile = publish.DefaultProfile
	}
	if err := publish.ValidateProfile(profile); err != nil {
		return 0, err
	}

	script := fmt.Sprintf(`set -e
cd /opt/minecraft
tar -czf /tmp/outpost-world.tar.gz world
SIZE=$(stat -c%%s /tmp/outpost-world.tar.gz)
STAMP=$(date -u +%%Y%%m%%dT%%H%%M%%SZ)
rclone copyto /tmp/outpost-world.tar.gz r2:%[1]s/profiles/%[2]s/world.tar.gz --retries 3
rclone copyto /tmp/outpost-world.tar.gz r2:%[1]s/profiles/%[2]s/backups/world-$STAMP.tar.gz --retries 3
rclone delete r2:%[1]s/profiles/%[2]s/backups/ --include "world-*.tar.gz" --min-age 14d || true
rm -f /tmp/outpost-world.tar.gz
echo SIZE=$SIZE`, o.cfg.Server.WorldBucket, profile)

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
		rate := sess.HourlyRate
		if rate == 0 {
			// A row written before the rate was stored, or one whose plan this
			// build does not know. Better an estimate from the table than a
			// session recorded as free.
			rate, _ = hetzner.Rate(sess.ServerType)
		}
		cost := rate * float64(duration) / 3600
		if err := o.store.EndSession(sess.ID, duration, cost, sess.MaxPlayers); err != nil {
			return err
		}
		o.log.Info("session closed", "seconds", duration, "cost_eur", cost)
		_ = o.store.Log("session", "%dm, %.4f EUR", duration/60, cost)
	}

	if err := o.store.Reset(); err != nil {
		return err
	}
	_ = o.store.MetaSet("world_saved", "")
	_ = o.store.MetaSet("rcon_failures", "0")
	_ = o.store.MetaSet("online_names", "")

	o.log.Info("offline", "reason", reason)
	return o.store.Log("offline", "%s", reason)
}

// transition moves to a new state, recording why.
func (o *Orchestrator) transition(st store.ServerState, to store.State) error {
	st.State = to
	return o.store.SetState(st)
}

// ErrUnsavedWorld is what Reset answers when deleting would lose a world.
var ErrUnsavedWorld = errors.New("the world on this server has not been saved to R2")

// Reset deletes any Outpost-labelled server that exists and returns to OFFLINE.
// The escape hatch for when something has gone properly wrong.
//
// It is also the one command that can destroy a world, so it refuses when the
// tracked server has run (so it has a world) and that world was never uploaded,
// unless force is set. `outpost stop` is the command that retries the save.
func (o *Orchestrator) Reset(ctx context.Context, force bool) error {
	unlock, err := o.lockForWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	st, err := o.store.GetState()
	if err != nil {
		return err
	}
	if st.VPSID != "" && !force {
		ran, _ := o.store.MetaGet("ever_running")
		saved, _ := o.store.MetaGet("world_saved")
		if ran == st.VPSID && saved != st.VPSID {
			return fmt.Errorf("%w (server %s); `outpost stop` retries the save, `outpost reset --force` deletes it anyway", ErrUnsavedWorld, st.VPSID)
		}
	}
	servers, err := o.deps.Servers.List(ctx)
	if err != nil {
		return err
	}
	if !force {
		// Servers this database does not know about have worlds nobody has
		// looked at; deleting them is a decision, not a cleanup.
		var unknown []string
		for _, s := range servers {
			if fmt.Sprint(s.ID) != st.VPSID {
				unknown = append(unknown, s.Name)
			}
		}
		if len(unknown) > 0 {
			return fmt.Errorf("%d server(s) exist in Hetzner that this database is not tracking (%s); check them, then `outpost reset --force`", len(unknown), strings.Join(unknown, ", "))
		}
	}

	if st.VPSID != "" {
		if err := o.deps.Servers.Delete(ctx, st.VPSID); err != nil {
			return err
		}
	}
	for _, s := range servers {
		o.log.Warn("deleting server", "id", s.ID, "name", s.Name, "status", s.Status)
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

// planFor answers which plan a start rents: the one asked for, else the one
// remembered from last time, else the region's default.
//
// A plan this build does not know is allowed. A stale table must never refuse a
// rental over a name Hetzner invented afterwards; the cost readout says it has
// no price for that plan rather than inventing one.
func (o *Orchestrator) planFor(reg config.Region, asked string) (hetzner.Plan, error) {
	chosen := strings.TrimSpace(asked)
	if chosen == "" {
		if remembered, err := o.store.MetaGet(metaServerType); err == nil && strings.TrimSpace(remembered) != "" {
			chosen = strings.TrimSpace(remembered)
		}
	}
	if chosen == "" {
		chosen = reg.ServerType
	}
	if chosen == "" {
		return hetzner.Plan{}, fmt.Errorf("region %q has no server_type and none was chosen", reg.Location)
	}
	if p, ok := hetzner.Lookup(chosen); ok {
		return p, nil
	}
	// Unknown, but rentable: carry the name and leave the rate at zero.
	return hetzner.Plan{Type: chosen}, nil
}

// requirePlanFit refuses a plan too small for the active pack, before anything is
// rented. It is the same rule the picker uses to choose a default, asked at the
// only moment it cannot be got wrong: the pack's own memory figure against the
// plan's RAM.
func (o *Orchestrator) requirePlanFit(ctx context.Context, profile string, plan hetzner.Plan) error {
	if plan.RAMGB == 0 {
		return nil // unknown plan: the table has no opinion, so neither does this
	}
	want, err := o.packMemoryGB(ctx, profile)
	if err != nil {
		return err
	}
	if want == 0 || plan.RAMGB >= want {
		return nil
	}
	bigger := ""
	if best, ok := hetzner.SmallestFit(want); ok {
		bigger = fmt.Sprintf("; %s has %d GB for EUR %.4f/h", best.Type, best.RAMGB, best.HourlyEUR)
	}
	return fmt.Errorf("%s has %d GB and the active pack asks for %d GB%s — choose a bigger plan before starting",
		plan.Type, plan.RAMGB, want, bigger)
}

// PackMemoryGB is the RAM the active profile's pack asks for, and zero when there
// is no manifest or no figure in it.
func (o *Orchestrator) PackMemoryGB(ctx context.Context) (int, error) {
	profile, err := o.ActiveProfile()
	if err != nil {
		return 0, err
	}
	return o.packMemoryGB(ctx, profile)
}

// packMemoryGB reads the pack's own memory figure out of its manifest.
func (o *Orchestrator) packMemoryGB(ctx context.Context, profile string) (int, error) {
	pk, ok, err := o.profilePack(ctx, profile)
	if err != nil || !ok || pk.MemoryGB <= 0 {
		return 0, err
	}
	return pk.MemoryGB, nil
}

// profileJava is the JDK this profile's pack needs, which is the JDK the VPS
// installs. A profile with no manifest is assumed to be for SupportedMC, the same
// assumption a pack that names no version gets: guessing is only a risk when it
// drives something that cannot be undone, and a JDK is one package.
//
// The table is publish's, so "which Java does 1.21.1 need" is answered in one
// place. A manifest naming a version outside it falls back rather than refusing
// here: publish refused it at upload time, and a start is the wrong moment to
// rediscover that (the world may simply be older than the manifest).
func (o *Orchestrator) profileJava(ctx context.Context, profile string) int {
	pk, ok, err := o.profilePack(ctx, profile)
	if err != nil {
		// Not fatal: an unreadable manifest is not worth an unrented session, and
		// the boot's own refusals are louder than this one would be.
		o.log.Warn("could not read the profile's pack.json for its Minecraft version", "profile", profile, "error", err)
	}
	if !ok {
		return defaultJava
	}
	if java, known := publish.JavaFor(pk.MC); known {
		return java
	}
	return defaultJava
}

// defaultJava is the JDK for SupportedMC, resolved once. It is the fallback for a
// profile with no manifest, and a zero here (SupportedMC missing from the table)
// is refused by cloudinit.Validate before a rental rather than rented and booted.
var defaultJava = func() int {
	java, _ := publish.JavaFor(publish.SupportedMC)
	return java
}()

// profilePack reads profiles/<profile>/pack.json. ok=false means there is no
// manifest, which is a hand-rolled profile and not an error.
func (o *Orchestrator) profilePack(ctx context.Context, profile string) (publish.Pack, bool, error) {
	assets := o.deps.Assets
	if assets == nil {
		c, err := r2.New(o.cfg.R2.AccountID, o.cfg.Secrets.R2AccessKey, o.cfg.Secrets.R2SecretKey)
		if err != nil {
			return publish.Pack{}, false, err
		}
		assets = c
	}
	raw, err := assets.Get(ctx, o.cfg.Server.AssetBucket, "profiles/"+profile+"/pack.json")
	if err != nil {
		if errors.Is(err, r2.ErrNotFound) {
			return publish.Pack{}, false, nil
		}
		return publish.Pack{}, false, err
	}
	var pk publish.Pack
	if err := json.Unmarshal(raw, &pk); err != nil {
		// Refused rather than ignored: the file is written by publish and read by
		// nothing else, so one that does not parse is a truncated upload or an edit
		// by hand — and both mean the profile may be half of what it claims.
		return publish.Pack{}, false, fmt.Errorf("parse profiles/%s/pack.json: %w; republish the pack to rewrite it", profile, err)
	}
	return pk, true, nil
}

// hourlyRate is what the current session costs per hour. With no session live it
// is what the next start would cost, so the page can price the button it shows.
func (o *Orchestrator) hourlyRate(st store.ServerState) float64 {
	if sess, err := o.store.CurrentSession(); err == nil && sess != nil && sess.EndedAt.IsZero() {
		if sess.HourlyRate > 0 {
			return sess.HourlyRate
		}
		if rate, ok := hetzner.Rate(sess.ServerType); ok {
			return rate
		}
	}
	if st.ServerType != "" {
		if rate, ok := hetzner.Rate(st.ServerType); ok {
			return rate
		}
	}
	// Nothing rented: the plan a start would pick right now.
	reg, err := o.cfg.Region(o.DefaultRegion())
	if err != nil {
		return 0
	}
	plan, err := o.planFor(reg, "")
	if err != nil {
		return 0
	}
	rate, _ := hetzner.Rate(plan.Type)
	return rate
}

// SelectedServerType is the plan the next start rents: the remembered choice, or
// the region's default. The page shows it as the picker's current value.
func (o *Orchestrator) SelectedServerType() string {
	reg, err := o.cfg.Region(o.DefaultRegion())
	if err != nil {
		return ""
	}
	plan, err := o.planFor(reg, "")
	if err != nil {
		return reg.ServerType
	}
	return plan.Type
}
