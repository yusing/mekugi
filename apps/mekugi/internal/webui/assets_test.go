package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestAssetsOnlyHost(t *testing.T) {
	server := httptest.NewServer(assetHandler{assets: fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>Mekugi</title>")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Mekugi"}`)},
		"assets/app-1234.js":   {Data: []byte("console.log('bundle')")},
		"assets/app-1234.css":  {Data: []byte("body { color: black }")},
	}})
	t.Cleanup(server.Close)
	for _, test := range []struct {
		method, path, body string
		status             int
		immutable          bool
	}{
		{http.MethodGet, "/", "<!doctype html>", http.StatusOK, false},
		{http.MethodHead, "/", "", http.StatusOK, false},
		{http.MethodGet, "/manifest.webmanifest", `"name":"Mekugi"`, http.StatusOK, false},
		{http.MethodHead, "/manifest.webmanifest", "", http.StatusOK, false},
		{http.MethodGet, "/assets/app-1234.js", "console.log", http.StatusOK, true},
		{http.MethodHead, "/assets/app-1234.css", "", http.StatusOK, true},
		{http.MethodGet, "/assets/", "404", http.StatusNotFound, false},
		{http.MethodGet, "/assets/missing.js", "404", http.StatusNotFound, false},
		{http.MethodGet, "/api/sessions", "404", http.StatusNotFound, false},
		{http.MethodPost, "/api/sessions", "Method not allowed", http.StatusMethodNotAllowed, false},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), test.method, server.URL+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status || !strings.Contains(string(body), test.body) || (test.method == http.MethodHead && len(body) != 0) {
				t.Fatalf("response = %d %q", response.StatusCode, body)
			}
			cache := "no-cache"
			if test.immutable {
				cache = "public, max-age=31536000, immutable"
			}
			if response.Header.Get("Cache-Control") != cache {
				t.Fatalf("cache = %q", response.Header.Get("Cache-Control"))
			}
			if response.Header.Get("Set-Cookie") != "" || response.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("asset host added credentials or API cross-origin admission")
			}
			policy := response.Header.Get("Content-Security-Policy")
			if test.path == "/manifest.webmanifest" && response.Header.Get("Content-Type") != "application/manifest+json" {
				t.Fatalf("manifest type = %q", response.Header.Get("Content-Type"))
			}
			if !strings.Contains(policy, "manifest-src 'self'") {
				t.Fatalf("CSP blocks install manifest: %s", policy)
			}
			for _, directive := range []string{"img-src 'self' data: blob:", "script-src 'self'", "style-src 'self' 'unsafe-inline'", "connect-src http: https: ws: wss:", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'"} {
				if !strings.Contains(policy, directive) {
					t.Errorf("CSP omits %q: %s", directive, policy)
				}
			}
		})
	}
}

func TestMissingAssets(t *testing.T) {
	response := httptest.NewRecorder()
	assetHandler{assets: fstest.MapFS{}}.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "make webui") || response.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("missing assets = %d %q, headers %v", response.Code, response.Body.String(), response.Header())
	}
}
