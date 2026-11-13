// Package api is the small JSON surface the dashboard and CLI talk to.
//
// It is bound to loopback and published with `tailscale serve`, so access
// control is the tailnet. The bearer token is a second lock for the case where
// someone forwards the port by accident.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"outpost/internal/orchestrator"
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
}

// Options is what the API needs that the orchestrator does not provide.
type Options struct {
	// Token is the optional bearer token for /api/*. Empty means the listener's
	// loopback binding and tailscale serve are the whole boundary.
	Token string
	// Update enables the page's Update button.
	Update *updater.Update
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
	}

	mux := http.NewServeMux()
	// The page, and the catch-all: anything not matched by a more specific
	// pattern is a client-side route, so the SPA shell answers it. ("/" is
	// Go's prefix catch-all; "GET /{$}" would match only the root and 404
	// every deep link.)
	mux.Handle("/", spaHandler())

	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/status", s.auth(s.status))
	mux.HandleFunc("POST /api/action", s.action)
	// Machine callers (Meridian's control plane, curl, cron) keep the bearer
	// token path; the page uses /api/action with a form token instead.
	mux.HandleFunc("POST /api/start", s.auth(s.start))
	mux.HandleFunc("POST /api/stop", s.auth(s.stop))
	mux.HandleFunc("GET /api/sessions", s.auth(s.sessions))
	mux.HandleFunc("GET /api/events", s.auth(s.events))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			header := r.Header.Get("Authorization")
			got, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || got != s.token {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("outpost ok\n"))
}

// statusResponse is Status plus the button token. Embedding keeps every field
// at the top level, which is what the Meridian tile's vitals read.
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
func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Token  string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	if !checkToken(body.Token, s.formToken) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "stale page: reload and try again"})
		return
	}

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
		s.updateFromPage(ctx, w, r)

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
	}
}

func (s *Server) updateFromPage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "this binary cannot find its own git checkout, so it cannot update itself",
		})
		return
	}

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

	// Answer the browser before systemd kills this process, or the page shows a
	// failed request for an update that actually worked.
	u := *s.update
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := u.Restart(context.WithoutCancel(ctx)); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Error("restart after update failed", "err", err)
		}
	}()
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
		"sessions": sessions,
		"totals": map[string]any{
			"hours":    hours,
			"costUsd":  cost,
			"sessions": count,
		},
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RecentEvents(100)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
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
