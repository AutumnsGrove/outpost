package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"outpost/internal/config"
	"outpost/internal/hetzner"
	"outpost/internal/rcon"
	"outpost/internal/store"
)

// --- fakes -----------------------------------------------------------------

type fakeServers struct {
	mu      sync.Mutex
	nextID  int64
	servers map[string]*hetzner.Server

	// unavailable counts how many create attempts of a type should fail with
	// ErrUnavailable, the way a sold-out plan answers.
	unavailable map[string]int
	// createErr fails Create outright, for errors that must not be retried.
	createErr error
	// created records every server type asked for, in order.
	created []string
}

func newFakeServers() *fakeServers {
	return &fakeServers{servers: map[string]*hetzner.Server{}}
}

func (f *fakeServers) Create(_ context.Context, spec hetzner.CreateRequest) (*hetzner.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.created = append(f.created, spec.ServerType)
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.unavailable[spec.ServerType] > 0 {
		f.unavailable[spec.ServerType]--
		return nil, hetzner.ErrUnavailable
	}

	f.nextID++
	s := &hetzner.Server{ID: f.nextID, Name: spec.Name, Status: "running"}
	s.PublicNet.IPv4.IP = "203.0.113.10"
	f.servers[strconv.FormatInt(f.nextID, 10)] = s
	return s, nil
}

func (f *fakeServers) Get(_ context.Context, id string) (*hetzner.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.servers[id]
	if !ok {
		return nil, hetzner.ErrNotFound
	}
	return s, nil
}

func (f *fakeServers) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.servers, id)
	return nil
}

func (f *fakeServers) List(context.Context) ([]hetzner.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]hetzner.Server, 0, len(f.servers))
	for _, s := range f.servers {
		out = append(out, *s)
	}
	return out, nil
}

func (f *fakeServers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.servers)
}

type fakeDNS struct {
	last string
	err  error
}

func (d *fakeDNS) SetA(_ context.Context, ip string) error {
	d.last = ip
	return d.err
}

type fakeRemote struct {
	out string
	err error
	// calls records the commands, so tests can assert the world was archived.
	calls []string
}

func (r *fakeRemote) Run(_ context.Context, _ string, cmd string) (string, error) {
	r.calls = append(r.calls, cmd)
	if r.err != nil {
		return "", r.err
	}
	return r.out, nil
}

func (r *fakeRemote) savedWorld() bool {
	for _, c := range r.calls {
		if strings.Contains(c, "rclone copyto") {
			return true
		}
	}
	return false
}

// --- harness ---------------------------------------------------------------

func testConfig() *config.Config {
	return &config.Config{
		Listen:   "127.0.0.1:0",
		Database: "test.db",
		Server: config.Server{
			AssetBucket:      "mc-assets",
			WorldBucket:      "mc-worlds",
			MCPort:           25565,
			IdleGrace:        config.Duration(10 * time.Minute),
			IdleTimeout:      config.Duration(20 * time.Minute),
			ProvisionTimeout: config.Duration(15 * time.Minute),
			TickInterval:     config.Duration(time.Second),
		},
		Regions: map[string]config.Region{
			// 10 an hour keeps the cost arithmetic easy to read in assertions.
			"eu": {Location: "nbg1", ServerType: "cx32", HourlyRate: 10},
		},
		Cloudflare: config.Cloudflare{ZoneID: "zone", RecordName: "mc.test"},
		Hetzner: config.Hetzner{
			Image:          "ubuntu-24.04",
			SSHKeyID:       1,
			SSHUser:        "root",
			SSHPrivateKey:  "/dev/null",
			ServerNameBase: "outpost",
		},
		R2:        config.R2{AccountID: "account"},
		Tailscale: config.Tailscale{Tags: []string{"tag:outpost"}},
		Secrets: config.Secrets{
			HetznerToken:     "hetzner",
			CloudflareToken:  "cloudflare",
			TailscaleAuthKey: "tskey-test",
			R2AccessKey:      "key",
			R2SecretKey:      "secret",
			APIToken:         "api",
		},
	}
}

