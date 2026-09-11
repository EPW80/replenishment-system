package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// consoleAssets contains the dependency-free development console. Keeping the files
// in the service binary makes the console useful in a local checkout without adding a
// JavaScript toolchain or a second process to operate.
//
//go:embed console/*
var consoleAssets embed.FS

// NewConsoleHandler serves the standalone development console and its static assets.
// Application routes fall back to index.html so list and detail views have real URLs.
func NewConsoleHandler() http.Handler {
	assets, err := fs.Sub(consoleAssets, "console")
	if err != nil {
		panic("embed console assets: " + err.Error())
	}
	files := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		rel := strings.TrimPrefix(r.URL.Path, "/console/")
		if rel == "" || (!strings.Contains(path.Base(rel), ".") && strings.HasPrefix(rel, "schedules/")) {
			r.URL.Path = "/"
			files.ServeHTTP(w, r)
			return
		}

		r.URL.Path = "/" + rel
		files.ServeHTTP(w, r)
	})
}
