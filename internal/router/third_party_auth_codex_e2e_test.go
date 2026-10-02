//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestThirdPartyAuthenticationNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for third-party authentication acceptance")
	}
	for _, unprefixed := range []bool{true, false} {
		model := "grok:grok-4.6"
		if unprefixed {
			model = "grok-4.6"
		}
		for _, websockets := range []bool{false, true} {
			transport := "http"
			if websockets {
				transport = "websocket"
			}
			t.Run(model+"/"+transport, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				defer cancel()
				environment := routerFaultCodexEnvironment(t)
				// An empty Codex home plus no environment credentials prevents a
				// developer's login from making this no-auth fixture pass.
				filtered := make([]string, 0, len(environment))
				for _, entry := range environment {
					key, _, _ := strings.Cut(entry, "=")
					if key != "OPENAI_API_KEY" && key != "CODEX_API_KEY" && key != "CHATGPT_API_KEY" {
						filtered = append(filtered, entry)
					}
				}
				catalogCommand := exec.CommandContext(ctx, codex, "debug", "models", "--bundled")
				catalogCommand.Env = filtered
				bundled, err := catalogCommand.Output()
				if err != nil {
					t.Fatalf("read installed Codex bundled catalog: %v", err)
				}
				catalog, err := ProviderModelCatalog(bundled, Session{GrokEnabled: true, GrokUnprefixed: unprefixed, ThirdPartyOnly: true})
				if err != nil {
					t.Fatal(err)
				}
				catalogPath := filepath.Join(t.TempDir(), "models.json")
				if err := os.WriteFile(catalogPath, catalog, 0o600); err != nil {
					t.Fatal(err)
				}
				var inferenceCalls, boundaryCalls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					inferenceCalls.Add(1)
					assertThirdPartyHeaderIsolation(t, r.Header, "grok-fixture-only")
					var wire map[string]any
					if err := json.UnmarshalRead(r.Body, &wire); err != nil || wire["model"] != "grok-4.6" {
						t.Errorf("invalid real adapter wire model: %v, %v", wire["model"], err)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, grokTextStream())
				}))
				defer upstream.Close()
				provider := newProviderClient("http://unused.invalid", nil)
				provider.thirdPartyOnly = true
				provider.grok = &grokClient{httpClient: grokTestHTTPClient(t, upstream), auth: newGrokAuth("", "grok-fixture-only"), unprefixed: unprefixed}
				mux := http.NewServeMux()
				endpoint := responsesWebSocketHandler(ctx, time.Minute, provider, nil, nil)
				defer endpoint.Close()
				mux.Handle("GET /v1/responses", endpoint)
				mux.HandleFunc("POST /v1/responses", responsesHandler(ctx, time.Minute, provider, nil, nil))
				router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					boundaryCalls.Add(1)
					if r.Header.Get("Authorization") != "" || r.Header.Get(chatGPTAccountIDHeader) != "" {
						t.Error("Codex attached authentication to third-party execution")
					}
					wantMethod := http.MethodPost
					if websockets {
						wantMethod = http.MethodGet
					}
					if r.Method != wantMethod || r.URL.Path != "/v1/responses" {
						t.Errorf("unexpected router boundary: %s %s, want %s responses", r.Method, r.URL.Path, wantMethod)
					}
					mux.ServeHTTP(w, r)
				}))
				defer router.Close()
				configuration := `model_providers.third_party_fixture={name="third_party_fixture",base_url=` + strconv.Quote(router.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false,supports_websockets=` + strconv.FormatBool(websockets) + `}`
				command := exec.CommandContext(ctx, codex,
					"-c", configuration, "-c", `model_provider="third_party_fixture"`,
					"-c", "model_catalog_json="+strconv.Quote(catalogPath),
					"-c", "tools.update_plan.enabled=false",
					"-c", "features.plugins=false",
					"--model", model, "--sandbox", "danger-full-access", "--ask-for-approval", "never",
					"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never", "-C", t.TempDir(), "Say GROK_OK without using tools.")
				command.Env = filtered
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Run(); err != nil {
					t.Fatalf("native third-party execution failed: %v\nstdout: %.8000s\nstderr: %.8000s", err, stdout.String(), stderr.String())
				}
				completed, _ := routerFaultNativeEventCount(t, stdout.String(), "turn.completed")
				if completed != 1 || inferenceCalls.Load() != 1 || boundaryCalls.Load() != 1 || !strings.Contains(stdout.String(), "GROK_OK") {
					t.Fatalf("incomplete native execution: completed=%d inference=%d boundary=%d\nstdout: %.8000s\nstderr: %.8000s", completed, inferenceCalls.Load(), boundaryCalls.Load(), stdout.String(), stderr.String())
				}
			})
		}
	}
}