type harness struct {
	o       *Orchestrator
	store   *store.Store
	servers *fakeServers
	dns     *fakeDNS
	remote  *fakeRemote

	clock time.Time

	// probe answers the RCON question. Tests set it per scenario.
	players  int
	probeErr error

	// probeAddr records where the last probe was pointed.
	probeAddr string

	// resolveIP/resolveErr control what the tailnet lookup answers.
	resolveIP  string
	resolveErr error
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	st, err := store.Open(t.TempDir() + "/outpost.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	h := &harness{
		store:   st,
		servers: newFakeServers(),
		dns:     &fakeDNS{},
		remote:  &fakeRemote{out: "SIZE=4096\n"},
		clock:   time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
		// Loopback so the real rcon.Dial inside persistWorld refuses instantly
		// instead of waiting out a 5s connect timeout on a fake address.
		resolveIP: "127.0.0.1",
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.o = NewWith(testConfig(), st, log, Deps{
		Servers: h.servers,
		DNS:     h.dns,
		Remote:  h.remote,
		Now:     func() time.Time { return h.clock },
		Probe: func(_ context.Context, addr, _ string) (rcon.Players, error) {
			h.probeAddr = addr
			return rcon.Players{Online: h.players, Max: 20}, h.probeErr
		},
		Resolve: func(context.Context, string) (string, error) {
			return h.resolveIP, h.resolveErr
		},
	})
	return h
}

func (h *harness) mustState(t *testing.T) store.State {
	t.Helper()
	st, err := h.store.GetState()
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	return st.State
}

func (h *harness) tick(t *testing.T) {
	t.Helper()
	if err := h.o.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// --- tests -----------------------------------------------------------------

func TestFullLifecycleStartsIdlesAndCleansUp(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := h.mustState(t); got != store.Provisioning {
		t.Fatalf("state after start = %s, want PROVISIONING", got)
	}
	if h.servers.count() != 1 {
		t.Fatalf("expected one server, got %d", h.servers.count())
	}

	// Still booting: RCON is not answering yet.
	h.probeErr = errors.New("connection refused")
	h.tick(t)
	if got := h.mustState(t); got != store.Provisioning {
		t.Fatalf("state while booting = %s, want PROVISIONING", got)
	}

	// RCON answers with nobody on.
	h.probeErr = nil
	h.players = 0
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state when ready = %s, want RUNNING", got)
	}
	if h.dns.last != "203.0.113.10" {
		t.Fatalf("dns not pointed at the vps: %q", h.dns.last)
	}

	// Five minutes empty is still RUNNING with the clock running.
	h.clock = h.clock.Add(5 * time.Minute)
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state at 5m empty = %s, want RUNNING", got)
	}

	// Past the grace period: IDLE, still alive.
	h.clock = h.clock.Add(6 * time.Minute)
	h.tick(t)
	if got := h.mustState(t); got != store.Idle {
		t.Fatalf("state at 11m empty = %s, want IDLE", got)
	}

	// Someone comes back before the limit. Clock resets.
	h.players = 2
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state after rejoining = %s, want RUNNING", got)
	}
	st, _ := h.store.GetState()
	if !st.IdleSince.IsZero() {
		t.Fatalf("idle clock should be cleared, got %s", st.IdleSince)
	}

	// Everybody leaves again and stays gone past grace + timeout.
	h.players = 0
	h.tick(t) // starts the clock
	h.clock = h.clock.Add(31 * time.Minute)
	h.tick(t)
	if got := h.mustState(t); got != store.Terminating {
		t.Fatalf("state past limit = %s, want TERMINATING", got)
	}

	// The shutdown pass archives the world and deletes the server.
	h.tick(t)
	if !h.remote.savedWorld() {
		t.Fatal("world was never archived before deletion")
	}
	if h.servers.count() != 0 {
		t.Fatalf("server still exists after shutdown: %d", h.servers.count())
	}
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state after shutdown = %s, want OFFLINE", got)
	}

	// The books are closed. Billed time is start to shutdown: 42 minutes at
	// $10/hr = $7.00, which also proves the clock reset when players rejoined.
	sess, err := h.store.CurrentSession()
	if err != nil || sess == nil {
		t.Fatalf("session: %v", err)
	}
	if sess.EndedAt.IsZero() {
		t.Fatal("session was not closed")
	}
	if got, want := sess.DurationSeconds, int64(42*60); got != want {
		t.Fatalf("billed seconds = %d, want %d", got, want)
	}
	if got, want := sess.CostUSD, 7.0; got < want-0.01 || got > want+0.01 {
		t.Fatalf("session cost = %.4f, want ~%.2f", got, want)
	}
	if len(sess.VPSID) == 0 {
		t.Fatal("session lost its server id")
	}

	backups, err := h.store.RecentBackups(5)
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one backup, got %d (%v)", len(backups), err)
	}
	if backups[0].SizeBytes != 4096 {
		t.Fatalf("backup size = %d, want 4096", backups[0].SizeBytes)
	}
}

func TestWorldSaveFailureKeepsTheServerAlive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.probeErr = nil
	h.tick(t)

	if err := h.o.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	h.remote.err = errors.New("rclone: 403")

	for i := 0; i < maxWorldSaveAttempts; i++ {
		if err := h.o.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	if got := h.mustState(t); got != store.Error {
		t.Fatalf("state = %s, want ERROR", got)
	}
	if h.servers.count() != 1 {
		t.Fatal("server was deleted even though the world was never saved")
	}
	st, _ := h.store.GetState()
	if !strings.Contains(st.LastError, "world save failed") {
		t.Fatalf("state should explain itself, got %q", st.LastError)
	}
}

func TestRetrySucceedsAfterOneFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.tick(t) // running

	if err := h.o.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	h.remote.err = errors.New("transient")
	if err := h.o.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := h.mustState(t); got != store.Terminating {
		t.Fatalf("a single failure should keep retrying, state = %s", got)
	}

	h.remote.err = nil
	h.tick(t)
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state = %s, want OFFLINE", got)
	}
}

func TestProvisioningTimeoutTearsTheServerDown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}

	h.probeErr = errors.New("never comes up")
	h.clock = h.clock.Add(16 * time.Minute)
	h.tick(t) // notices the timeout, moves to TERMINATING

	if got := h.mustState(t); got != store.Terminating {
		t.Fatalf("state = %s, want TERMINATING", got)
	}

	h.tick(t) // tears down
	if h.servers.count() != 0 {
		t.Fatal("timed-out server was not deleted")
	}
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state = %s, want OFFLINE", got)
	}
}

func TestVanishedServerReturnsToOffline(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	st, _ := h.store.GetState()
	if err := h.servers.Delete(ctx, st.VPSID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	h.tick(t)
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state = %s, want OFFLINE", got)
	}
	sess, _ := h.store.CurrentSession()
	if sess == nil || sess.EndedAt.IsZero() {
		t.Fatal("session should be closed when the server disappears")
	}
}

func TestStartRefusesWhenAlreadyRunning(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := h.o.Start(ctx, "eu"); err == nil {
		t.Fatal("second start should be refused")
	}
}

func TestTickIsIdempotentWhenOffline(t *testing.T) {
	h := newHarness(t)

	for i := 0; i < 5; i++ {
		h.tick(t)
	}
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state = %s, want OFFLINE", got)
	}
	if h.servers.count() != 0 {
		t.Fatal("ticking offline should not create anything")
	}
}

func TestStartRefusesWhenTheVPSNeverCameUpHardlyMatters(t *testing.T) {
	// The provisioning timeout must not try to archive a world that was never
	// generated: v1's failure mode was backing up on every death, this is the
	// mirror of it.
	h := newHarness(t)
	if _, err := h.o.Start(context.Background(), "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}

	h.probeErr = errors.New("never comes up")
	h.clock = h.clock.Add(16 * time.Minute)
	h.tick(t) // -> TERMINATING
	h.tick(t) // -> OFFLINE

	if h.remote.savedWorld() {
		t.Fatal("a server that never ran should not attempt a world save")
	}
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state = %s, want OFFLINE", got)
	}
	if h.servers.count() != 0 {
		t.Fatal("timed-out server was not deleted")
	}
}

func TestStartRefusesFromErrorWithALiveVPS(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.tick(t) // RUNNING
	if err := h.o.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// The world never uploads, so the machine lands in ERROR with the VPS alive.
	h.remote.err = errors.New("rclone: 403")
	for i := 0; i < maxWorldSaveAttempts; i++ {
		h.tick(t)
	}
	if got := h.mustState(t); got != store.Error {
		t.Fatalf("state = %s, want ERROR", got)
	}

	if _, err := h.o.Start(ctx, "eu"); err == nil {
		t.Fatal("starting from ERROR with a live server should be refused")
	} else if !strings.Contains(err.Error(), "reset") {
		t.Fatalf("error should say what to do: %v", err)
	}
	if h.servers.count() != 1 {
		t.Fatalf("a second server was rented; %d exist", h.servers.count())
	}
}

func TestStartRefusesWhenHetznerHasAStrayServer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// SQLite says OFFLINE, Hetzner says otherwise: a lost or restored outpost.db.
	if _, err := h.servers.Create(ctx, hetzner.CreateRequest{Name: "outpost-eu-old"}); err != nil {
		t.Fatalf("seed stray: %v", err)
	}

	if _, err := h.o.Start(ctx, "eu"); err == nil {
		t.Fatal("start should be refused while a labelled server exists")
	} else if !strings.Contains(err.Error(), "already exist") {
		t.Fatalf("error should name the problem: %v", err)
	}
	if h.servers.count() != 1 {
		t.Fatalf("a second server was rented; %d exist", h.servers.count())
	}
}

func TestStartIsRefusedWhileAnotherProcessHoldsTheLock(t *testing.T) {
	h := newHarness(t)

	release, ok, err := h.store.TryLock()
	if err != nil || !ok {
		t.Fatalf("lock: %v ok=%v", err, ok)
	}
	defer release()

	if _, err := h.o.Start(context.Background(), "eu"); err == nil {
		t.Fatal("start should be refused while another process is renting")
	}
	if h.servers.count() != 0 {
		t.Fatal("a server was rented despite the lock")
	}
}

