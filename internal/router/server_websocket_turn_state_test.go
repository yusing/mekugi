package router

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestResponsesWebSocketBridgesUpgradeTurnStateOnce(t *testing.T) {
	for _, handshakeToken := range []string{"", "upstream-state"} {
		t.Run("token="+handshakeToken, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-codex-turn-state", handshakeToken)
				w.Header().Set("x-unrelated-provider-header", "must-not-leak")
				upstream, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer upstream.CloseNow()
				ids := []string{"warm", "echo", "omit", "replace"}
				for i, id := range ids {
					request, err := providerSocketRead(ctx, upstream)
					if err != nil {
						t.Error(err)
						return
					}
					if i == 0 {
						if string(request["generate"]) != "false" {
							t.Errorf("prewarm generated: %s", mustMarshalJSON(request))
						}
					} else {
						if got := jsonString(request, "previous_response_id"); got != ids[i-1] {
							t.Errorf("request %d parent = %q, want %q", i, got, ids[i-1])
						}
						var metadata map[string]string
						if value := request["client_metadata"]; len(value) != 0 {
							if err := json.Unmarshal(value, &metadata); err != nil {
								t.Errorf("request %d metadata: %v", i, err)
							}
						}
						want := ""
						if i == 1 {
							want = handshakeToken
						} else if i == 3 {
							want = "new-client-state"
						}
						if got := metadata["x-codex-turn-state"]; got != want {
							t.Errorf("request %d turn state = %q, want %q", i, got, want)
						}
						if i == 2 {
							if _, present := metadata["x-codex-turn-state"]; present {
								t.Error("omitted client turn state was restored from handshake")
							}
						}
					}
					if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
						t.Error(err)
						return
					}
				}
				_, _, _ = upstream.Read(ctx)
			}), nil, codexAuthHeaders())

			for i, id := range []string{"warm", "echo", "omit", "replace"} {
				request := map[string]any{"type": "response.create", "model": "gpt-test", "input": []any{map[string]string{"role": "user", "content": id}}}
				if i == 0 {
					request["generate"] = false
				} else {
					request["previous_response_id"] = []string{"warm", "echo", "omit"}[i-1]
				}
				if i == 1 && handshakeToken != "" {
					request["client_metadata"] = map[string]string{"x-codex-turn-state": handshakeToken}
				} else if i == 3 {
					request["client_metadata"] = map[string]string{"x-codex-turn-state": "new-client-state"}
				}
				socketWrite(t, ctx, conn, request)
				event := socketRead(t, ctx, conn)
				if i == 0 && handshakeToken != "" {
					if got := jsonString(event, "type"); got != "response.metadata" {
						t.Fatalf("first event = %q, want response.metadata: %s", got, mustMarshalJSON(event))
					}
					var headers map[string]string
					if err := json.Unmarshal(event["headers"], &headers); err != nil {
						t.Fatal(err)
					}
					if len(headers) != 1 || headers["x-codex-turn-state"] != handshakeToken {
						t.Fatalf("bridged headers = %v, want only turn state", headers)
					}
					event = socketRead(t, ctx, conn)
				}
				if got := jsonString(event, "type"); got != "response.completed" {
					t.Fatalf("request %d event = %q, want completed: %s", i, got, mustMarshalJSON(event))
				}
			}
		})
	}
}
