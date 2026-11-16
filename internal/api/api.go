// Package api is the small JSON surface the dashboard and CLI talk to.
//
// It is bound to loopback and published with `tailscale serve`, so access
// control is the tailnet. The bearer token is a second lock for the case where
// someone forwards the port by accident.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"outpost/internal/orchestrator"
	"outpost/internal/pack"
	"outpost/internal/roster"
	"outpost/internal/store"
	"outpost/internal/updatesignal"
)

// Server is the HTTP handler set.
type Server struct {
	orch  *orchestrator.Orchestrator
	store *store.Store
	token string
	log   *slog.Logger

	// formToken guards the buttons on the page. It is not authentication — the
	// tailnet is — it stops a page somewhere else on the internet from
	// submitting a form that rents a server. Issued per process, never stored.
	formToken string

	// updates is the mailbox shared with the host update watcher. A zero one means
	// no watcher is installed (a binary built from a checkout and run by hand),
	// which hides the Update button rather than offering a request nobody reads.
	updates updatesignal.Mailbox
	// updating makes the update single-flight: a double tap on the button must not
	// write two requests.
	updating sync.Mutex

	roster *roster.Roster
	cache  rosterCache

	// packs drives the modpack panel. Nil when the daemon has no R2 client, which
	// hides the panel rather than offering buttons that fail.
	packs *pack.Manager
}

// Options is what the API needs that the orchestrator does not provide.
type Options struct {
	// Token is the optional bearer token for /api/*. Empty means the listener's
	// loopback binding and tailscale serve are the whole boundary.
	Token string
	// Updates is the mailbox the host update watcher watches. The daemon does not
	// update itself and cannot: it is a container with a read-only filesystem and no
	// docker socket, and the thing that pulls by digest is a host script with the
	// privileges that needs (PLAN 38). Empty means no watcher is installed.
	Updates updatesignal.Mailbox
	// Roster enables the player list and its controls. Nil hides them.
	Roster *roster.Roster
	// Packs enables the modpack panel: profiles, resolving links and installing
	// them. Nil hides it.
	Packs *pack.Manager
	Log   *slog.Logger
}

// New returns the handler.
func New(orch *orchestrator.Orchestrator, st *store.Store, opts Options) http.Handler {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		orch:      orch,
		store:     st,
		token:     opts.Token,
		log:       log,
		formToken: newFormToken(),
		updates:   opts.Updates,
		roster:    opts.Roster,
		packs:     opts.Packs,
	}

	mux := http.NewServeMux()
	// The page, and the catch-all: anything not matched by a more specific
	// pattern is a client-side route, so the SPA shell answers it. ("/" is
	// Go's prefix catch-all; "GET /{$}" would match only the root and 404
	// every deep link.)
	mux.Handle("/", spaHandler())

	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/status", s.auth(s.status))
	mux.HandleFunc("GET /api/overview", s.auth(s.overview))
	mux.HandleFunc("POST /api/action", s.page(s.action))
	mux.HandleFunc("POST /api/console", s.page(s.console))
	mux.HandleFunc("POST /api/players", s.page(s.players))
	mux.HandleFunc("GET /api/pack", s.auth(s.packGet))
	mux.HandleFunc("POST /api/pack/versions", s.packHandler(s.packVersions))
	mux.HandleFunc("POST /api/pack/preview", s.packHandler(s.packPreview))
	mux.HandleFunc("POST /api/pack/install", s.packHandler(s.packInstall))
	mux.HandleFunc("POST /api/pack/vanilla", s.packHandler(s.packVanilla))
	mux.HandleFunc("POST /api/pack/use", s.packHandler(s.packUse))
	mux.HandleFunc("POST /api/pack/delete", s.packHandler(s.packDelete))
	mux.HandleFunc("POST /api/pack/world/delete", s.packHandler(s.packWorldDelete))
	// Machine callers (Meridian's control plane, curl, cron) keep the bearer
	// token path; the page uses /api/action with a form token instead.
	mux.HandleFunc("POST /api/start", s.auth(s.start))
	mux.HandleFunc("POST /api/stop", s.auth(s.stop))
	mux.HandleFunc("GET /api/sessions", s.auth(s.sessions))
	mux.HandleFunc("GET /api/events", s.auth(s.events))
	// An unknown /api path is a JSON 404, not the page's HTML with a 200: a
	// client talking to an older daemon should see "not found", not a parse error.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such endpoint"})
	})
	return s.crossSiteGuard(noStore(mux))
}

