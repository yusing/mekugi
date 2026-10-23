package launch

import (
	"bytes"
	"context"
	"crypto/tls"
	"github.com/yusing/mekugi/contracts/command"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	json "encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/outputdeduplication"
)

func TestFrontendsRunSessionLivePreviewInputProjection(t *testing.T) {
	for _, tc := range []struct {
		name            string
		config          string
		flags           []string
		projected       bool
		withoutGuidance bool
		withoutPreview  bool
	}{
		{name: "default", projected: true},
		{name: "launch opt-out", flags: []string{"--deduplicate-output=false"}},
		{name: "file opt-out", config: "deduplicate_output = false"},
		{name: "CLI overrides file", config: "deduplicate_output = false", flags: []string{"--deduplicate-output"}, projected: true},
		{name: "guidance absent", projected: true, withoutGuidance: true},
		{name: "preview absent", projected: true, withoutPreview: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR"} {
				t.Setenv(key, t.TempDir())
			}
			if tc.config != "" {
				writeFeatureConfig(t, tc.config)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			directory, err := history.ReplayDirectory()
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := composition.Frontends(ctx, false, true, directory, nil)
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
			if runtime.LivePreview == nil {
				t.Fatal("composed preview handle is missing")
			}
			if tc.withoutPreview {
				runtime.LivePreview = nil
			}
			if tc.withoutGuidance {
				runtime.Guidance = nil
			}
			// Use the existing default-transport boundary to route the production
			// provider client exclusively to a local TLS HTTP fixture.
			received := make(chan []byte, 1)
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				received <- request
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"local-response\",\"status\":\"completed\",\"output\":[]}}\n\n")
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

			options, err := launchoptions.Parse(append([]string{"--ansi-faint=off"}, tc.flags...))
			if err != nil {
				t.Fatal(err)
			}
			ready := make(chan invocationFixture, 1)
			done := make(chan error, 1)
			go func() {
				defer close(done)
				done <- runFixtureInvocation(runtime.Terminal, runtime.Presentation, runtime.Conversation, runtime.LivePreview, runtime.Activity, runtime.History, runtime.Preferences, runtime.Recovery, runtime.Segments, runtime.Changes, runtime.Usage, runtime.AgentMetrics, runtime.TransportDiagnostics, runtime.Diagnostics, ctx, runtime.Commands, runtime.InputProjections, runtime.Guidance, runtime.Naming, runtime.Observation, runtime.Guards, options, requestexecution.NewFailures(runtime.Notifications), func(session invocationFixture) { ready <- session }, nil)
			}()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			var session invocationFixture
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
			if !tc.withoutGuidance {
				specs := make([]command.Spec, len(runtime.Commands))
				for i, contribution := range runtime.Commands {
					specs[i] = contribution.Spec
				}
	guide, err := runtime.Guidance.Frontends(specs)
				if err != nil {
					t.Fatal(err)
				}
				wantInstructions += "\n\n" + guide.Workflow
			}
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
		var c composition.Composition
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

func writeFeatureConfig(t *testing.T, features string) string {
	t.Helper()
	directory := os.Getenv("XDG_CONFIG_HOME")
	if actual, _ := os.UserConfigDir(); actual != directory || directory == "" {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	if err := os.MkdirAll(filepath.Join(directory, "mekugi"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "mekugi", "config.toml")
	if err := os.WriteFile(path, []byte("[features]\n"+features+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
