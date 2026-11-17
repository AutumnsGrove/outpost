package pack

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
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
	PublishedAt   string        `json:"date_published"`
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

// postJSON sends a body and decodes one API answer. Modrinth's batch endpoints take
// a JSON array of ids, which turns a hundred lookups into one request.
func postJSON(ctx context.Context, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, modrinthBase+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("modrinth %s: %s: %.160s", path, resp.Status, strings.TrimSpace(string(answer)))
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("parse modrinth %s: %w", path, err)
	}
	return nil
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
// Without an explicit one it takes the newest release for SupportedMC — the
// oldest row of publish.Minecrafts, not the newest bootable one. That is
// deliberate: the table can now boot 1.21.1 as well, but "newest runnable" would
// change which Minecraft version a default `pack fetch` installs the day a row is
// added, over a world that cannot follow it. Naming a version (the panel does) or
// --version on the command line is how somebody asks for 1.21.1.
//
// The filter is not tidiness: picking the newest version of a pack that has moved
// to a version outside the table produces a rental that dies at boot and looks
// like a broken modpack.
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
	return Version{}, fmt.Errorf("no version for Minecraft %s on Fabric (the default; pass a version to choose another); this pack offers %s",
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
//
// wantSHA512 is the hash the API published for the archive, and it is checked
// when there is one: the pack's mod jars get verified one by one as they land,
// but overrides/ carries no hashes at all, so the archive is the only thing that
// vouches for those bytes before they are extracted and run.
func openMrpack(ctx context.Context, rawURL, wantSHA512, cachePath string) (f *os.File, cleanup func(), err error) {
	noop := func() {}

	// A cached archive is the whole point of resolving before installing: the
	// preview downloads it once, the install reads it back.
	if cachePath != "" {
		if cached, err := os.Open(cachePath); err == nil {
			info, statErr := cached.Stat()
			if statErr == nil && info.Size() > 0 {
				if wantSHA512 == "" {
					return cached, func() { cached.Close() }, nil
				}
				if ok, _ := matches(cachePath, "sha512", wantSHA512); ok {
					return cached, func() { cached.Close() }, nil
				}
			}
			cached.Close()
		}
	}

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

	dir, name := os.TempDir(), "outpost-*.mrpack"
	if cachePath != "" {
		dir, name = filepath.Dir(cachePath), filepath.Base(cachePath)+".part"
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, noop, err
		}
	}
	tmp, err := os.CreateTemp(dir, name)
	if err != nil {
		return nil, noop, err
	}
	cached := cachePath != ""
	// discard is for every exit that is not a verified archive: a partial or
	// mismatched download must never reach the cache, because a bare .mrpack URL
	// has no hash to check it against later, so a truncated file would be read as
	// valid for ever.
	discard := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	// Close before removing: the pack is up to a few hundred MB and the caller
	// defers this, so a leaked descriptor would live as long as the process.
	cleanup = func() {
		if !cached {
			discard()
			return
		}
		tmp.Close()
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		discard()
		return nil, func() {}, fmt.Errorf("download %s: %w", rawURL, err)
	}
	// The temp file is at EOF, and both the hash and the caller read it from the
	// start.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		discard()
		return nil, noop, err
	}
	if wantSHA512 != "" {
		h := sha512.New()
		if _, err := io.Copy(h, tmp); err != nil {
			discard()
			return nil, func() {}, fmt.Errorf("hash %s: %w", rawURL, err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA512) {
			discard()
			return nil, func() {}, fmt.Errorf("downloaded %s does not match the hash Modrinth published for it", rawURL)
		}
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		cleanup()
		return nil, func() {}, err
	}
	// A cached archive is renamed into place before it is handed over, so the next
	// run finds it.
	if cached {
		if err := os.Rename(tmp.Name(), cachePath); err != nil {
			discard()
			return nil, noop, err
		}
		reopened, err := os.Open(cachePath)
		if err != nil {
			return nil, noop, err
		}
		tmp.Close()
		return reopened, func() { reopened.Close() }, nil
	}
	return tmp, cleanup, nil
}

// cachePath is where one version's .mrpack is kept between runs. Empty cache dir
// means "no cache", which is what a bare .mrpack URL gets when nothing says where
// to keep it.
func cachePath(cacheDir, slug, versionID string) string {
	if cacheDir == "" {
		return ""
	}
	if slug == "" {
		slug = "pack"
	}
	if versionID == "" {
		versionID = "latest"
	}
	return filepath.Join(cacheDir, slugify(slug)+"-"+versionID+".mrpack")
}

// SourceProfile is the profile id a source would install into when the caller
// does not name one: the Modrinth slug, or the .mrpack's file name.
func SourceProfile(src string) (string, error) {
	parsed, err := parseSource(src)
	if err != nil {
		return "", err
	}
	var name string
	switch parsed.kind {
	case sourceProject:
		name = parsed.slug
	case sourceURL:
		name = parsed.slug
	case sourceFile:
		name = strings.TrimSuffix(filepath.Base(parsed.path), ".mrpack")
	}
	if slugify(name) == "" {
		return "", fmt.Errorf("cannot derive a profile name from %q; name one", src)
	}
	// The same rule publish uses for a pack's own slug, so the panel, the CLI and
	// the version picker all name a profile identically. `slugify` is for file
	// names: it turns an underscore into a dash, which would make "my_pack" a
	// different profile here than at install time.
	return publish.ProfileFromSlug(name), nil
}
