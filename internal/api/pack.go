package api

// The pack panel: which profiles exist, what a link contains, and the two
// actions that take minutes. All of it lives here rather than in the handlers
// because the refusal rules are the interesting part — nothing while a server is
// rented, one job at a time — and they should be readable in one place.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"outpost/internal/pack"
	"outpost/internal/publish"
	"outpost/internal/store"
	"outpost/internal/world"
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
	// SupportedMC is the Minecraft version this image can boot, so the panel can
	// offer a vanilla profile without hardcoding a version number in the page.
	SupportedMC string `json:"supportedMc"`
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
	out.SupportedMC = publish.SupportedMC
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

// packVanilla publishes a server with no mods: a pack.json that says source
// "outpost", an empty mods/, and the launcher fetched for 1.20.1. The panel's
// "Vanilla 1.20.1" button is the only caller, and it is a job because the
// launcher download and the upload take as long as any other install.
func (s *Server) packVanilla(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body pack.VanillaRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
			return
		}
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

	job, err := s.packs.InstallVanilla(r.Context(), body)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	_ = s.store.Log("pack", "installing vanilla into profile %s", job.Profile)
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

// packWorldDelete moves one profile's world to the trash.
//
// Inline rather than a job: it is a server-side copy and two key operations, and
// a page that has to poll for a delete is a page that makes deleting feel bigger
// than it is. The world is recoverable for two weeks either way.
func (s *Server) packWorldDelete(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	if err := s.offlineOnly(w); err != nil {
		return
	}

	res, err := world.Delete(r.Context(), s.packs.Store, world.DeleteOptions{
		WorldBucket: s.packs.WorldBucket,
		Profile:     body.Profile,
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if res.Missing {
		writeJSON(w, http.StatusOK, map[string]string{"message": body.Profile + " had no world saved"})
		return
	}
	_ = s.store.Log("pack", "world of profile %s trashed to %s (%d bytes, by page)", body.Profile, res.Trash, res.Bytes)
	writeJSON(w, http.StatusOK, map[string]string{
		"message": fmt.Sprintf("world of %s (%s) moved to %s — recoverable until %s",
			body.Profile, humanBytes(res.Bytes), res.Trash, res.Expires.Format("2 Jan 15:04")),
	})
}

// packDelete removes a whole profile. It refuses the active one: the caller knows
// which that is, and deleting what Start boots is a confusing way to find out.
func (s *Server) packDelete(w http.ResponseWriter, r *http.Request, raw []byte) {
	var body struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	if err := s.offlineOnly(w); err != nil {
		return
	}

	active, err := s.orch.ActiveProfile()
	if err != nil {
		s.fail(w, err)
		return
	}
	if body.Profile == active {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "wrong? " + body.Profile + " is the profile Start boots; switch to another one first, or delete just its world",
		})
		return
	}

	res, err := s.packs.DeleteProfile(r.Context(), body.Profile, active, nil)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	_ = s.store.Log("pack", "profile %s deleted (by page): %d asset key(s), world trashed to %s", body.Profile, res.Assets, res.World.Trash)

	msg := "profile " + body.Profile + " is gone"
	if res.Trashed {
		msg += fmt.Sprintf("; its world (%s) is in %s until %s",
			humanBytes(res.World.Bytes), res.World.Trash, res.World.Expires.Format("2 Jan 15:04"))
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// offlineOnly is the guard every destructive pack action shares.
func (s *Server) offlineOnly(w http.ResponseWriter) error {
	st, err := s.store.GetState()
	if err != nil {
		s.fail(w, err)
		return err
	}
	if st.State != store.Offline {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the server is " + string(st.State) + "; save and stop it first — a live session uploads over its own profile's world",
		})
		return errors.New("not offline")
	}
	return nil
}

// humanBytes is a one-decimal size for a sentence. publish has the CLI's version;
// this one is for messages and does not need to agree to the byte.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(n)
	for _, u := range units {
		size /= unit
		if size < unit {
			return fmt.Sprintf("%.1f %s", size, u)
		}
	}
	return fmt.Sprintf("%.1f PB", size)
}
