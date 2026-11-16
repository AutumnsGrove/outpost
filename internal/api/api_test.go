package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"os"
	"outpost/internal/config"
	"outpost/internal/hetzner"
	"outpost/internal/orchestrator"
	"outpost/internal/publish"
	"outpost/internal/r2"
	"outpost/internal/roster"
	"outpost/internal/store"
	"outpost/internal/updatesignal"
	"path/filepath"
)

// --- fakes -----------------------------------------------------------------

type noServers struct{ created int }

func (n *noServers) Create(context.Context, hetzner.CreateRequest) (*hetzner.Server, error) {
	n.created++
	s := &hetzner.Server{ID: 7, Name: "outpost-eu-x", Status: "running"}
	s.PublicNet.IPv4.IP = "203.0.113.9"
	return s, nil
}
func (n *noServers) Get(context.Context, string) (*hetzner.Server, error) {
	return nil, hetzner.ErrNotFound
}
func (n *noServers) Delete(context.Context, string) error           { return nil }
func (n *noServers) List(context.Context) ([]hetzner.Server, error) { return nil, nil }

type noDNS struct{}

func (noDNS) SetA(context.Context, string) error { return nil }

type memBlobs struct{ data map[string][]byte }

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
	return nil
}

type fixture struct {
	h       http.Handler
	orch    *orchestrator.Orchestrator
	store   *store.Store
	servers *noServers
	blobs   *memBlobs
	sent    []string
}

func newFixture(t *testing.T, token string) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/outpost.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		Server: config.Server{
			AssetBucket: "mc-assets", WorldBucket: "mc-worlds", MCPort: 25565,
			IdleGrace: config.Duration(10 * time.Minute), IdleTimeout: config.Duration(20 * time.Minute),
			ProvisionTimeout: config.Duration(15 * time.Minute), TickInterval: config.Duration(time.Second),
		},
		Regions:    map[string]config.Region{"eu": {Location: "nbg1", ServerType: "cx23"}},
		Cloudflare: config.Cloudflare{RecordName: "mc.test"},
		Secrets: config.Secrets{
			TailscaleAuthKey: "tskey-test", R2AccessKey: "k", R2SecretKey: "s",
		},
		R2: config.R2{AccountID: "acct"},
	}
	f := &fixture{store: st, servers: &noServers{}, blobs: &memBlobs{data: map[string][]byte{}}}
	orch := orchestrator.NewWith(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), orchestrator.Deps{
		Servers: f.servers,
		DNS:     noDNS{},
		Exec: func(_ context.Context, _, _, cmd string) (string, error) {
			f.sent = append(f.sent, cmd)
			return "ok: " + cmd, nil
		},
		Resolve: func(context.Context, string) (string, error) { return "127.0.0.1", nil },
	})
	rost := roster.New(f.blobs, "mc-assets", func(_ context.Context, name string) (publish.Player, error) {
		return publish.Player{UUID: "11111111-1111-1111-1111-111111111111", Name: name}, nil
	})
	f.orch = orch
	f.h = New(orch, st, Options{Token: token, Roster: rost})
	return f
}

