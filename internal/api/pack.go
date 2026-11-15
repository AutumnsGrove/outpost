package api

// The pack panel: which profiles exist, what a link contains, and the two
// actions that take minutes. All of it lives here rather than in the handlers
// because the refusal rules are the interesting part — nothing while a server is
// rented, one job at a time — and they should be readable in one place.

import (
	"encoding/json"
	"net/http"

	"outpost/internal/pack"
	"outpost/internal/store"
)

// packOverview is what GET /api/pack answers.
type packOverview struct {
	// Active is the profile the next `start` boots.
	Active   string         `json:"active"`
	Profiles []pack.Profile `json:"profiles"`
	// Job is the install or preview in flight, if any. The page polls while it sees
	// one.
	Job *pack.Job `json:"job,omitempty"`
	// Editable is false while a server is rented: installing a pack or switching
	// profiles underneath a live server would file its world in the wrong drawer.
	Editable bool   `json:"editable"`
	State    string `json:"state"`
	// Detail explains why the panel is read-only, when it is.
	Detail string `json:"detail,omitempty"`
}

// packHandler is the shared guard: the panel needs R2, which this daemon may have
// been started without.
func (s *Server) packHandler(next func(w http.ResponseWriter, r *http.Request, raw []byte)) http.HandlerFunc {
	wrapped := func(w http.ResponseWriter, r *http.Request, raw []byte) {
		if s.packs == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "the modpack panel needs R2 credentials, which this daemon was started without"})
			return
		}
		next(w, r, raw)
	}
	return s.page(wrapped)
}

func (s *Server) packGet(w http.ResponseWriter, r *http.Request) {
	out := packOverview{State: string(store.Offline)}

	active, err := s.orch.ActiveProfile()
	if err != nil {
		s.fail(w, err)
		return
	}
	out.Active = active

	st, err := s.store.GetState()
	if err != nil {
		s.fail(w, err)
		return
	}
	out.State = string(st.State)
	out.Editable = st.State == store.Offline
	if !out.Editable {
		out.Detail = "the server is " + string(st.State) + ": installing or switching a pack now would file its world in the wrong profile"
	}

	if s.packs != nil {
		profiles, err := s.packs.Profiles(r.Context(), active)
		if err != nil {
			s.fail(w, err)
			return
		}
		out.Profiles = profiles
		out.Job = s.packs.Job()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) packPreview(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body pack.PreviewRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}

	job, err := s.packs.Preview(r.Context(), body)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// packVersions lists a pack's versions so the panel can offer them. One API call
// and no download, so unlike preview and install it answers inline.
func (s *Server) packVersions(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body struct {
		Source string `json:"source"`
		// Profile is the row the list is about; empty derives it from the source.
		// A profile published as --profile potato is not named after its pack, so
		// deriving would answer about a different profile.
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	versions, err := s.packs.Versions(r.Context(), body.Source, body.Profile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, versions)
}

func (s *Server) packInstall(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body pack.InstallRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}

	st, err := s.store.GetState()
	if err != nil {
		s.fail(w, err)
		return
	}
	if st.State != store.Offline {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the server is " + string(st.State) + "; save and stop it before installing a pack",
		})
		return
	}

	job, err := s.packs.Install(r.Context(), body)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	_ = s.store.Log("pack", "installing %s into profile %s", body.Source, job.Profile)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) packUse(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}

	if err := s.orch.SetActiveProfile(body.Profile); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	_ = s.store.Log("pack", "profile %s is now the active one (by page)", body.Profile)
	writeJSON(w, http.StatusOK, map[string]string{"active": body.Profile})
}
