// Package roster owns the player list: who may join and who is an operator.
//
// The authoritative copy is `server/whitelist.json` and `server/ops.json` in the
// asset bucket, because that is what cloud-init restores on every boot. Anything
// done to a running server over RCON alone is a lie that lasts until the VPS
// dies, so every change here is written to R2 first and then (by the caller)
// applied to the live server. See PLAN.md, decision 18.
package roster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"outpost/internal/publish"
	"outpost/internal/r2"
)

const (
	whitelistKey = "server/whitelist.json"
	opsKey       = "server/ops.json"
)

// Blobs is the slice of R2 the roster needs.
type Blobs interface {
	Get(ctx context.Context, bucket, key string) ([]byte, error)
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64, sha256 string) error
}

// Resolver turns a name into the canonical name and UUID.
type Resolver func(ctx context.Context, name string) (publish.Player, error)

// Roster reads and edits the two files.
type Roster struct {
	blobs   Blobs
	bucket  string
	resolve Resolver

	// mu serialises read-modify-write within this process. Across processes the
	// last writer wins, which for two files edited by one person is acceptable.
	mu sync.Mutex
}

// New returns a Roster over the asset bucket. A nil resolve uses Mojang.
func New(blobs Blobs, assetBucket string, resolve Resolver) *Roster {
	if resolve == nil {
		resolve = publish.ResolvePlayer
	}
	return &Roster{blobs: blobs, bucket: assetBucket, resolve: resolve}
}

// Snapshot is the current lists, by name.
type Snapshot struct {
	Whitelist []string `json:"whitelist"`
	Ops       []string `json:"ops"`
}

// ErrNotWhitelisted is returned when asking to op someone who cannot join.
var ErrNotWhitelisted = errors.New("not on the whitelist; add them first")

