package grok_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/providers/grok"
)

type authTransport func(*http.Request) (*http.Response, error)

func (f authTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func authResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestAuthAPIKeyPrecedesMissingStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := &http.Client{Transport: authTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("API-key authentication attempted an OAuth request")
		return nil, nil
	})}
	auth := grok.NewAuth(filepath.Join(dir, "missing.json"), " \tapi-key\n", client)
	if !auth.UsesAPIKey() {
		t.Fatal("trimmed API key was not selected")
	}
	credentials, err := auth.Credentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Endpoint != "https://api.x.ai/v1/chat/completions" || credentials.Headers.Get("Authorization") != "Bearer api-key" {
		t.Fatalf("unexpected API credentials: endpoint=%q authorization=%q", credentials.Endpoint, credentials.Headers.Get("Authorization"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("API-key selection created credential-store artifacts: %v", entries)
	}
}

func TestAuthRejectsRedirectsWithoutChangingInjectedClient(t *testing.T) {
	for _, stage := range []string{"discovery", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "auth.json")
			// A rejected, otherwise unexpired token must force a refresh.
			store := `{"https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828":{"auth_mode":"oidc","oidc_issuer":"https://auth.x.ai","oidc_client_id":"b1a00492-073a-47ea-816f-4c329264a828","key":"rejected-token","refresh_token":"private-refresh","expires_at":"2099-01-01T00:00:00Z"}}`
			if err := os.WriteFile(path, []byte(store), 0600); err != nil {
				t.Fatal(err)
			}
			var requests []string
			redirectChecks := 0
			client := &http.Client{
				Timeout: time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					redirectChecks++
					return nil
				},
				Transport: authTransport(func(r *http.Request) (*http.Response, error) {
					requests = append(requests, r.Method+" "+r.URL.String())
					if r.URL.Host == "client.invalid" {
						if r.URL.Path == "/probe" {
							response := authResponse(http.StatusFound, "")
							response.Header.Set("Location", "https://client.invalid/target")
							return response, nil
						}
						return authResponse(http.StatusOK, ""), nil
					}
					if r.URL.Host != "auth.x.ai" {
						t.Errorf("followed credential redirect to %s", r.URL)
						return authResponse(http.StatusOK, `{}`), nil
					}
					if stage == "refresh" && r.Method == http.MethodGet {
						return authResponse(http.StatusOK, `{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/token"}`), nil
					}
					if r.Method == http.MethodPost {
						if err := r.ParseForm(); err != nil {
							t.Fatal(err)
						}
						if r.Form.Get("refresh_token") != "private-refresh" {
							t.Fatal("refresh fixture did not receive stored credential")
						}
					}
					response := authResponse(http.StatusTemporaryRedirect, "")
					response.Header.Set("Location", "https://attacker.invalid/token")
					return response, nil
				}),
			}
			auth := grok.NewAuth(path, "", client)
			if auth.UsesAPIKey() {
				t.Fatal("OAuth store selected API-key authentication")
			}
			if err := auth.Refresh(t.Context(), "rejected-token"); err == nil {
				t.Fatal("accepted redirected OAuth response")
			}
			wantRequests := 1
			if stage == "refresh" {
				wantRequests = 2
			}
			if len(requests) != wantRequests || redirectChecks != 0 {
				t.Fatalf("credential redirects escaped rejection: requests=%v redirect checks=%d", requests, redirectChecks)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != store {
				t.Fatal("failed refresh changed credential store")
			}
			response, err := client.Get("https://client.invalid/probe")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK || redirectChecks != 1 || client.Timeout != time.Second {
				t.Fatal("authentication changed the injected client's redirect behavior or timeout")
			}
		})
	}
}
