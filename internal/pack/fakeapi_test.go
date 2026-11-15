package pack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeModrinth serves one project and a fixed version list, so the parts that
// read Modrinth can be tested without the network.
func newFakeModrinth(t *testing.T, versions []Version) *httptest.Server {
	t.Helper()

	// Any slug is served: some tests are about how a source becomes a profile name,
	// and the fake should not be the thing that decides which slugs exist.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/project/")
		if path == "" || path == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		switch suffix := path; {
		case strings.HasSuffix(suffix, "/version"):
			_ = json.NewEncoder(w).Encode(versions)
		case !strings.Contains(suffix, "/"):
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": "p", "slug": suffix, "title": "Test Pack", "project_type": "modpack",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	old := modrinthBase
	modrinthBase = srv.URL
	t.Cleanup(func() { modrinthBase = old })
	return srv
}
