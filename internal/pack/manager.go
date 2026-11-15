// The manager is the daemon's half of the pack panel: which profiles exist, what
// is in each one, and the two long actions — resolving a link and installing it.
//
// Both actions move hundreds of megabytes and take minutes, so they run on their
// own goroutine and the page polls for progress. One at a time: two installs
// writing into the same profile is not a merge anybody wants.
package pack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"outpost/internal/config"
	"outpost/internal/publish"
)

// ErrBusy is returned when an install or a preview is already running.
var ErrBusy = errors.New("another pack job is already running")

// Manager does the pack work the page asks for.
type Manager struct {
	AssetBucket string
	WorldBucket string

	// Dir is where pack directories live, one per profile, with the .mrpack cache
	// beside them. Re-installing a profile reuses both.
	Dir string

	// Store is R2. Manager never touches SQLite: the active profile is the
	// orchestrator's business.
	Store publish.Store

	// Regions are the plans a session can be rented on, so the RAM guard runs at
	// install time rather than at start time.
	Regions map[string]config.Region

	Log *slog.Logger

	// Whitelist returns the names on the server-wide player list. An install needs
	// one, and the page already has it.
	Whitelist func(ctx context.Context) ([]string, error)

	// Activate switches the active profile after a successful install.
	Activate func(ctx context.Context, profile string) error

	mu  sync.Mutex
	job *Job
}

// Profile is one pack+world pair as the page sees it.
type Profile struct {
	ID       string        `json:"id"`
	Active   bool          `json:"active"`
	Pack     *publish.Pack `json:"pack,omitempty"`
	Mods     int           `json:"mods"`
	Bytes    int64         `json:"bytes"`
	HasWorld bool          `json:"hasWorld"`
	// WorldPack is the pack the saved world was made by, when the marker says so.
	WorldPack *publish.Pack `json:"worldPack,omitempty"`
}

// Job is one preview or install in flight.
type Job struct {
	Kind      string     `json:"kind"` // preview | install
	Phase     string     `json:"phase"`
	Source    string     `json:"source"`
	Profile   string     `json:"profile"`
	Done      int        `json:"done"`
	Total     int        `json:"total"`
	Error     string     `json:"error,omitempty"`
	StartedAt string     `json:"startedAt"`
	EndedAt   string     `json:"endedAt,omitempty"`
	Result    *JobResult `json:"result,omitempty"`

	running bool
}

// JobResult is what a finished job has to say.
type JobResult struct {
	Pack       publish.Pack  `json:"pack"`
	Profile    string        `json:"profile"`
	Mods       []PlannedFile `json:"mods"`
	Files      int           `json:"files"`
	Bytes      int64         `json:"bytes"`
	ClientOnly []string      `json:"clientOnly,omitempty"`
	Warnings   []string      `json:"warnings,omitempty"`
	// WorldPack is what made the profile's saved world, when it is not the pack
	// being installed.
	WorldPack *publish.Pack `json:"worldPack,omitempty"`
}

// PreviewRequest is "tell me what is in this link".
type PreviewRequest struct {
	Source       string `json:"source"`
	Profile      string `json:"profile"`
	MemoryGB     int    `json:"memoryGB"`
	SkipOptional bool   `json:"skipOptional"`
}

// InstallRequest is "make this the profile".
type InstallRequest struct {
	Source       string `json:"source"`
	Profile      string `json:"profile"`
	MemoryGB     int    `json:"memoryGB"`
	SkipOptional bool   `json:"skipOptional"`
	NewWorld     bool   `json:"newWorld"`
	KeepWorld    bool   `json:"keepWorld"`
	Activate     bool   `json:"activate"`
}