// noStore marks every API response uncacheable.
//
// This is a live view of a machine that bills by the hour: a browser serving a
// back-navigation out of its cache, or an intermediary replaying this morning's
// "RUNNING" onto a screen where the server is gone, is worse than a slow page.
// The SPA handler sets its own cache headers, so only /api/ is touched.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// crossSiteGuard refuses state-changing requests that a browser says came from
// another origin.
//
// The form token protects the page's own endpoints, but /api/start and
// /api/stop are the machine path and are open when no bearer token is
// configured — so without this, any web page the user visits while on the
// tailnet could `fetch('/api/start', {method: 'POST', mode: 'no-cors'})` and
// rent a server. Browsers attach Sec-Fetch-Site to every request and pages
// cannot forge it; curl and Meridian send none and are unaffected.
//
// Only same-origin (and "none", a user-typed navigation) pass. same-site is
// refused too: another service on this box shares the registrable domain, and
// it has no business spending money here.
func (s *Server) crossSiteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
				if site != "same-origin" && site != "none" {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
					return
				}
			} else if origin := r.Header.Get("Origin"); origin != "" && !originMatches(origin, r) {
				// Older browsers: fall back to comparing Origin with the host the
				// request was addressed to (or the one a proxy says it was).
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func originMatches(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host || u.Host == r.Header.Get("X-Forwarded-Host")
}

// bearerOK reports whether the request carries the configured bearer token. The
// comparison is constant-time: it costs nothing and removes a question.
func (s *Server) bearerOK(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && !s.bearerOK(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// pageBody is what every page-driven POST carries besides its own fields.
type pageBody struct {
	Token string `json:"token"`
}

// page wraps the handlers behind the form token. The body is decoded once, here,
// so each handler sees the same struct it asked for. A caller holding the bearer
// token (curl, Meridian) may skip the form token.
func (s *Server) page(next func(w http.ResponseWriter, r *http.Request, raw []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&raw); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
			return
		}
		var pb pageBody
		_ = json.Unmarshal(raw, &pb)
		if !s.bearerOK(r) && !checkToken(pb.Token, s.formToken) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "stale page: reload and try again"})
			return
		}
		next(w, r, raw)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("outpost ok\n"))
}

