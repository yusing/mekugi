package requestexecution

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestUsageLimitWebSocketUpgradeReachesNativeOwner(t *testing.T) {
	for _, capacity := range []bool{false, true} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			reset := time.Now().Add(time.Hour).Truncate(time.Second)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", reset.UTC().Format(http.TimeFormat))
				if capacity {
					w.WriteHeader(http.StatusServiceUnavailable)
					io.WriteString(w, `{"error":{"code":"server_is_overloaded"},"headers":{"x-provider-fixture":"body"}}`)
				} else {
					w.WriteHeader(http.StatusTooManyRequests)
					io.WriteString(w, `{"error":{"type":"usage_limit_reached"},"headers":{"x-provider-fixture":"body"}}`)
				}
			}))
			defer provider.Close()
			client := inferencegateway.NewClient(provider.URL, provider.Client(), ProviderDiagnostics())
			executor := Executor{Issues: testCriticalErrors()}
			endpoint := inferencegateway.NewWebSocketEndpoint(ctx, time.Second, client, executor.ExecuteWebSocket)
			defer endpoint.Close()
			server := httptest.NewServer(endpoint)
			defer server.Close()
			headers := http.Header{"Authorization": {"Bearer fixture"}, "Chatgpt-Account-Id": {"fixture"}}
			headers.Set(codexresponses.ThreadIDHeader, "root")
			headers.Set(codexresponses.TurnMetadataHeader, `{"thread_id":"root","turn_id":"turn","request_kind":"turn"}`)
			conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: headers})
			check(err)
			defer conn.CloseNow()
			for _, generate := range []bool{false, true, true} {
				body := `{"type":"response.create","model":"gpt-test","input":[],"stream":true}`
				if !generate {
					body = `{"type":"response.create","model":"gpt-test","input":[],"generate":false,"stream":true}`
				}
				check(conn.Write(ctx, websocket.MessageText, []byte(body)))
				for {
					_, body, err := conn.Read(ctx)
					check(err)
					rejected := strings.Contains(string(body), `"type":"error"`) || strings.Contains(string(body), `"type":"response.failed"`)
					completed := strings.Contains(string(body), `"type":"response.completed"`)
					if generate && completed || !generate && rejected {
						t.Fatalf("native provider outcome changed: %s", body)
					}
					if generate && rejected {
						var event struct {
							Status int `json:"status"`
							Error  struct {
								Type string `json:"type"`
								Code string `json:"code"`
							} `json:"error"`
							Headers map[string]string `json:"headers"`
						}
						check(json.Unmarshal(body, &event))
						status := http.StatusTooManyRequests
						if capacity {
							status = http.StatusServiceUnavailable
						}
						if event.Status != status || event.Headers["retry-after"] != reset.UTC().Format(http.TimeFormat) || event.Headers["x-provider-fixture"] != "body" || capacity && event.Error.Code != "server_is_overloaded" || !capacity && event.Error.Type != "usage_limit_reached" {
							t.Fatalf("native owner lost original rejection or retry timing: %s", body)
						}
					}
					if rejected || completed {
						break
					}
				}

			}

		})
	}
}
