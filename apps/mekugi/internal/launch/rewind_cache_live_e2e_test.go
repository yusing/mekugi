//go:build codex_e2e && e2e

package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/openai"
)

// Verify the first request after native same-thread history replacement, not a follow-up hit.
func TestRewindCacheEligibilityLiveOpenAI(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("MEKUGI_E2E_MODEL"))
	if model == "" {
		t.Fatal("MEKUGI_E2E_MODEL must be set")
	}
	configRoot := os.Getenv("CODEX_HOME")
	if configRoot == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		configRoot = filepath.Join(user, ".codex")
	}
	auth, err := os.ReadFile(filepath.Join(configRoot, "auth.json"))
	if err != nil {
		t.Fatal("live Codex authentication required")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	upstream := http.DefaultTransport.(*http.Transport).Clone()
	defer upstream.CloseIdleConnections()
	transport := &cacheAffinityE2ETransport{delegate: upstream}
	proxy := newManagedMekugiProxy(t)
	handler := testResponsesHandler(t.Context(), 3*time.Minute,
		newTestProviderClient(openai.BaseURL, &http.Client{Transport: transport}), nil, proxy)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "use HTTP", http.StatusUpgradeRequired)
			return
		}
		handler(w, r)
	}))
	defer server.Close()
	environment := builtInOpenAICodexEnvironment(t)
	for _, entry := range environment {
		if path, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			if err := os.WriteFile(filepath.Join(path, "auth.json"), auth, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	workspace := t.TempDir()
	terminal := startAppResumeTerminalWithIntegration(t, func(ctx context.Context) *exec.Cmd {
		args := codexArgs(server.URL+"/v1", []string{"app-server", "-c", fmt.Sprintf("model=%q", model),
			"-c", "features.plugins=false", "-c", `model_reasoning_effort="low"`}, false, true, nil)
		cmd := exec.CommandContext(ctx, "codex", args...)
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "", testAppServerIntegration(proxy))

	terminal.await("Ready")
	for _, parts := range []string{"KEEP, _, READY", "KEEP, _, WARM", "DROP, _, ANSWER"} {
		terminal.send(parts + ". Without using tools, join those parts with no spaces and reply with only the result.\r")
		terminal.await(strings.ReplaceAll(parts, ", ", ""))
		terminal.await("╭─ Completed")
	}
	seed, _, headers := transport.snapshot()
	if len(seed) != 3 {
		t.Fatalf("want three seed requests, got %d", len(seed))
	}
	for i, usage := range seed {
		t.Logf("seed request %d: input=%d cached=%d", i+1, usage.InputTokens, usage.InputTokens-usage.UncachedInputTokens)
	}
	if seed[1].InputTokens <= seed[1].UncachedInputTokens {
		t.Fatal("Main did not establish a cached prefix before rewind")
	}
	thread := codexresponses.ThreadID(headers[1])
	terminal.send("/rewind\r")
	terminal.awaitMatch("loaded rewind choices", func(frame string) bool {
		return strings.Contains(frame, "Rewind before a user turn") && strings.Contains(frame, "› KEEP, _, READY.")
	})
	// The picker lists oldest first. Drop only the latest user turn.
	terminal.send("\x1b[B\x1b[B\r")
	terminal.awaitMatch("rewound history", func(frame string) bool {
		return strings.Contains(frame, "KEEP_WARM") && !strings.Contains(frame, "DROP_ANSWER") &&
			!strings.Contains(frame, "Rewind before a user turn") && strings.Contains(frame, "Ready")
	})
	terminal.send("Without using tools, join these parts with no spaces and reply with only the result: AFTER, _, REWIND.\r")
	terminal.await("AFTER_REWIND")
	terminal.await("╭─ Completed")
	usages, _, headers := transport.snapshot()
	bodies := transport.requestBodies()
	if len(usages) != 4 || len(bodies) != 4 {
		t.Fatalf("want one first post-rewind request: usages=%d bodies=%d", len(usages), len(bodies))
	}
	first := usages[3]
	t.Logf("first post-rewind request: input=%d cached=%d", first.InputTokens, first.InputTokens-first.UncachedInputTokens)
	if thread == "" || codexresponses.ThreadID(headers[3]) != thread {
		t.Error("rewind changed native thread identity")
	}
	if bytes.Contains(bodies[3], []byte("DROP")) || !bytes.Contains(bodies[3], []byte("KEEP_WARM")) {
		t.Error("first post-rewind request lost retained history or included excluded history")
	}

	before, err := codexresponses.ParseRequest(bodies[1])
	if err != nil {
		t.Fatal(err)
	}
	after, err := codexresponses.ParseRequest(bodies[3])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"instructions", "tools", "model", "reasoning", "prompt_cache_key"} {
		equal := responses.SameJSONValue(before.Fields[field], after.Fields[field])
		t.Logf("post-rewind %s unchanged=%t", field, equal)
		if !equal {
			t.Errorf("rewind changed shared %s", field)
		}
	}
	if headers[1].Get(codexresponses.SessionIDHeader) != headers[3].Get(codexresponses.SessionIDHeader) {
		t.Error("rewind changed provider session affinity")
	}
	var oldItems, newItems []jsontext.Value
	if err := json.Unmarshal(before.Fields["input"], &oldItems); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after.Fields["input"], &newItems); err != nil {
		t.Fatal(err)
	}
	if len(oldItems) == 0 || len(newItems) <= len(oldItems) {
		t.Fatal("rewind lost retained input or its new question")
	}
	for i := range oldItems {
		if !responses.SameJSONValue(oldItems[i], newItems[i]) {
			t.Errorf("rewind changed retained input item %d", i)
		}
	}
	t.Logf("checked %d retained input items; post-rewind items=%d", len(oldItems), len(newItems))

	if first.InputTokens <= first.UncachedInputTokens {
		t.Error("first post-rewind request had no cached input despite warmed prefix")
	}
	terminal.stopCanceled()
}
