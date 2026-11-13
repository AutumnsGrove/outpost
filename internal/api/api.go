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
	"time"

	"outpost/internal/orchestrator"
	"outpost/internal/roster"
	"outpost/internal/store"
	"outpost/internal/updater"
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

	// update is nil when the binary cannot find its own checkout (a dev build
	// in /tmp, say), which hides the Update button rather than offering a
	// button that fails.
	update *updater.Update
	// restart replaces the process after an update. See Options.Restart.
	restart func()
	// updating makes the update single-flight: a double tap on the button must
	// not run two `git pull`s and two builds over the same outpost.new.
	updating sync.Mutex

	roster *roster.Roster
	cache  rosterCache
}

// Options is what the API needs that the orchestrator does not provide.
type Options struct {
	// Token is the optional bearer token for /api/*. Empty means the listener's
	// loopback binding and tailscale serve are the whole boundary.
	Token string
	// Update enables the page's Update button.
	Update *updater.Update
	// Restart, when set, is how the daemon replaces itself after a page-driven
	// update. Under systemd this should simply end the process and let
	// Restart=always bring the new binary up: the unit runs with NoNewPrivileges,
	// which makes `sudo systemctl restart` from inside it fail.
	Restart func()
	// Roster enables the player list and its controls. Nil hides them.
	Roster *roster.Roster
	Log    *slog.Logger
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
		update:    opts.Update,
		restart:   opts.Restart,
		roster:    opts.Roster,
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
	return s.crossSiteGuard(mux)
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
	}
	_ = json.Unmarshal(raw, &body)

	ctx := r.Context()
	switch body.Action {
	case "start":
		st, err := s.orch.Start(ctx, s.orch.DefaultRegion())
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

func (s *Server) updateFromPage(ctx context.Context, w http.ResponseWriter) {
	if s.update == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "this binary cannot find its own git checkout, so it cannot update itself",
		})
		return
	}
	if !s.updating.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is already running"})
		return
	}
	defer s.updating.Unlock()

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

	res, err := s.update.PullAndBuild(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !res.Changed {
		writeJSON(w, http.StatusOK, map[string]string{
			"message": "already up to date at " + res.After,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "updated " + res.Before + " → " + res.After + ", restarting",
	})

	// Answer the browser before the process goes away, or the page shows a failed
	// request for an update that actually worked.
	u := *s.update
	go func() {
		time.Sleep(300 * time.Millisecond)
		if s.restart != nil {
			s.restart()
			return
		}
		if err := u.Restart(context.WithoutCancel(ctx)); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Error("restart after update failed", "err", err)
		}
	}()
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
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Region == "" {
		body.Region = s.orch.DefaultRegion()
	}

	st, err := s.orch.Start(r.Context(), body.Region)
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
