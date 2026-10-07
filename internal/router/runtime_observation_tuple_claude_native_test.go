package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// The installed native consumer owns Bash and permission handling. Every provider
// response is scripted locally: this matrix makes no inference or billing request.
func TestNativeBashTupleClaudeScripted(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; no model inference")
	}
	nativeAcceptanceSettingsUnchanged(t)
	for _, mode := range []string{"default", "acceptEdits"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			t.Setenv(routerTestWorkerEnvironment, "1")
			t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			t.Setenv("ANTHROPIC_API_KEY", "local-tuple-fixture")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
			nativeAcceptanceSettingsUnchanged(t)
			cases := []struct{ name, input string }{
				{"missing_optional_args", `{"command":"printf plain"}`},
				{"description", `{"command":"printf described","description":"Print text"}`},
				{"huge_timeout", `{"command":"printf timeout","timeout":999999999,"description":"Print text"}`},
				{"explicit_false_defaults", `{"command":"printf defaults","run_in_background":false,"dangerouslyDisableSandbox":false,"timeout":1000}`},
				{"string_coercion", `{"command":"printf coerced","run_in_background":"false","timeout":"1000"}`},
				{"escape_normalization", `{"command":"find . -maxdepth 0 -exec printf normalized \\\\;","description":"Print from find"}`},
				{"mkdir", `{"command":"mkdir tuple-dir","description":"Create directory","timeout":1000,"run_in_background":false}`},
				{"write", `{"command":"printf 'old\\n' > sed-target.txt"}`},
				{"sed", `{"command":"sed -i 's/old/new/' sed-target.txt","description":"Edit fixture","timeout":1000}`},
				{"background", `{"command":"printf background","run_in_background":true,"description":"Print background text"}`},
			}
			inputs := make([]map[string]any, len(cases))
			var wantEffects strings.Builder
			for i, tc := range cases {
				if err := json.Unmarshal([]byte(tc.input), &inputs[i]); err != nil {
					t.Fatal(err)
				}
				// Append-only markers detect replay, including for read-only shapes.
				inputs[i]["command"] = inputs[i]["command"].(string) + fmt.Sprintf(" && printf '%s\\n' >> tuple-effects.txt", tc.name)
				fmt.Fprintln(&wantEffects, tc.name)
			}
			var mu sync.Mutex
			issued, requests := 0, 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				var packet map[string]any
				if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				requests++
				if requests > 32 {
					t.Error("native tuple fixture exceeded its bounded request budget")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				content := []any{map[string]any{"type": "text", "text": "OK"}}
				stop := "end_turn"
				if tools, _ := packet["tools"].([]any); len(tools) > 0 && issued < len(cases) {
					content = []any{map[string]any{"type": "tool_use", "id": "tuple-" + mode + "-" + cases[issued].name, "name": "Bash", "input": inputs[issued]}}
					issued++
					stop = "tool_use"
				}
				nativeGuidanceProviderReply(w, packet, content, stop)
			}))
			defer provider.Close()
			t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
			service, binding, _ := observationHTTPFixture(t)
			trace := traceNativeObservation(t, service)
			settings := filepath.Join(binding.Workspace, ".claude", "settings.local.json")
			if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
				t.Fatal(err)
			}
			settingsContent := []byte(`{"permissions":{"defaultMode":"` + mode + `"}}`)
			if err := os.WriteFile(settings, settingsContent, 0600); err != nil {
				t.Fatal(err)
			}
			presentation, err := service.PrepareCompanion(ctx)
			if err != nil {
				t.Fatal(err)
			}
			bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
			if err != nil {
				t.Fatal(err)
			}
			executable, err := exec.LookPath("claude")
			if err != nil {
				t.Fatal(err)
			}
			endpoint := service.Endpoint()
			client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{
				Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema,
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			completed := false
			for !completed {
				select {
				case <-ctx.Done():
					t.Fatal("native tuple fixture timed out")
				case event, ok := <-client.Events():
					if !ok {
						t.Fatal("native bridge ended before tuple acceptance")
					}
					switch event.Kind {
					case "ready":
						if err := client.Send(ctx, "Execute the local fixture Bash calls."); err != nil {
							t.Fatal(err)
						}
					case "prompt":
						if event.Prompt == nil || event.Prompt.Tool != "Bash" {
							t.Fatal("unexpected native permission request")
						}
						if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: true}); err != nil {
							t.Fatal(err)
						}
					case "notice":
						if strings.Contains(strings.ToLower(event.Text), "capture unavailable") {
							t.Fatal(event.Text)
						}
					case "error":
						t.Fatal(event.Text)
					case "done":
						if event.Failed {
							t.Fatal("scripted native tuple turn failed")
						}
						completed = true
					}
				}
			}
			mu.Lock()
			gotIssued := issued
			mu.Unlock()
			if gotIssued != len(cases) {
				t.Fatalf("issued Bash calls=%d, want %d", gotIssued, len(cases))
			}
			trace.mu.Lock()
			defer trace.mu.Unlock()
			if len(trace.before) != len(cases) || len(trace.after) != len(cases) {
				t.Fatalf("hook counts: before=%d after=%d, want %d each", len(trace.before), len(trace.after), len(cases))
			}
			for _, tc := range cases {
				id := "tuple-" + mode + "-" + tc.name
				before, preOK := trace.before[id]
				after, postOK := trace.after[id]
				if !preOK || !postOK || before.Tool != "Bash" || !sameObservationCall(&before, &after) {
					t.Errorf("%s: complete, identical native Pre/Post Bash tuple missing", tc.name)
				}
			}
			for name, want := range map[string]string{"tuple-effects.txt": wantEffects.String(), "sed-target.txt": "new\n"} {
				data, err := os.ReadFile(filepath.Join(binding.Workspace, name))
				if err != nil || string(data) != want {
					t.Errorf("%s: exactly-once effect mismatch: got %q, want %q, error %v", name, data, want, err)
				}
			}
			if info, err := os.Stat(filepath.Join(binding.Workspace, "tuple-dir")); err != nil || !info.IsDir() {
				t.Errorf("native mkdir effect missing: %v", err)
			}
			if data, err := os.ReadFile(settings); err != nil || string(data) != string(settingsContent) {
				t.Errorf("invocation-local native permission settings changed: %v", err)
			}
		})
	}
}