// statusResponse is Status plus the button token. Embedding keeps every field
// at the top level, which is what the Meridian tile's vitals read. The shape of
// /api/status is a contract: add new things to /api/overview instead.
type statusResponse struct {
	orchestrator.Status
	FormToken string `json:"formToken"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.orch.Status(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: st, FormToken: s.formToken})
}

// action is the page's buttons: start, stop, update.
func (s *Server) action(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body struct {
		Action string `json:"action"`
		// Type is the plan the Start button rents. It is remembered, so the next
		// start — from the page or the CLI — rents the same one.
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &body)

	ctx := r.Context()
	switch body.Action {
	case "start":
		st, err := s.orch.Start(ctx, s.orch.DefaultRegion(), body.Type)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"message": "renting " + st.TailnetHost + " — about four minutes to come up",
		})

	case "stop":
		if err := s.orch.Stop(ctx); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"message": "saving the world and deleting the server",
		})

	case "update":
		s.updateFromPage(ctx, w)

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
	}
}

// updateFromPage asks the host watcher for the newest image.
//
// It does not update anything: the daemon cannot pull an image, and the whole
// point of the split is that the process being replaced is not the process doing
// the replacing. So this writes one file — the request — and says what it did. The
// answer arrives later, in the mailbox's `result`, which the page reads on its next
// poll (and which survives the container being recreated, because it is a volume).
func (s *Server) updateFromPage(ctx context.Context, w http.ResponseWriter) {
	if !s.updates.Configured() {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no update watcher is installed on this host, so there is nobody to ask; deploy with deploy/install.sh and the update comes with it",
		})
		return
	}
	if !s.updating.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is already being requested"})
		return
	}
	defer s.updating.Unlock()

	// The state check is the same one the old in-process updater made, and it
	// matters more now: recreating the container while a VPS is rented would kill
	// the loop that saves the world.
	st, err := s.orch.Status(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if st.State != string(store.Offline) && st.State != string(store.Error) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "a server is " + st.State + "; save and stop it before updating",
		})
		return
	}

	if err := s.updates.Request(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("asked the update watcher for the newest image", "mailbox", s.updates.Dir)

	message := "asked the update watcher for the newest image; the daemon restarts when it is ready"
	if st := mailboxStatus(s.updates); st.Available != nil && !st.Available.UpdateAvailable {
		message = "asked the update watcher; it last saw you already on the newest image, so this may come back as nothing to do"
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": message})
}

// mailboxStatus is Status with errors flattened: a mailbox that cannot be read is
// "no answer", which only affects the wording of a message.
func mailboxStatus(m updatesignal.Mailbox) updatesignal.Status {
	st, err := m.Status()
	if err != nil {
		return updatesignal.Status{}
	}
	return st
}

// console sends one command to the running server.
func (s *Server) console(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(raw, &body)

	who := "page"
	if s.bearerOK(r) {
		who = "api"
	}
	res, err := s.orch.Console(r.Context(), who, body.Command)
	if err != nil {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, orchestrator.ErrRefused):
			code = http.StatusBadRequest
		case errors.Is(err, orchestrator.ErrNotRunning):
			code = http.StatusConflict
		case strings.Contains(err.Error(), "rcon unreachable"):
			code = http.StatusBadGateway
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// players adds, removes and ops players, persistently.
func (s *Server) players(w http.ResponseWriter, r *http.Request, raw []byte) {
	if s.roster == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "the player list needs R2 credentials, which this daemon was started without"})
		return
	}
	var body struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	_ = json.Unmarshal(raw, &body)

	who := "page"
	if s.bearerOK(r) {
		who = "api"
	}
	live := s.orch.Live()
	res, err := s.roster.Apply(r.Context(), roster.Action(body.Action), body.Name, live,
		func(ctx context.Context, cmd string) error {
			_, err := s.orch.Console(ctx, who, cmd)
			return err
		})
	s.cache.invalidate()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_ = s.store.Log("player", "%s %s (by %s)", body.Action, res.Name, who)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Region string `json:"region"`
		// Type is the plan to rent. Empty takes the remembered choice, then the
		// region's default, so a caller that does not care is unaffected.
		Type string `json:"type"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Region == "" {
		body.Region = s.orch.DefaultRegion()
	}

	st, err := s.orch.Start(r.Context(), body.Region, body.Type)
	if err != nil {
		s.log.Error("start failed", "err", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"state":    string(st.State),
		"hostname": st.TailnetHost,
		"serverId": st.VPSID,
	})
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	if err := s.orch.Stop(r.Context()); err != nil {
		s.log.Error("stop failed", "err", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"state": string(store.Terminating)})
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.RecentSessions(50)
	if err != nil {
		s.fail(w, err)
		return
	}
	hours, cost, count, err := s.store.Totals()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": sessionDTOs(sessions),
		"totals":   totalsDTO{Hours: hours, Cost: cost, Sessions: count},
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RecentEvents(100)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": eventDTOs(events)})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.log.Error("api error", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
