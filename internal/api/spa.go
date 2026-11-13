package api

import (
	"io/fs"
	"net/http"
	"strings"

	"outpost/web"
)

// spaHandler serves the embedded SvelteKit build, falling back to index.html
// for anything that is not a real static asset.
//
// Two things here are load-bearing and were learned the hard way in bwmon:
//
//   - The fallback serves index.html's bytes directly instead of delegating to
//     http.FileServer with a rewritten path, because FileServer 301-redirects
//     any path ending in "index.html" to its directory form, which turns every
//     fallback into a redirect loop.
//   - Hashed assets under /_app/immutable/ are cached forever (a new build
//     never reuses a hash for different bytes) and everything else is not
//     cached at all. An "outpost update" swaps the embedded build mid-flight;
//     a browser holding a stale index.html would ask for asset hashes that no
//     longer exist and the page would be blank — which reads as "it broke".
func spaHandler() http.Handler {
	static, err := fs.Sub(web.Assets, "build")
	if err != nil {
		panic("web: embedded build is missing: " + err.Error())
	}
	files := http.FileServer(http.FS(static))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}

		if r.URL.Path != "/" {
			if _, err := fs.Stat(static, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				serveIndex(w, static)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, static fs.FS) {
	data, err := fs.ReadFile(static, "index.html")
	if err != nil {
		http.Error(w, "index.html missing from the embedded build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
