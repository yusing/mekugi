// Package webui serves the embedded browser application independently of execution.
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

// Handler serves only the page and bundled assets. Machine connections belong to
// the browser and never pass through this host.
func Handler() http.Handler {
	assets, _ := fs.Sub(dist, "dist/app") // The literal path is always valid.
	return assetHandler{assets: assets}
}

type assetHandler struct{ assets fs.FS }

// Explicit machines may use any HTTP(S)/WebSocket origin. Other resources stay
// bundled, so recorded Markdown cannot make requests to the hosts it names.
const contentPolicy = "default-src 'none'; manifest-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data: blob:; connect-src http: https: ws: wss:; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Source: apps/mekugi/internal/webui/hub.go:116:128@[14a6d1f3] Hub.static
func (h assetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", contentPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := fs.Stat(h.assets, "index.html"); err != nil {
		http.Error(w, "Web UI assets are not built. Run `make webui`, then rebuild mekugi.", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if entry, err := fs.Stat(h.assets, name); err != nil || entry.IsDir() {
		http.NotFound(w, r)
		return
	}
	if name == "manifest.webmanifest" {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	// Vite places content-hashed bundles and fonts under assets/.
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.FileServerFS(h.assets).ServeHTTP(w, r)
}
