package pack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newFakeModrinth serves one project and a fixed version list, so the parts that
// read Modrinth can be tested without the network.
func newFakeModrinth(t *testing.T, versions []Version) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/project/test-pack":
			_, _ = io.WriteString(w, `{"id":"p","slug":"test-pack","title":"Test Pack","project_type":"modpack"}`)
		case r.URL.Path == "/project/test-pack/version":
			_ = json.NewEncoder(w).Encode(versions)
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
