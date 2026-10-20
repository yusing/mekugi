package opencode

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inferenceTransport func(*http.Request) (*http.Response, error)

func (f inferenceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestForwardFormatAndCredentialIsolation(t *testing.T) {
	for _, prefix := range []string{"opencode-go", "opencode-zen"} {
		for _, format := range []string{"chat", "anthropic", "responses"} {
			t.Run(prefix+"/"+format, func(t *testing.T) {
				t.Parallel()
				service := Service{Prefix: prefix, Label: "OpenCode", Endpoint: "https://opencode.invalid/v1/chat/completions", APIKey: prefix + "-secret"}
				body := " {\"opaque\":true}\n"
				calls, redirects := 0, 0
				client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
					redirects++
					return nil
				}}
				client.Transport = inferenceTransport(func(r *http.Request) (*http.Response, error) {
					response := &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://probe.invalid/end"}}, Body: io.NopCloser(strings.NewReader(""))}
					if r.URL.Host == "probe.invalid" {
						if r.URL.Path == "/end" {
							response.StatusCode = http.StatusOK
						}
						return response, nil
					}
					calls++
					path := "/v1/chat/completions"
					want := http.Header{"Authorization": {"Bearer " + service.APIKey}, "User-Agent": {"mekugi"}, "X-Opencode-Session": {sessionID(prefix, "thread")}, "Content-Type": {"application/json"}, "Accept": {"text/event-stream"}}
					if format == "anthropic" {
						path = "/v1/messages"
						want.Del("Authorization")
						want.Set("x-api-key", service.APIKey)
						want.Set("anthropic-version", "2023-06-01")
					} else if format == "responses" {
						path = "/v1/responses"
					}
					got, err := io.ReadAll(r.Body)
					if err != nil || string(got) != body || r.Method != http.MethodPost || r.URL.Path != path || !reflect.DeepEqual(r.Header, want) {
						t.Fatalf("request: %s %s headers=%v body=%q err=%v", r.Method, r.URL, r.Header, got, err)
					}
					response.Header.Set("Location", "https://attacker.invalid/inference")
					return response, nil
				})
				response, err := service.Forward(t.Context(), client, format, " thread ", []byte(body))
				if err != nil || response == nil || response.StatusCode != http.StatusTemporaryRedirect || calls != 1 || redirects != 0 {
					t.Fatalf("redirect escaped: calls=%d redirects=%d response=%v err=%v", calls, redirects, response, err)
				}
				response.Body.Close()
				response, err = client.Get("https://probe.invalid/start")
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusOK || redirects != 1 || client.Timeout != time.Second {
					t.Fatal("Forward mutated the supplied client")
				}
			})
		}
	}
}

func TestSessionIDScope(t *testing.T) {
	id := sessionID("opencode-go", " thread ")
	if id == "" || strings.Contains(id, "thread") || id != sessionID("opencode-go", "thread") || id == sessionID("opencode-zen", "thread") || id == sessionID("opencode-go", "other") || sessionID("opencode-go", " \t") != "" {
		t.Fatal("session affinity lost stable, trimmed service/thread isolation")
	}
}

func TestForwardReturnsRejectionWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			calls := 0
			rejected := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("provider rejection"))}
			client := &http.Client{Transport: inferenceTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("provider rejection triggered a retry")
				}
				if _, exists := r.Header["X-Opencode-Session"]; exists {
					t.Fatal("blank thread received session affinity")
				}
				return rejected, nil
			})}
			service := Service{Endpoint: "https://opencode.invalid/v1/chat/completions", APIKey: "key"}
			response, err := service.Forward(t.Context(), client, "chat", " \t", []byte(`{}`))
			if err != nil || response != rejected || calls != 1 {
				t.Fatalf("rejection was replaced or retried: calls=%d response=%v err=%v", calls, response, err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "provider rejection" {
				t.Fatalf("rejection body unavailable: %q err=%v", body, err)
			}
		})
	}
}
