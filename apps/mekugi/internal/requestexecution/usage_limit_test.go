package requestexecution

import (
	"context"
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
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestUsageLimitWebSocketUpgradeWaitsThroughPrewarm(t *testing.T) {
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
			retained, err := history.NewService(control.RecordKinds())
			check(err)
			store, err := retained.Open(ctx, t.TempDir())
			check(err)
			session, err := store.SelectSession(ctx, nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
			check(err)
			defer session.Close()
			service := control.New()
			check(service.AdmitRoot(ctx, session, &control.Member{Thread: "root", Session: "session", Task: "/root", Turn: "turn", Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true}))
			reset := time.Now().Add(time.Hour).Truncate(time.Second)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", reset.UTC().Format(http.TimeFormat))
				if capacity {
					w.WriteHeader(http.StatusServiceUnavailable)
					io.WriteString(w, `{"error":{"code":"server_is_overloaded"}}`)
				} else {
					w.WriteHeader(http.StatusTooManyRequests)
					io.WriteString(w, `{"error":{"type":"usage_limit_reached"}}`)
				}
			}))
			defer provider.Close()
			client := inferencegateway.NewClient(provider.URL, provider.Client(), ProviderDiagnostics())
			executor := Executor{ThreadControl: service, Issues: testCriticalErrors()}
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
			for _, generate := range []bool{false, true} {
				body := `{"type":"response.create","model":"gpt-test","input":[],"stream":true}`
				if !generate {
					body = `{"type":"response.create","model":"gpt-test","input":[],"generate":false,"stream":true}`
				}
				check(conn.Write(ctx, websocket.MessageText, []byte(body)))
				for {
					_, body, err := conn.Read(ctx)
					check(err)
					if strings.Contains(string(body), `"type":"error"`) || strings.Contains(string(body), `"type":"response.failed"`) {
						t.Fatalf("usage limit reached host as an error: %s", body)
					}
					if strings.Contains(string(body), `"type":"response.completed"`) {
						break
					}
				}
				member, _ := service.Member("root")
				if !generate {
					if member.Wait != nil {
						t.Fatal("prewarm registered a usage wait")
					}
				} else if member.Wait == nil || !member.Wait.UsageLimit || member.Wait.Capacity != capacity || member.Wait.Deadline == nil || !member.Wait.Deadline.Equal(reset) {
					t.Fatalf("upgrade timing did not reach logical wait: %+v", member)
				}
			}

		})
	}
}
