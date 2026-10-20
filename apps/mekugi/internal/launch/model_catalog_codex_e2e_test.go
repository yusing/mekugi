//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

// Real Codex consumes the account catalog through the same handler and launch
// overrides as production, then executes a stock JavaScript command through
// Mekugi's normal request admission. No external provider is contacted.
func TestCodeModeOnlyCatalogNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	environment := builtInOpenAICodexEnvironment(t)
	// ChatGPT discovery follows the invocation's local OpenAI URL. API-key
	// discovery deliberately skips overridden base URLs in stock Codex.
	var configRoot string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			configRoot = value
		}
	}
	claims := `{"email":"fixture@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account","chatgpt_plan_type":"pro"}}`
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".c2ln"
	auth, err := json.Marshal(map[string]any{
		"tokens":       map[string]string{"id_token": token, "access_token": "fixture-access", "refresh_token": "fixture-refresh", "account_id": "fixture-account"},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	bundled := exec.CommandContext(ctx, codex, "debug", "models", "--bundled")
	bundled.Env = environment
	body, err := bundled.Output()
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]jsontext.Value `json:"models"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models {
		model["tool_mode"] = jsontext.Value(`"code_mode"`)
	}
	body, err = json.Marshal(&catalog)
	if err != nil {
		t.Fatal(err)
	}
	var discoveries atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected upstream request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		discoveries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "mixed-account-catalog")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	client := newTestProviderClient(upstream.URL, upstream.Client())
	// Seed the shared host cache with the unsupported account metadata from a
	// previous invocation. The new invocation's URL must prevent its reuse.
	previous := httptest.NewServer(inferencegateway.ModelsHandler(client, false, nil))
	defer previous.Close()
	seed := exec.CommandContext(ctx, codex, "-c", "openai_base_url="+strconv.Quote(previous.URL+"/v1"), "debug", "models")
	seed.Env = environment
	seeded, err := seed.Output()
	if err != nil || !bytes.Contains(seeded, []byte(`"tool_mode":"code_mode"`)) {
		t.Fatalf("seed unsupported account catalog: %v", err)
	}
	models := inferencegateway.ModelsHandler(client, true, nil)
	workspace := t.TempDir()
	fixture := &toolFrontendCodexProvider{
		workspace: workspace, program: `text(await tools.exec_command({cmd:"printf MODE_OK"}));`,
		expected: []string{"MODE_OK"}, finalText: "catalog host accepted",
		observeRequest: func(body []byte, _ http.Header) error {
			request, err := codexresponses.ParseRequest(body)
			if err != nil {
				return err
			}
			_, err = binding.PrepareStockExecution(request.Fields, codexresponses.DecodeToolCatalog(request.Fields), "", nil)
			return err
		},
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	inference := testResponsesHandler(ctx, time.Minute, fixture, nil, proxy)
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key, values := range codexAuthHeaders() {
			r.Header[key] = values
		}
		if r.URL.Path == "/v1/models" {
			models(w, r)
		} else if r.URL.Path == "/v1/responses" && r.Method == http.MethodGet {
			http.Error(w, "fixture requires HTTP", http.StatusUpgradeRequired)
		} else if r.URL.Path == "/v1/responses" && r.Method == http.MethodPost {
			inference.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer router.Close()
	args := codexArgs(router.URL+"/v1", []string{
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "-C", workspace,
		"-c", "features.plugins=false",
		"-c", "features.code_mode_host=true",
		"-c", "chatgpt_base_url=" + strconv.Quote(router.URL),
		"-c", "model_reasoning_effort=" + strconv.Quote("low"),
		"Run the execution interface fixture.",
	}, true, false, true)
	cmd := exec.CommandContext(ctx, codex, args...)
	cmd.Env, cmd.Dir = environment, filepath.Dir(workspace)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("native catalog fixture: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if discoveries.Load() < 2 || !fixture.resultSeen || fixture.turns != 2 {
		t.Fatalf("catalog/host execution: discoveries=%d result=%t turns=%d\nstdout: %s\nstderr: %s",
			discoveries.Load(), fixture.resultSeen, fixture.turns, &stdout, &stderr)
	}
}
