// Package webui serves the exported Expo web build that is embedded in the binary.
//
// Build it with `npm run build:web` in app/, which writes into internal/webui/dist.
// Without a build the handler answers with instructions instead of failing.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler returns an http.Handler for the embedded single-page app. Unknown
// paths without a file extension fall back to index.html (client-side routing).
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at compile time
	}
	return handlerFor(sub)
}

func handlerFor(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(root, "index.html"); err != nil {
			http.Error(w, "Argus web UI is not built into this binary. Run `npm run build:web` in app/ and rebuild.", http.StatusNotFound)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(root, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "_expo/static/") || strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // content-hashed
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		if path.Ext(p) != "" { // a missing asset must 404, not return HTML
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
