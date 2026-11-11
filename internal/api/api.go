// Package api is the small JSON surface the dashboard and CLI talk to.
//
// It is bound to loopback and published with `tailscale serve`, so access
// control is the tailnet. The bearer token is a second lock for the case where
// someone forwards the port by accident.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"outpost/internal/orchestrator"
	"outpost/internal/store"
)

// Server is the HTTP handler set.
type Server struct {
	orch  *orchestrator.Orchestrator
	store *store.Store
	token string
	log   *slog.Logger
}

// New returns the handler.
func New(orch *orchestrator.Orchestrator, st *store.Store, token string, log *slog.Logger) http.Handler {
	s := &Server{orch: orch, store: st, token: token, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/status", s.auth(s.status))
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

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.orch.Status(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Region string `json:"region"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Region == "" {
		body.Region = "eu"
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
