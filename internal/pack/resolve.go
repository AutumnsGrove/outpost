package pack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"outpost/internal/publish"
)

// modrinthBase is the v2 API root. A variable so tests can point it at httptest.
var modrinthBase = "https://api.modrinth.com/v2"

// httpClient is generous on purpose: a large pack's .mrpack is hundreds of
// megabytes, and a timeout tuned for JSON would cut it off mid-download.
var httpClient = &http.Client{Timeout: 30 * time.Minute}

const userAgent = "outpost/1 (github.com/AutumnsGrove/outpost)"

// Project is what Modrinth knows about a pack.
type Project struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	ProjectType string `json:"project_type"`
}

// Version is one published version of a pack.
type Version struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	VersionNumber string        `json:"version_number"`
	VersionType   string        `json:"version_type"` // release, beta, alpha
	GameVersions  []string      `json:"game_versions"`
	Loaders       []string      `json:"loaders"`
	Files         []VersionFile `json:"files"`
}

// VersionFile is one downloadable file of a version. A pack version has one:
// the .mrpack.
type VersionFile struct {
	URL      string            `json:"url"`
	Filename string            `json:"filename"`
	Primary  bool              `json:"primary"`
	Hashes   map[string]string `json:"hashes"`
	Size     int64             `json:"size"`
}

// Mrpack is the file this package wants out of a version.
func (v Version) Mrpack() (VersionFile, error) {
	for _, f := range v.Files {
		if f.Primary && strings.HasSuffix(strings.ToLower(f.Filename), ".mrpack") {
			return f, nil
		}
	}
	for _, f := range v.Files {
		if strings.HasSuffix(strings.ToLower(f.Filename), ".mrpack") {
			return f, nil
		}
	}
	return VersionFile{}, fmt.Errorf("modrinth version %s has no .mrpack file", v.ID)
}

// sourceKind is what the command line argument turned out to be.
type sourceKind int

const (
	sourceProject sourceKind = iota // a slug or a modrinth.com project URL
	sourceURL                       // a direct .mrpack URL
	sourceFile                      // a local .mrpack
)

// source is a parsed `outpost pack fetch` argument.
type source struct {
	kind    sourceKind
	slug    string
	version string // version id or number from a /version/ URL, may be empty
	url     string
	path    string
}

// parseSource classifies what a person pastes in. All four forms are things
// Prism accepts or produces, so none of them should require translating first.
func parseSource(raw string) (source, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return source{}, fmt.Errorf("empty pack source")
	}

	if !strings.Contains(raw, "://") {
		if strings.HasSuffix(strings.ToLower(raw), ".mrpack") {
			return source{kind: sourceFile, path: raw}, nil
		}
		if !validSlug(raw) {
			return source{}, fmt.Errorf("%q is not a Modrinth slug (lowercase letters, digits and dashes), a modrinth.com URL, or a path to a .mrpack", raw)
		}
		return source{kind: sourceProject, slug: raw}, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return source{}, fmt.Errorf("parse %q: %w", raw, err)
	}
	if strings.HasSuffix(strings.ToLower(u.Path), ".mrpack") {
		return source{kind: sourceURL, url: raw, slug: strings.TrimSuffix(filepath.Base(u.Path), ".mrpack")}, nil
	}

	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if host != "modrinth.com" && host != "api.modrinth.com" {
		return source{}, fmt.Errorf("%q is not a Modrinth link and not a .mrpack; a CurseForge or FTB pack has to be exported by hand", raw)
	}

	// /modpack/<slug>, /modpack/<slug>/version/<number-or-id>, and the older
	// /mod/<slug> shape.
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || (parts[0] != "modpack" && parts[0] != "mod") || parts[1] == "" {
		return source{}, fmt.Errorf("%q is not a Modrinth modpack URL", raw)
	}
	src := source{kind: sourceProject, slug: parts[1]}
	if len(parts) >= 4 && parts[2] == "version" {
		src.version = parts[3]
	}
	return src, nil
}

func validSlug(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// getJSON fetches and decodes one API answer.
func getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modrinthBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("modrinth has no such project: %s", path)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("modrinth %s: %s: %.160s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse modrinth %s: %w", path, err)
	}
	return nil
}

// fetchProject reads a pack's metadata.
func fetchProject(ctx context.Context, slug string) (Project, error) {
	var p Project
	err := getJSON(ctx, "/project/"+url.PathEscape(slug), &p)
	return p, err
}

// fetchVersions lists a pack's versions, newest first, as Modrinth orders them.
func fetchVersions(ctx context.Context, slug string) ([]Version, error) {
	var v []Version
	err := getJSON(ctx, "/project/"+url.PathEscape(slug)+"/version", &v)
	return v, err
}

// pickVersion chooses the version to install.
//
// Without an explicit one it takes the newest release for the Minecraft version
// this server can boot. That filter is not tidiness: picking the newest version
// of a pack that has moved to 1.21 produces a rental that dies on Java 17 and
// looks like a broken modpack.
func pickVersion(versions []Version, want string) (Version, error) {
	if len(versions) == 0 {
		return Version{}, fmt.Errorf("the project has no versions")
	}

	if want != "" {
		for _, v := range versions {
			if v.ID == want || strings.EqualFold(v.VersionNumber, want) {
				return v, nil
			}
		}
		return Version{}, fmt.Errorf("no version %q (have %s)", want, versionList(versions, 5))
	}

	for _, releaseOnly := range []bool{true, false} {
		for _, v := range versions {
			if releaseOnly && v.VersionType != "release" {
				continue
			}
			if !contains(v.GameVersions, publish.SupportedMC) || !contains(v.Loaders, "fabric") {
				continue
			}
			return v, nil
		}
		// A pre-release for the right version beats nothing at all; that is the
		// second pass.
	}
	return Version{}, fmt.Errorf("no version for Minecraft %s on Fabric; this pack offers %s",
		publish.SupportedMC, versionList(versions, 3))
}

func versionList(versions []Version, n int) string {
	out := make([]string, 0, n)
	for i, v := range versions {
		if i >= n {
			out = append(out, "…")
			break
		}
		out = append(out, fmt.Sprintf("%s (minecraft %s, %s)",
			v.VersionNumber, strings.Join(v.GameVersions, "/"), strings.Join(v.Loaders, "/")))
	}
	return strings.Join(out, ", ")
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// openMrpack produces a readable .mrpack, downloading it when the source is a
// URL. cleanup is safe to call in every case.
func openMrpack(ctx context.Context, rawURL string) (f *os.File, cleanup func(), err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, func() {}, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, func() {}, fmt.Errorf("download %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, func() {}, fmt.Errorf("download %s: %s: %.160s", rawURL, resp.Status, strings.TrimSpace(string(body)))
	}

	tmp, err := os.CreateTemp("", "outpost-*.mrpack")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup = func() { os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		cleanup()
		return nil, func() {}, fmt.Errorf("download %s: %w", rawURL, err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		cleanup()
		return nil, func() {}, err
	}
	return tmp, cleanup, nil
}
