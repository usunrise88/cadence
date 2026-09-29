// Package webui serves the single-page app embedded in the binary.
//
// dist/ holds the Vite build (copied in by `make web` or the Docker build); only a placeholder index.html is
// committed, so a plain `go build` still produces a working binary.
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

// Handler serves files from the build and answers every other path (except under /assets/) with index.html, so
// client routes such as /p/demo/w/Training load the app. Hashed files under /assets/ are cached for a year; index.html never.
func Handler() http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed directive guarantees dist exists
	}
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch {
		case name == "" || name == "index.html":
			serveIndex(w, r, root)
			return
		case !exists(root, name) && strings.HasPrefix(name, "assets/"):
			http.NotFound(w, r) // a missing build file: answering HTML would hide the error
			return
		case !exists(root, name):
			serveIndex(w, r, root)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func exists(root fs.FS, name string) bool {
	st, err := fs.Stat(root, name)
	return err == nil && !st.IsDir()
}

func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	body, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "web UI missing from this build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
