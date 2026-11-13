package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"outpost/web"
)

// firstAsset returns a real hashed asset path from the embedded build, so the
// test does not hard-code a hash that changes on every build.
func firstAsset(t *testing.T) string {
	t.Helper()
	static, err := fs.Sub(web.Assets, "build")
	if err != nil {
		t.Fatalf("sub build: %v", err)
	}
	var found string
	err = fs.WalkDir(static, "_app/immutable", func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".js") {
			found = p
		}
		return nil
	})
	if err != nil || found == "" {
		t.Fatalf("no immutable assets in the embedded build (was web/build committed?): %v", err)
	}
	return found
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func TestSPAServesTheShellAtTheRoot(t *testing.T) {
	rr := get(t, spaHandler(), "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	if body := rr.Body.String(); !strings.Contains(body, "<div style=\"display: contents\">") {
		t.Fatalf("not the SvelteKit shell:\n%.200s", body)
	}
	// A stale index.html points at asset hashes that a new build deleted, which
	// is a blank page that reads as "it broke".
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Fatalf("index cache-control = %q", cc)
	}
}

func TestSPAFallsBackForClientSideRoutes(t *testing.T) {
	rr := get(t, spaHandler(), "/some/client/route")
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("deep link = %d %s", rr.Code, rr.Header().Get("Content-Type"))
	}
}

func TestSPAServesHashedAssetsWithALongCache(t *testing.T) {
	asset := firstAsset(t)
	rr := get(t, spaHandler(), "/"+asset)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s = %d", asset, rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("asset cache-control = %q", cc)
	}
}

func TestFormTokenIsUnpredictableAndComparedStrictly(t *testing.T) {
	a, b := newFormToken(), newFormToken()
	if len(a) != 32 || a == b {
		t.Fatalf("tokens: %q / %q", a, b)
	}
	if !checkToken(a, a) {
		t.Fatal("a token should match itself")
	}
	if checkToken(a, b) || checkToken("", a) || checkToken(a, "") {
		t.Fatal("mismatched or empty tokens must be refused")
	}
}