// Profiles lists what is installed in both buckets, with the active one marked.
func (m *Manager) Profiles(ctx context.Context, active string) ([]Profile, error) {
	ids := map[string]bool{}
	for _, bucket := range []string{m.AssetBucket, m.WorldBucket} {
		objs, err := m.Store.List(ctx, bucket, "profiles/")
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			rest := strings.TrimPrefix(o.Key, "profiles/")
			if id, _, ok := strings.Cut(rest, "/"); ok && id != "" {
				ids[id] = true
			}
		}
	}

	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)

	out := make([]Profile, 0, len(names))
	for _, id := range names {
		p := Profile{ID: id, Active: id == active}
		if raw, err := m.Store.Get(ctx, m.AssetBucket, "profiles/"+id+"/pack.json"); err == nil {
			var pk publish.Pack
			if json.Unmarshal(raw, &pk) == nil {
				p.Pack = &pk
			}
		}
		if mods, err := m.Store.List(ctx, m.AssetBucket, "profiles/"+id+"/mods/"); err == nil {
			p.Mods = len(mods)
			for _, o := range mods {
				p.Bytes += o.Size
			}
		}
		if _, err := m.Store.Stat(ctx, m.WorldBucket, "profiles/"+id+"/world.tar.gz"); err == nil {
			p.HasWorld = true
			if raw, err := m.Store.Get(ctx, m.WorldBucket, "profiles/"+id+"/pack.json"); err == nil {
				var wp publish.Pack
				if json.Unmarshal(raw, &wp) == nil {
					p.WorldPack = &wp
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// Job returns a copy of the current job, or nil.
func (m *Manager) Job() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	cp := *m.job
	if cp.Result != nil {
		res := *cp.Result
		cp.Result = &res
	}
	return &cp
}

// Preview resolves a link and reports what it contains. Nothing is uploaded and
// no mod files are downloaded: only the .mrpack, which is cached for the install
// that usually follows.
func (m *Manager) Preview(ctx context.Context, req PreviewRequest) (*Job, error) {
	profile, err := m.profileFor(req.Source, req.Profile)
	if err != nil {
		return nil, err
	}
	job, err := m.begin("preview", req.Source, profile)
	if err != nil {
		return nil, err
	}

	opts := Options{
		Source:       req.Source,
		Dir:          m.profileDir(profile),
		MemoryGB:     req.MemoryGB,
		SkipOptional: req.SkipOptional,
		PlanOnly:     true,
		CacheDir:     m.cacheDir(),
		Log:          m.Log,
		Progress:     m.progress(job),
	}
	m.run(ctx, job, func(ctx context.Context) (*JobResult, error) {
		res, err := Fetch(ctx, opts)
		if err != nil {
			return nil, err
		}
		// KeepWorld only silences the guard: nothing is uploaded, and the answer to
		// "is this a different pack" is reported, not acted on.
		pub := m.publishOptions(profile)
		pub.KeepWorld = true
		pf, err := publish.InspectPack(ctx, m.Store, pub, res.Pack)
		if err != nil {
			return nil, err
		}
		result := &JobResult{
			Pack:       res.Pack,
			Profile:    profile,
			Files:      len(res.Planned),
			Bytes:      res.Bytes,
			ClientOnly: res.Skipped,
			Warnings:   append(res.Warnings, pf.Warnings...),
			WorldPack:  pf.WorldPack,
		}
		for _, f := range res.Planned {
			if strings.HasPrefix(f.Path, "mods/") {
				result.Mods = append(result.Mods, f)
			}
		}
		return result, nil
	})
	return job, nil
}

// Install fetches a pack into a profile and publishes it to R2.
func (m *Manager) Install(ctx context.Context, req InstallRequest) (*Job, error) {
	profile, err := m.profileFor(req.Source, req.Profile)
	if err != nil {
		return nil, err
	}
	if req.NewWorld && req.KeepWorld {
		return nil, fmt.Errorf("newWorld and keepWorld ask for opposite things")
	}
	job, err := m.begin("install", req.Source, profile)
	if err != nil {
		return nil, err
	}

	opts := Options{
		Source:       req.Source,
		Dir:          m.profileDir(profile),
		MemoryGB:     req.MemoryGB,
		SkipOptional: req.SkipOptional,
		Force:        true,
		CacheDir:     m.cacheDir(),
		Log:          m.Log,
		Progress:     m.progress(job),
	}

	m.run(ctx, job, func(ctx context.Context) (*JobResult, error) {
		res, err := Fetch(ctx, opts)
		if err != nil {
			return nil, err
		}

		pub := m.publishOptions(profile)
		pub.Dir = opts.Dir
		pub.NewWorld = req.NewWorld
		pub.KeepWorld = req.KeepWorld
		pub.Progress = m.uploadProgress(job)
		if m.Whitelist != nil {
			names, err := m.Whitelist(ctx)
			if err != nil {
				return nil, err
			}
			if len(names) == 0 {
				return nil, fmt.Errorf("the server-wide player list is empty; add someone on the page first, or the server would refuse everybody")
			}
			pub.Whitelist = names
		}

		uploaded, err := publish.Run(ctx, m.Store, pub)
		if err != nil {
			return nil, err
		}

		if req.Activate && m.Activate != nil {
			if err := m.Activate(ctx, profile); err != nil {
				return nil, err
			}
		}

		result := &JobResult{
			Pack:     res.Pack,
			Profile:  profile,
			Files:    res.Files,
			Bytes:    res.Bytes,
			Warnings: append(res.Warnings, uploaded.Warnings...),
		}
		for _, f := range res.Planned {
			if strings.HasPrefix(f.Path, "mods/") {
				result.Mods = append(result.Mods, f)
			}
		}
		return result, nil
	})
	return job, nil
}

// publishOptions is the part of publish's options that comes from the manager.
func (m *Manager) publishOptions(profile string) publish.Options {
	return publish.Options{
		AssetBucket: m.AssetBucket,
		WorldBucket: m.WorldBucket,
		Profile:     profile,
		Regions:     m.Regions,
		Log:         m.Log,
	}
}

// profileFor is the profile a request lands in: the name it gave, or one derived
// from the link (the Modrinth slug), which is the whole "paste and go" promise.
func (m *Manager) profileFor(source, profile string) (string, error) {
	if strings.TrimSpace(profile) == "" {
		derived, err := SourceProfile(source)
		if err != nil {
			return "", err
		}
		profile = derived
	}
	if err := publish.ValidateProfile(profile); err != nil {
		return "", err
	}
	return profile, nil
}

func (m *Manager) profileDir(profile string) string { return filepath.Join(m.Dir, profile) }
func (m *Manager) cacheDir() string                 { return filepath.Join(m.Dir, "_cache") }

// begin claims the single job slot.
func (m *Manager) begin(kind, source, profile string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && m.job.running {
		return nil, ErrBusy
	}
	job := &Job{
		Kind:      kind,
		Phase:     "starting",
		Source:    source,
		Profile:   profile,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		running:   true,
	}
	m.job = job
	return job, nil
}

// run executes the work and records how it ended.
func (m *Manager) run(ctx context.Context, job *Job, fn func(ctx context.Context) (*JobResult, error)) {
	go func() {
		// The request that asked for this may go away; the install does not. Same
		// rule as renting a server.
		ctx = context.WithoutCancel(ctx)

		result, err := fn(ctx)

		m.mu.Lock()
		defer m.mu.Unlock()
		job.running = false
		job.EndedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			job.Phase = "failed"
			job.Error = err.Error()
			if m.Log != nil {
				m.Log.Error("pack job failed", "kind", job.Kind, "profile", job.Profile, "err", err)
			}
			return
		}
		job.Phase = "done"
		job.Done, job.Total = 1, 1
		job.Result = result
		if m.Log != nil {
			m.Log.Info("pack job finished", "kind", job.Kind, "profile", job.Profile, "files", result.Files)
		}
	}()
}

// progress is the fetch half of the counters.
func (m *Manager) progress(job *Job) func(phase string, done, total int) {
	return func(phase string, done, total int) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.job != job {
			return
		}
		job.Phase = phase
		job.Done, job.Total = done, total
	}
}

// uploadProgress is the publish half. It carries the same counters forward so the
// page shows one bar, not two.
func (m *Manager) uploadProgress(job *Job) func(done, total int) {
	return func(done, total int) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.job != job {
			return
		}
		job.Phase = "upload"
		job.Done, job.Total = done, total
	}
}
