package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Both are variables so tests can point them at an httptest server.
var (
	fabricMetaBase = "https://meta.fabricmc.net"
	mojangBase     = "https://api.mojang.com"
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// FabricServer resolves a loader version if needed and downloads the launcher
// jar Fabric Meta builds for that Minecraft version.
//
// The launcher downloads the vanilla server jar itself on first boot, which is
// why the pack only needs this one jar rather than a full server tree. That
// download is also the slowest part of provisioning, and it is what a snapshot
// of a prepped VPS would remove later.
func FabricServer(ctx context.Context, mcVersion, loaderVersion string) (string, []byte, error) {
	if mcVersion == "" {
		return "", nil, fmt.Errorf("fabric: minecraft version is empty")
	}

	if loaderVersion == "" {
		v, err := newestStableLoader(ctx, mcVersion)
		if err != nil {
			return "", nil, err
		}
		loaderVersion = v
	}

	// The installer version is part of the path, not an option. Leaving it out
	// answers "Endpoint GET ... not found" as a 67-byte text body, which is why
	// the zip check below exists rather than trusting a 200.
	installer, err := newestStableInstaller(ctx)
	if err != nil {
		return "", nil, err
	}

	jarURL := fmt.Sprintf("%s/v2/versions/loader/%s/%s/%s/server/jar",
		fabricMetaBase, mcVersion, loaderVersion, installer)
	jar, err := get(ctx, jarURL)
	if err != nil {
		return "", nil, fmt.Errorf("fabric launcher for %s/%s: %w", mcVersion, loaderVersion, err)
	}
	// A zip starts with "PK\x03\x04". Catching an HTML error page here is worth
	// it: the failure would otherwise be a Java stack trace on the VPS.
	if len(jar) < 4 || string(jar[:4]) != "PK\x03\x04" {
		return "", nil, fmt.Errorf("fabric launcher for %s/%s is not a jar (%d bytes)", mcVersion, loaderVersion, len(jar))
	}
	return loaderVersion, jar, nil
}

// newestStableLoader asks Fabric Meta which loaders exist for a game version and
// picks the first stable one. The list is ordered newest first.
func newestStableLoader(ctx context.Context, mcVersion string) (string, error) {
	raw, err := get(ctx, fmt.Sprintf("%s/v2/versions/loader/%s", fabricMetaBase, mcVersion))
	if err != nil {
		return "", fmt.Errorf("fabric loader list for %s: %w", mcVersion, err)
	}

	var entries []struct {
		Loader struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		} `json:"loader"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", fmt.Errorf("parse fabric loader list: %w", err)
	}
	for _, e := range entries {
		if e.Loader.Stable && e.Loader.Version != "" {
			return e.Loader.Version, nil
		}
	}
	return "", fmt.Errorf("fabric: no stable loader for minecraft %s", mcVersion)
}

// newestStableInstaller returns the current Fabric installer version. The
// launcher jar is built by the installer, and its version is a path segment.
func newestStableInstaller(ctx context.Context) (string, error) {
	raw, err := get(ctx, fabricMetaBase+"/v2/versions/installer")
	if err != nil {
		return "", fmt.Errorf("fabric installer list: %w", err)
	}

	var entries []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", fmt.Errorf("parse fabric installer list: %w", err)
	}
	for _, e := range entries {
		if e.Stable && e.Version != "" {
			return e.Version, nil
		}
	}
	return "", fmt.Errorf("fabric: no stable installer version")
}

// Player is one entry in whitelist.json or ops.json.
type Player struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Level  int    `json:"level,omitempty"`
	Bypass bool   `json:"bypassesPlayerLimit,omitempty"`
}

// PlayerFiles resolves Minecraft names to UUIDs and returns whitelist.json and
// ops.json.
//
// Resolving at publish time is the point: a hand-written whitelist.json goes
// stale silently, and a wrong UUID is a player who cannot join with no error
// anywhere except their own client.
func PlayerFiles(ctx context.Context, whitelistNames, opNames []string) ([]byte, []byte, error) {
	if len(whitelistNames) == 0 {
		return nil, nil, fmt.Errorf("publish: no whitelist names given")
	}

	seen := map[string]bool{}
	var whitelist []Player
	for _, name := range whitelistNames {
		name = strings.TrimSpace(name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true

		p, err := resolve(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		whitelist = append(whitelist, p)
	}

	var ops []Player
	for _, name := range opNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !seen[strings.ToLower(name)] {
			return nil, nil, fmt.Errorf("publish: %q is an op but not on the whitelist", name)
		}
		p, err := resolve(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		p.Level = 4
		p.Bypass = true
		ops = append(ops, p)
	}

	whitelistJSON, err := marshal(whitelist)
	if err != nil {
		return nil, nil, err
	}
	opsJSON, err := marshal(ops)
	if err != nil {
		return nil, nil, err
	}
	return whitelistJSON, opsJSON, nil
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

// ValidName reports whether name can be a Minecraft account name. It guards the
// lookup URL: the name comes from a CLI flag or a web form and is put in a path.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// ResolvePlayer is the one Mojang client: publish uses it at publish time and
// the roster uses it when the page or CLI adds a player.
func ResolvePlayer(ctx context.Context, name string) (Player, error) { return resolve(ctx, name) }

// resolve asks Mojang for the canonical spelling and UUID of a name.
func resolve(ctx context.Context, name string) (Player, error) {
	if !ValidName(name) {
		return Player{}, fmt.Errorf("%q is not a Minecraft name (3-16 letters, digits or underscores)", name)
	}
	raw, err := get(ctx, fmt.Sprintf("%s/users/profiles/minecraft/%s", mojangBase, url.PathEscape(name)))
	if err != nil {
		return Player{}, fmt.Errorf("mojang lookup for %q: %w", name, err)
	}

	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Player{}, fmt.Errorf("parse mojang response for %q: %w", name, err)
	}
	uuid, err := dashUUID(out.ID)
	if err != nil {
		return Player{}, fmt.Errorf("mojang returned a strange id for %q: %w", name, err)
	}
	return Player{UUID: uuid, Name: out.Name}, nil
}

// dashUUID turns the 32 hex characters Mojang returns into the dashed form
// Minecraft's JSON files expect.
func dashUUID(id string) (string, error) {
	id = strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if len(id) != 32 {
		return "", fmt.Errorf("id %q is not 32 hex characters", id)
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return "", fmt.Errorf("id %q has a non-hex character", id)
		}
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", id[0:8], id[8:12], id[12:16], id[16:20], id[20:32]), nil
}

func marshal(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// get is a GET with a short error path: the interesting part of a failure here
// is the status code and the first bytes of the body, not a wrapped transport
// error.
func get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "outpost/publish")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return nil, fmt.Errorf("%s: no such player or version", rawURL)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s: %.120s", rawURL, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}