func (r *Roster) load(ctx context.Context, key string) ([]publish.Player, error) {
	raw, err := r.blobs.Get(ctx, r.bucket, key)
	if errors.Is(err, r2.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var out []publish.Player
	if err := json.Unmarshal(raw, &out); err != nil {
		// Refuse rather than treat a mangled file as empty: writing back an
		// "empty plus one" list would silently drop everyone else.
		return nil, fmt.Errorf("%s in R2 is not valid JSON, fix it before editing: %w", key, err)
	}
	return out, nil
}

func (r *Roster) save(ctx context.Context, key string, players []publish.Player) error {
	if players == nil {
		players = []publish.Player{}
	}
	body, err := json.MarshalIndent(players, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return r.blobs.Put(ctx, r.bucket, key, bytes.NewReader(body), int64(len(body)), "")
}

func names(players []publish.Player) []string {
	out := make([]string, 0, len(players))
	for _, p := range players {
		out = append(out, p.Name)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// List returns both lists.
func (r *Roster) List(ctx context.Context) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot(ctx)
}

func (r *Roster) snapshot(ctx context.Context) (Snapshot, error) {
	wl, err := r.load(ctx, whitelistKey)
	if err != nil {
		return Snapshot{}, err
	}
	ops, err := r.load(ctx, opsKey)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Whitelist: names(wl), Ops: names(ops)}, nil
}

func indexOf(players []publish.Player, uuid string) int {
	for i, p := range players {
		if strings.EqualFold(p.UUID, uuid) {
			return i
		}
	}
	return -1
}

// Add whitelists a player. Idempotent: adding someone already listed is a no-op
// that still returns their canonical name.
func (r *Roster) Add(ctx context.Context, name string) (publish.Player, bool, error) {
	p, err := r.resolve(ctx, name)
	if err != nil {
		return p, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	wl, err := r.load(ctx, whitelistKey)
	if err != nil {
		return p, false, err
	}
	if indexOf(wl, p.UUID) >= 0 {
		return p, false, nil
	}
	return p, true, r.save(ctx, whitelistKey, append(wl, publish.Player{UUID: p.UUID, Name: p.Name}))
}

// Remove drops a player from the whitelist and from ops (an op who cannot join
// is a trap). Names are matched against what is stored, so a player whose
// Mojang name changed can still be removed.
func (r *Roster) Remove(ctx context.Context, name string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	wl, err := r.load(ctx, whitelistKey)
	if err != nil {
		return name, false, err
	}
	ops, err := r.load(ctx, opsKey)
	if err != nil {
		return name, false, err
	}

	canonical, changed := name, false
	wl, canonical, changed = drop(wl, name, canonical, changed)
	ops, canonical, changed = drop(ops, name, canonical, changed)
	if !changed {
		return name, false, nil
	}
	if err := r.save(ctx, whitelistKey, wl); err != nil {
		return canonical, true, err
	}
	return canonical, true, r.save(ctx, opsKey, ops)
}

func drop(players []publish.Player, name, canonical string, changed bool) ([]publish.Player, string, bool) {
	out := players[:0:0]
	for _, p := range players {
		if strings.EqualFold(p.Name, name) {
			canonical, changed = p.Name, true
			continue
		}
		out = append(out, p)
	}
	return out, canonical, changed
}

// Op makes a whitelisted player an operator, at the same level publish uses.
func (r *Roster) Op(ctx context.Context, name string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	wl, err := r.load(ctx, whitelistKey)
	if err != nil {
		return name, false, err
	}
	var who *publish.Player
	for i := range wl {
		if strings.EqualFold(wl[i].Name, name) {
			who = &wl[i]
		}
	}
	if who == nil {
		return name, false, fmt.Errorf("%s: %w", name, ErrNotWhitelisted)
	}

	ops, err := r.load(ctx, opsKey)
	if err != nil {
		return who.Name, false, err
	}
	if indexOf(ops, who.UUID) >= 0 {
		return who.Name, false, nil
	}
	ops = append(ops, publish.Player{UUID: who.UUID, Name: who.Name, Level: 4, Bypass: true})
	return who.Name, true, r.save(ctx, opsKey, ops)
}

// Deop removes operator status but keeps the player on the whitelist.
func (r *Roster) Deop(ctx context.Context, name string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ops, err := r.load(ctx, opsKey)
	if err != nil {
		return name, false, err
	}
	rest, canonical, changed := drop(ops, name, name, false)
	if !changed {
		return name, false, nil
	}
	return canonical, true, r.save(ctx, opsKey, rest)
}

// Action is something the page or CLI can do to a player.
type Action string

const (
	ActionAdd    Action = "add"
	ActionRemove Action = "remove"
	ActionOp     Action = "op"
	ActionDeop   Action = "deop"
)

// Result describes what an Apply did, in words a phone can show.
type Result struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	// Changed is false when the files already said what was asked.
	Changed bool `json:"changed"`
	// Applied is true when the running server was told as well.
	Applied bool `json:"applied"`
}

// Apply is the whole story of one change: write it to R2, where the next boot
// reads it, then (when there is a live server) tell that server too. The order
// is the point. The other way round, a successful RCON command with a failed R2
// write is a change that evaporates when the VPS is deleted.
//
// send runs one console command; it is nil or live=false when nothing is up. A
// failure to reach the live server is reported but does not undo the R2 write,
// which is the part that persists.
func (r *Roster) Apply(ctx context.Context, action Action, name string, live bool, send func(ctx context.Context, cmd string) error) (Result, error) {
	name = strings.TrimSpace(name)
	if !publish.ValidName(name) {
		return Result{}, fmt.Errorf("%q is not a Minecraft name (3-16 letters, digits or underscores)", name)
	}

	var (
		res  = Result{Name: name}
		cmds []string
		err  error
	)
	switch action {
	case ActionAdd:
		var p publish.Player
		p, res.Changed, err = r.Add(ctx, name)
		res.Name = p.Name
		cmds = []string{"whitelist add " + res.Name}
	case ActionRemove:
		res.Name, res.Changed, err = r.Remove(ctx, name)
		// Whether or not R2 changed, make the live server agree.
		cmds = []string{"deop " + res.Name, "whitelist remove " + res.Name}
	case ActionOp:
		res.Name, res.Changed, err = r.Op(ctx, name)
		cmds = []string{"op " + res.Name}
	case ActionDeop:
		res.Name, res.Changed, err = r.Deop(ctx, name)
		cmds = []string{"deop " + res.Name}
	default:
		return Result{}, fmt.Errorf("unknown action %q", action)
	}
	if err != nil {
		return res, err
	}

	verb := map[Action]string{
		ActionAdd:    "is on the whitelist",
		ActionRemove: "is off the whitelist",
		ActionOp:     "is an operator",
		ActionDeop:   "is no longer an operator",
	}[action]
	res.Message = res.Name + " " + verb
	if !res.Changed {
		res.Message = res.Name + " " + verb + " already"
	}

	if live && send != nil {
		for _, c := range cmds {
			if err := send(ctx, c); err != nil {
				res.Message += fmt.Sprintf(" for the next boot, but the running server could not be told (%v)", err)
				return res, nil
			}
		}
		res.Applied = true
		res.Message += ", and the running server knows"
	} else {
		res.Message += "; it takes effect when the server next starts"
	}
	return res, nil
}