func TestRCONFailureLimitIsConfigurable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.o.cfg.Server.RCONFailureLimit = 2
	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.tick(t) // RUNNING

	h.probeErr = errors.New("timed out")
	h.tick(t)
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("one failed probe should not end the session, state = %s", got)
	}
	h.tick(t)
	if got := h.mustState(t); got != store.Terminating {
		t.Fatalf("state at the limit = %s, want TERMINATING", got)
	}
}

func TestStartFallsBackWhenThePreferredSizeIsSoldOut(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.servers.unavailable = map[string]int{"cx32": 1}
	h.o.cfg.Regions["eu"] = config.Region{
		Location:      "nbg1",
		ServerType:    "cx32",
		HourlyRate:    10,
		FallbackTypes: []string{"cx31"},
	}

	st, err := h.o.Start(ctx, "eu")
	if err != nil {
		t.Fatalf("start should fall back, got %v", err)
	}
	if st.ServerType != "cx31" {
		t.Fatalf("server type = %q, want the fallback", st.ServerType)
	}
	if len(h.servers.created) != 2 || h.servers.created[0] != "cx32" || h.servers.created[1] != "cx31" {
		t.Fatalf("create order = %v", h.servers.created)
	}
	if got := h.mustState(t); got != store.Provisioning {
		t.Fatalf("state = %s", got)
	}
}

func TestStartDoesNotFallBackOnRealErrors(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.servers.createErr = errors.New("invalid token")
	h.o.cfg.Regions["eu"] = config.Region{
		Location: "nbg1", ServerType: "cx32", HourlyRate: 10,
		FallbackTypes: []string{"cx31", "cx21"},
	}

	if _, err := h.o.Start(ctx, "eu"); err == nil {
		t.Fatal("expected the error")
	}
	if len(h.servers.created) != 1 {
		t.Fatalf("kept trying after a real error: %v", h.servers.created)
	}
}

func TestStartReportsEverySizeItTried(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.servers.unavailable = map[string]int{"cx32": 1, "cx31": 1}
	h.o.cfg.Regions["eu"] = config.Region{
		Location: "nbg1", ServerType: "cx32", HourlyRate: 10,
		FallbackTypes: []string{"cx31", "cx21"},
	}

	st, err := h.o.Start(ctx, "eu")
	if err != nil {
		t.Fatalf("third size should succeed: %v", err)
	}
	if st.ServerType != "cx21" {
		t.Fatalf("type = %q", st.ServerType)
	}
	if len(h.servers.created) != 3 {
		t.Fatalf("tried %v", h.servers.created)
	}
}

func TestProbeUsesTheTailnetAddressNotTheName(t *testing.T) {
	h := newHarness(t)
	h.resolveIP = "100.64.0.5"
	if _, err := h.o.Start(context.Background(), "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	h.tick(t)

	// MagicDNS can answer NXDOMAIN for minutes after a node joins; the probe
	// must not depend on it.
	if h.probeAddr != "100.64.0.5:25575" {
		t.Fatalf("probe addr = %q, want the resolved tailnet IP", h.probeAddr)
	}
}

func TestProbeFallsBackToTheNameWhenTheTailnetCannotAnswer(t *testing.T) {
	h := newHarness(t)
	h.resolveErr = errors.New("no tailscale here")

	if _, err := h.o.Start(context.Background(), "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	st, _ := h.store.GetState()
	h.tick(t)

	if want := st.TailnetHost + ":25575"; h.probeAddr != want {
		t.Fatalf("probe addr = %q, want %q", h.probeAddr, want)
	}
	if got := h.mustState(t); got != store.Running {
		t.Fatalf("state = %s, want RUNNING", got)
	}
}

func TestTickSkipsWhileAnotherProcessHoldsTheLock(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.o.Start(ctx, "eu"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := h.o.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Something else (a second `outpost` process) is mid-pass.
	release, ok, err := h.store.TryLock()
	if err != nil || !ok {
		t.Fatalf("lock: %v ok=%v", err, ok)
	}

	if err := h.o.Tick(ctx); err != nil {
		t.Fatalf("tick should be a no-op while locked, got %v", err)
	}
	if h.remote.savedWorld() {
		t.Fatal("a second process saved the world while the first held the lock")
	}
	if got := h.mustState(t); got != store.Terminating {
		t.Fatalf("state = %s, want TERMINATING (untouched)", got)
	}

	release()
	h.tick(t)
	if got := h.mustState(t); got != store.Offline {
		t.Fatalf("state after the lock is free = %s, want OFFLINE", got)
	}
}
