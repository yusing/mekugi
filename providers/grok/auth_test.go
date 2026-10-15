package grok

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/grok_test.go:166:244@[18475aaf] credential acceptance.
type grokTestTransport func(*http.Request) (*http.Response, error)

func (f grokTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func grokWriteTestAuth(t *testing.T, path string, expiry time.Time) {
	t.Helper()
	entry := map[string]any{"auth_mode": "oidc", "oidc_issuer": grokIssuer, "oidc_client_id": grokOAuthClientID, "key": "old-token", "refresh_token": "old-refresh", "expires_at": expiry.Format(time.RFC3339Nano), "unrelated": "keep"}
	if err := os.WriteFile(path, responses.MustMarshalJSON(map[string]any{grokIssuer + "::" + grokOAuthClientID: entry, "other-account": map[string]string{"key": "unrelated"}}), 0600); err != nil {
		t.Fatal(err)
	}
}

func serverHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestGrokOAuthRefreshSerializesAndPreservesStore(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "auth.json")
	now := time.Now().UTC()
	grokWriteTestAuth(t, path, now.Add(-time.Minute))
	var exchanges atomic.Int32
	transport := grokTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("inference authorization leaked into OAuth")
		}
		if r.Method == "GET" {
			return serverHTTPResponse(`{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/token"}`), nil
		}
		exchanges.Add(1)
		r.ParseForm()
		if r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != grokOAuthClientID {
			t.Error("bad refresh form")
		}
		return serverHTTPResponse(`{"access_token":"fresh-token","refresh_token":"rotated-refresh","expires_in":3600}`), nil
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			a := NewAuth(path, "", &http.Client{})
			a.httpClient.Transport = transport
			c, err := a.Credentials(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			if c.Headers.Get("Authorization") != "Bearer fresh-token" {
				t.Error("stale token")
			}
		})
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("refreshes=%d", exchanges.Load())
	}
	a := NewAuth(path, "", &http.Client{})
	store, entry, err := a.read()
	if err != nil {
		t.Fatal(err)
	}
	if responses.StringField(entry, "refresh_token") != "rotated-refresh" || responses.StringField(entry, "unrelated") != "keep" || store["other-account"] == nil {
		t.Fatal("store fields lost")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe credential permissions")
	}
}

func TestGrokOAuthRejectsUntrustedDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	grokWriteTestAuth(t, path, time.Now().Add(-time.Hour))
	a := NewAuth(path, "", &http.Client{})
	var calls int
	a.httpClient.Transport = grokTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return serverHTTPResponse(`{"issuer":"https://auth.x.ai","token_endpoint":"https://attacker.invalid/token"}`), nil
	})
	if _, err := a.Credentials(t.Context()); err == nil {
		t.Fatal("accepted credential exfiltration endpoint")
	}
	if calls != 1 {
		t.Fatal("sent refresh token to untrusted endpoint")
	}
}