func (f *fixture) setLive(t *testing.T) {
	t.Helper()
	if err := f.store.SetState(store.ServerState{
		State: store.Running, VPSID: "7", VPSIP: "203.0.113.9", TailnetHost: "outpost-eu-x",
		Region: "eu", ServerType: "cx23", RCONPassword: "pw", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) formToken(t *testing.T) string {
	t.Helper()
	rec := f.do("GET", "/api/status", "", nil)
	var out struct {
		FormToken string `json:"formToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.FormToken == "" {
		t.Fatalf("no form token: %v %s", err, rec.Body.String())
	}
	return out.FormToken
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return out
}

// --- the cross-site hole ---------------------------------------------------

// With no bearer token configured, /api/start was open to any web page the
// user visited while on the tailnet: a no-cors POST is a "simple request".
func TestACrossSitePageCannotRentAServer(t *testing.T) {
	f := newFixture(t, "")
	for _, path := range []string{"/api/start", "/api/stop"} {
		rec := f.do("POST", path, "", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %s = %d, want 403", path, rec.Code)
		}
	}
	if f.servers.created != 0 {
		t.Fatal("a cross-site request rented a server")
	}
}

func TestSameSiteSiblingServicesAreAlsoRefused(t *testing.T) {
	f := newFixture(t, "")
	rec := f.do("POST", "/api/start", "", map[string]string{"Sec-Fetch-Site": "same-site"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("same-site POST = %d; another service on this box should not be able to spend money here", rec.Code)
	}
}

func TestOlderBrowsersAreCheckedByOrigin(t *testing.T) {
	f := newFixture(t, "")
	if rec := f.do("POST", "/api/start", "", map[string]string{"Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin = %d, want 403", rec.Code)
	}
	if rec := f.do("POST", "/api/start", "", map[string]string{"Origin": "http://example.com"}); rec.Code == http.StatusForbidden {
		t.Fatal("an Origin matching the request's own host was refused") // httptest.NewRequest uses example.com
	}
}

func TestMachineCallersAndTheOwnPageStillWork(t *testing.T) {
	f := newFixture(t, "")
	for name, h := range map[string]map[string]string{
		"curl (no fetch metadata)": nil,
		"same-origin page":         {"Sec-Fetch-Site": "same-origin"},
		"typed navigation":         {"Sec-Fetch-Site": "none"},
	} {
		rec := f.do("POST", "/api/stop", "", h)
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s was refused", name)
		}
	}
}

func TestPageEndpointsNeedTheFormToken(t *testing.T) {
	f := newFixture(t, "")
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	for _, path := range []string{"/api/action", "/api/console", "/api/players"} {
		rec := f.do("POST", path, `{"action":"start","command":"list","token":"nope"}`, same)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s with a bad token = %d, want 403", path, rec.Code)
		}
	}
	if f.servers.created != 0 {
		t.Fatal("started with a bad token")
	}
}

func TestBearerTokenIsRequiredWhenConfiguredAndAcceptedWhenRight(t *testing.T) {
	f := newFixture(t, "s3cret")
	if rec := f.do("GET", "/api/status", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec := f.do("GET", "/api/status", "", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
	if rec := f.do("GET", "/api/status", "", map[string]string{"Authorization": "Bearer s3cret"}); rec.Code != http.StatusOK {
		t.Fatalf("right token = %d", rec.Code)
	}
}

func TestUnknownAPIPathsAreJSON404NotThePage(t *testing.T) {
	f := newFixture(t, "")
	rec := f.do("GET", "/api/nothing-here", "", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// --- contracts -------------------------------------------------------------

// Meridian's tile reads these fields from /api/status; the page's new data
// lives in /api/overview precisely so this cannot move.
func TestStatusKeepsTheShapeMeridianReads(t *testing.T) {
	f := newFixture(t, "")
	got := decode(t, f.do("GET", "/api/status", "", nil))
	want := []string{"state", "region", "serverType", "serverId", "serverIp", "hostname", "players",
		"uptimeSeconds", "idleSeconds", "ttlSeconds", "startedAt", "hourlyRate", "sessionCost",
		"totalHours", "totalCost", "lastError", "dnsName", "idleGraceSeconds", "idleLimitSeconds", "formToken"}
	if len(got) != len(want) {
		t.Errorf("/api/status has %d fields, want exactly %d: it is a contract", len(got), len(want))
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("/api/status lost %q", k)
		}
	}
}

func TestEventsAndSessionsUseLowercaseKeys(t *testing.T) {
	f := newFixture(t, "")
	_ = f.store.Log("start", "rented something")
	ev := decode(t, f.do("GET", "/api/events", "", nil))["events"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "at", "kind", "message"} {
		if _, ok := ev[k]; !ok {
			t.Errorf("event is missing %q (got %v): the page reads these names", k, ev)
		}
	}
}

func TestOverviewCarriesTheWholePage(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	_ = f.store.MetaSet("online_names", "Autumn,Felix")
	f.blobs.data["server/whitelist.json"] = []byte(`[{"uuid":"u1","name":"Autumn"},{"uuid":"u2","name":"Felix"}]`)
	f.blobs.data["server/ops.json"] = []byte(`[{"uuid":"u1","name":"Autumn","level":4}]`)
	_, _ = f.store.StartSession(time.Now().UTC(), "eu", "cx23", 0.0096, "7")
	_ = f.store.RecordBackup(5994477, 1, "shutdown")

	got := decode(t, f.do("GET", "/api/overview", "", nil))
	for _, k := range []string{"status", "health", "events", "sessions", "totals", "backups", "dns", "players", "build", "canUpdate"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("overview is missing %q", k)
		}
	}
	players := got["players"].(map[string]any)
	if len(players["online"].([]any)) != 2 || len(players["whitelist"].([]any)) != 2 || len(players["ops"].([]any)) != 1 {
		t.Fatalf("players = %v", players)
	}
	if got["status"].(map[string]any)["formToken"] == "" {
		t.Fatal("the page gets its form token from overview.status")
	}
	if got["dns"].(map[string]any)["state"] != "pending" {
		t.Fatalf("dns = %v: a live server whose record has not been set is pending", got["dns"])
	}
}

func TestDNSStateSaysWhetherPeopleCanFindTheServer(t *testing.T) {
	live := store.ServerState{State: store.Running, VPSIP: "1.2.3.4", DNSValue: "1.2.3.4"}
	if got := dnsState("mc.test", live, "1.2.3.4").State; got != "ok" {
		t.Errorf("matching = %q", got)
	}
	live.DNSValue = "9.9.9.9"
	if got := dnsState("mc.test", live, "9.9.9.9").State; got != "pending" {
		t.Errorf("stale record on a live server = %q, want pending", got)
	}
	if got := dnsState("mc.test", store.ServerState{State: store.Offline}, "9.9.9.9").State; got != "idle" {
		t.Errorf("offline with a remembered record = %q, want idle", got)
	}
	if got := dnsState("mc.test", store.ServerState{State: store.Offline}, "").State; got != "unset" {
		t.Errorf("never published = %q, want unset", got)
	}
}

// --- console and players ---------------------------------------------------

func TestConsoleEndpointRunsRefusesAndExplains(t *testing.T) {
	f := newFixture(t, "")
	tok := f.formToken(t)
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	post := func(cmd string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"command": cmd, "token": tok})
		return f.do("POST", "/api/console", string(b), same)
	}

	if rec := post("list"); rec.Code != http.StatusConflict {
		t.Fatalf("with no server = %d, want 409", rec.Code)
	}

	f.setLive(t)
	rec := post("list")
	if rec.Code != 200 || decode(t, rec)["output"] != "ok: list" {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("stop"); rec.Code != http.StatusBadRequest {
		t.Fatalf("stop = %d, want 400", rec.Code)
	}
	if strings.Contains(strings.Join(f.sent, ","), "stop") {
		t.Fatalf("stop reached the server: %v", f.sent)
	}
}

func TestPlayersEndpointPersistsThenAppliesLive(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	tok := f.formToken(t)

	b, _ := json.Marshal(map[string]string{"action": "add", "name": "FelixFurr", "token": tok})
	rec := f.do("POST", "/api/players", string(b), map[string]string{"Sec-Fetch-Site": "same-origin"})
	if rec.Code != 200 {
		t.Fatalf("add = %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(f.blobs.data["server/whitelist.json"], []byte("FelixFurr")) {
		t.Fatal("not written to R2, so it would vanish with the VPS")
	}
	if len(f.sent) != 1 || f.sent[0] != "whitelist add FelixFurr" {
		t.Fatalf("live server told %v", f.sent)
	}

	events, _ := f.store.RecentEvents(10)
	audited := false
	for _, e := range events {
		audited = audited || (e.Kind == "player" && strings.Contains(e.Message, "FelixFurr"))
	}
	if !audited {
		t.Fatal("no audit row for the player change")
	}
}

func TestPlayersEndpointRefusesAnInjectedName(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	tok := f.formToken(t)
	b, _ := json.Marshal(map[string]string{"action": "add", "name": "Felix\nstop", "token": tok})
	rec := f.do("POST", "/api/players", string(b), map[string]string{"Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusBadRequest || len(f.sent) != 0 {
		t.Fatalf("code=%d sent=%v", rec.Code, f.sent)
	}
}

// The page polls this every five seconds from a phone on a tailnet that may be
// relayed, so the response is bounded to what it draws rather than to how much
// history the database holds.
func TestOverviewIsBoundedAndNotCached(t *testing.T) {
	f := newFixture(t, "")
	f.setLive(t)
	for i := 0; i < 40; i++ {
		_ = f.store.Log("console", "command %d", i)
	}
	for i := 0; i < 10; i++ {
		id, _ := f.store.StartSession(time.Now().UTC(), "eu", "cx23", 0.0096, "7")
		_ = f.store.EndSession(id, 60, 0.001, 1)
	}
	for i := 0; i < 8; i++ {
		_ = f.store.RecordBackup(5994477, 1, "shutdown")
	}

	rec := f.do("GET", "/api/overview", "", nil)
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("cache-control = %q; a cached overview can show a server that is gone", cc)
	}

	got := decode(t, rec)
	for name, limit := range map[string]int{
		"events":   overviewEvents,
		"sessions": overviewSessions,
		"backups":  overviewBackups,
	} {
		if n := len(got[name].([]any)); n > limit {
			t.Errorf("%s = %d entries, want at most %d", name, n, limit)
		}
	}
}

// /api/status is the contract Meridian's tile reads; it must not be cacheable
// either, or the tile keeps showing yesterday's state.
func TestAPIResponsesAreNotCached(t *testing.T) {
	f := newFixture(t, "")
	for _, path := range []string{"/api/status", "/api/overview", "/api/events", "/api/sessions"} {
		if cc := f.do("GET", path, "", nil).Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s cache-control = %q", path, cc)
		}
	}
}

// --- the update mailbox ----------------------------------------------------

// withMailbox rebuilds the handler with an update mailbox, the way the daemon has
// one when it is deployed as a container.
func (f *fixture) withMailbox(t *testing.T, dir string) {
	t.Helper()
	rost := roster.New(f.blobs, "mc-assets", func(_ context.Context, name string) (publish.Player, error) {
		return publish.Player{UUID: "11111111-1111-1111-1111-111111111111", Name: name}, nil
	})
	f.h = New(f.orch, f.store, Options{Roster: rost, Updates: updatesignal.Mailbox{Dir: dir}})
}

// The page's Update button writes one file. It does not update anything, and with
// no watcher installed it says so rather than offering a request nobody reads.
func TestUpdateWritesARequestForTheWatcher(t *testing.T) {
	f := newFixture(t, "")
	dir := t.TempDir()
	f.withMailbox(t, dir)

	body := `{"action":"update","token":"` + f.formToken(t) + `"}`
	rec := f.do("POST", "/api/action", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, updatesignal.RequestFile))
	if err != nil {
		t.Fatalf("no request file: %v", err)
	}
	if string(raw) != "latest\n" {
		t.Fatalf("request = %q; the watcher refuses anything but the word 'latest'", raw)
	}
}

func TestUpdateIsRefusedWhileAServerIsRented(t *testing.T) {
	f := newFixture(t, "")
	dir := t.TempDir()
	f.withMailbox(t, dir)
	f.setLive(t)

	body := `{"action":"update","token":"` + f.formToken(t) + `"}`
	rec := f.do("POST", "/api/action", body, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("update while RUNNING = %d, want 409", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, updatesignal.RequestFile)); err == nil {
		t.Fatal("a refused update still asked the watcher to recreate the daemon mid-session")
	}
}

func TestUpdateSaysThereIsNoWatcherWhenThereIsNone(t *testing.T) {
	f := newFixture(t, "")
	body := `{"action":"update","token":"` + f.formToken(t) + `"}`
	rec := f.do("POST", "/api/action", body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update with no mailbox = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "watcher") {
		t.Fatalf("the refusal should name what is missing: %s", rec.Body.String())
	}
}

// The page reads the watcher's last answer from the overview, which is also where
// it asks for a fresh digest when the answer is stale.
func TestOverviewCarriesTheWatchersAnswer(t *testing.T) {
	f := newFixture(t, "")
	dir := t.TempDir()
	f.withMailbox(t, dir)
	stale := `{"current":"aaaaaaaaaaaa","latest":"bbbbbbbbbbbb","latestDigest":"sha256:bbbb","checkedAt":"2020-01-01T00:00:00Z","updateAvailable":true}`
	if err := os.WriteFile(filepath.Join(dir, updatesignal.AvailableLog), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, updatesignal.ResultFile), []byte(`{"status":"ok","detail":"updated to bbbbbbbbbbbb"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := decode(t, f.do("GET", "/api/overview", "", nil))
	if got["canUpdate"] != true {
		t.Fatal("a configured mailbox should offer the button")
	}
	upd, ok := got["update"].(map[string]any)
	if !ok {
		t.Fatalf("overview has no update section: %v", got["update"])
	}
	if upd["available"].(map[string]any)["latest"] != "bbbbbbbbbbbb" {
		t.Fatalf("available = %v", upd["available"])
	}
	if upd["result"].(map[string]any)["status"] != "ok" {
		t.Fatalf("result = %v", upd["result"])
	}
	// The stale answer means the page asked for a fresh one.
	if _, err := os.Stat(filepath.Join(dir, updatesignal.CheckFile)); err != nil {
		t.Fatal("a stale answer did not ask the watcher to look again")
	}
}
