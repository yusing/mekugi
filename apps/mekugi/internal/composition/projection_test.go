package composition

import (
	"bytes"
	"context"
	"crypto/tls"
	json "encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/internal/router"
	"github.com/yusing/mekugi/modules/outputdeduplication"
)

func TestFrontendsRunSessionInputProjection(t *testing.T) {
	for _, tc := range []struct {
		name            string
		flags           []string
		projected       bool
		withoutGuidance bool
	}{
		{name: "default", projected: true},
		{name: "launch opt-out", flags: []string{"--deduplicate-output=false"}},
		{name: "guidance absent", projected: true, withoutGuidance: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR"} {
				t.Setenv(key, t.TempDir())
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			runtime, err := Frontends(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := runtime.Close(context.WithoutCancel(ctx)); err != nil {
					t.Error(err)
				}
			}()
			if len(runtime.InputProjections) != 1 {
				t.Fatalf("frontend projections = %d", len(runtime.InputProjections))
			}
			if runtime.Guidance == nil {
				t.Fatal("composed guidance is missing")
			}
			if tc.withoutGuidance {
				runtime.Guidance = nil
			}
			// Use the existing default-transport boundary to route the production
			// provider client exclusively to a local TLS WebSocket fixture.
			received := make(chan []byte, 1)
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				_, request, err := conn.Read(r.Context())
				if err != nil {
					t.Error(err)
					return
				}
				received <- request
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"local-response","status":"completed","output":[]}}`)); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			transport := upstream.Client().Transport.(*http.Transport).Clone()
			transport.Proxy = nil
			transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&tls.Dialer{Config: transport.TLSClientConfig}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			originalTransport := http.DefaultTransport
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = originalTransport; transport.CloseIdleConnections() }()

			options, err := router.ParseSessionConfig(append([]string{"--ansi-faint=off"}, tc.flags...))
			if err != nil {
				t.Fatal(err)
			}
			ready := make(chan router.Session, 1)
			done := make(chan error, 1)
			go func() {
				defer close(done)
				done <- router.RunSession(runtime.History, runtime.Recovery, runtime.Segments, runtime.Changes,
					ctx, runtime.Commands, runtime.InputProjections, runtime.Guidance, options, router.NewCriticalErrors(),
					func(session router.Session) { ready <- session }, nil)
			}()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			var session router.Session
			select {
			case session = <-ready:
			case err := <-done:
				t.Fatalf("session stopped before ready: %v", err)
			case <-ctx.Done():
				t.Fatal("session did not become ready")
			}
			body := strings.Repeat("snapshot line "+strings.Repeat("x", 80)+"\n", 3)
			input := []map[string]any{
				{"type": "message", "role": "developer", "content": "Keep policy.\n- You share updates in the `commentary` channel.\n"},
				{"type": "custom_tool_call_output", "call_id": "source", "output": body},
				{"type": "custom_tool_call_output", "call_id": "repeat", "output": body},
			}
			tools := []map[string]any{{"type": "custom", "name": "exec", "description": "Run JavaScript with tools.\n### `exec_command`\nexec_command(args: { cmd: string }): Promise<unknown>;\n### `apply_patch`\napply_patch(input: string): Promise<unknown>;"}}
			const instructions = "Keep authority.<!-- mekugi:omit -->omit me<!-- /mekugi:omit -->"
			payload, err := json.Marshal(map[string]any{"model": "local-test", "stream": true, "instructions": instructions, "input": input, "tools": tools})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, session.BaseURL+"/responses", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer local-test")
			request.Header.Set("Chatgpt-Account-Id", "local-account")
			request.Header.Set("session-id", "projection-session")
			request.Header.Set("thread-id", "projection-thread")
			request.Header.Set("x-codex-turn-metadata", `{"request_kind":"turn"}`)
			client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 10 * time.Second}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			responseBody, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(responseBody, []byte("response.completed")) {
				t.Fatalf("response status = %d, body = %s, error = %v", response.StatusCode, responseBody, err)
			}
			var wire []byte
			select {
			case wire = <-received:
			case <-ctx.Done():
				t.Fatal("model request was not delivered")
			}
			var modelRequest struct {
				Instructions string `json:"instructions"`
				Tools        []struct {
					Description string `json:"description"`
				} `json:"tools"`
				Input []struct {
					Output  string `json:"output"`
					Content string `json:"content"`
				} `json:"input"`
			}
			if err := json.Unmarshal(wire, &modelRequest); err != nil {
				t.Fatal(err)
			}
			wantSource, wantRepeat := body, body
			if tc.projected {
				wantSource, wantRepeat = "[O1]\n"+body, "[same as O1]\n"
			}
			if len(modelRequest.Input) != 3 || modelRequest.Input[1].Output != wantSource || modelRequest.Input[2].Output != wantRepeat {
				t.Fatalf("provider input = %+v", modelRequest.Input)
			}
			wantInstructions, wantContent := "Keep authority.", "Keep policy.\n\n"
			if tc.withoutGuidance {
				wantInstructions, wantContent = instructions, input[0]["content"].(string)
			}
			if modelRequest.Instructions != wantInstructions || modelRequest.Input[0].Content != wantContent {
				t.Fatalf("provider guidance: instructions=%q content=%q", modelRequest.Instructions, modelRequest.Input[0].Content)
			}
			if len(modelRequest.Tools) != 1 || strings.Contains(modelRequest.Tools[0].Description, "<!-- mekugi-frontends:start -->") == tc.withoutGuidance {
				t.Fatalf("provider command guidance: %+v", modelRequest.Tools)
			}
		})
	}
}

func TestCompositionInputProjectionRemoval(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var c Composition
		if enabled {
			c.Add(outputdeduplication.Module, func() outputdeduplication.Deps { return outputdeduplication.Deps{} })
		}
		runtime, err := c.Activate(t.Context(), new(binding.Registry))
		if err != nil {
			t.Fatal(err)
		}
		if enabled {
			if len(runtime.InputProjections) != 1 || runtime.InputProjections[0].Name != "outputdeduplication" {
				t.Fatalf("module registration = %+v", runtime.InputProjections)
			}
		} else if len(runtime.InputProjections) != 0 {
			t.Fatal("omitted module registered an input projection")
		}
		if err := runtime.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
