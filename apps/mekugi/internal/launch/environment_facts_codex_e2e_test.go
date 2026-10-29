//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
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

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// The complete provider boundary compares native context, the data supplement,
// native hook environment, tool-shell environment and a nested standalone Codex.
func TestEnvironmentFactsNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex required:", err)
	}
	for _, key := range []string{"CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "HOME"} {
		t.Setenv(key, t.TempDir())
	}
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, "tracked"), "base")
	gitTestCommit(t, workspace)
	hook := filepath.Join(t.TempDir(), "environment-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'hook-capability=%s\\n' \"${MEKUGI_ENVIRONMENT_FACTS:-empty}\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hookCommand := quoteShellWord(hook)
	handler := fmt.Sprintf(`{type="command",command=%s,timeout=5,async=false,statusMessage="Environment fixture"}`, strconv.Quote(hookCommand))
	identity := map[string]any{"event_name": "session_start", "matcher": "^startup$", "hooks": []map[string]any{{"type": "command", "command": hookCommand, "timeout": 5, "async": false, "statusMessage": "Environment fixture"}}}
	encoded, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
	hookArgs := []string{"-c", "features.hooks=true", "-c", `hooks.SessionStart=[{matcher="^startup$",hooks=[` + handler + `]}]`, "-c", `hooks.state={"/<session-flags>/config.toml:session_start:0:0"={trusted_hash=` + strconv.Quote(hash) + `,enabled=true}}`}
	var nestedCalls atomic.Int32
	nested := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body bytes.Buffer
		if _, err := body.ReadFrom(r.Body); err != nil {
			t.Error(err)
			return
		}
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(body.Bytes(), &fields); err != nil {
			t.Error(err)
			return
		}
		if !bytes.Contains(body.Bytes(), []byte("hook-capability=empty")) || strings.Contains(responses.StringField(fields, "instructions"), "<mekugi_environment_facts>") {
			t.Error("nested standalone Codex did not retain its original hook context")
		}
		nestedCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"type": "message", "id": "nested-final", "role": "assistant", "status": "completed", "content": []map[string]any{{"type": "output_text", "text": "nested-accepted", "annotations": []any{}}}}
		for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": "nested-response", "status": "in_progress"}}, {"type": "response.output_item.added", "output_index": 0, "item": item}, {"type": "response.output_item.done", "output_index": 0, "item": item}, {"type": "response.completed", "response": map[string]any{"id": "nested-response", "status": "completed", "output": []any{item}}}} {
			fmt.Fprintf(w, "data: %s\n\n", responses.MustMarshalJSON(event))
		}
	}))
	defer nested.Close()
	nestedArgs := append([]string{"-c", `model_providers.nested={name="nested",base_url=` + strconv.Quote(nested.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="nested"`, "-c", "features.plugins=false"}, hookArgs...)
	nestedArgs = append(nestedArgs, "--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never", "exec", "--skip-git-repo-check", "--json", "--color", "never", "-C", workspace, "Nested fixture")
	var shell strings.Builder
	shell.WriteString(`test -z "$MEKUGI_ENVIRONMENT_FACTS" && printf 'tool-capability=empty\n'; ` + quoteShellWord(codex))
	for _, arg := range nestedArgs {
		shell.WriteString(" " + quoteShellWord(arg))
	}
	program := `const result = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(shell.String())) + `,workdir:` + string(responses.MustMarshalJSON(workspace)) + `,max_output_tokens:1500}); text(JSON.stringify(result));`
	proxy := newManagedMekugiProxy(t)
	provider := &toolFrontendCodexProvider{workspace: workspace, program: program, expected: []string{"tool-capability=empty", "nested-accepted", `\"exit_code\":0`}, finalText: "environment accepted"}
	var observed string
	provider.observeRequest = func(body []byte, _ http.Header) error {
		facts := environmentFacts(t, body)
		if observed != "" && observed != facts {
			return fmt.Errorf("native tool continuation rescanned environment facts")
		}
		observed = facts
		if !strings.Contains(facts, "Recent commits:\n") || !strings.Contains(facts, "\nPlatform: ") || strings.Contains(facts, workspace) {
			return fmt.Errorf("environment snapshot missing required facts")
		}
		for _, marker := range []string{"<environment_context>", "<cwd>", "<shell>", "<current_date>", "<timezone>", "<permissions instructions>", "hook-capability=1"} {
			if !bytes.Contains(body, []byte(marker)) {
				return fmt.Errorf("native provider context missing %s", marker)
			}
		}
		if bytes.Contains(body, []byte("test@example.com")) {
			return fmt.Errorf("provider input leaked Git identity")
		}
		return nil
	}
	server := httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	defer server.Close()
	args := append([]string{"-c", "features.plugins=false", "--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never"}, hookArgs...)
	args = append(args, "exec", "--skip-git-repo-check", "--json", "--color", "never", "-C", workspace, "Environment acceptance fixture")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codex, codexArgs(server.URL+"/v1", args, false, false)...)
	command.Dir, command.Env = workspace, codexEnvironmentCapability(os.Environ(), true)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native environment fixture: %v\n%s", err, output)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.resultSeen || nestedCalls.Load() != 1 || !bytes.Contains(output, []byte("environment accepted")) {
		t.Fatalf("native environment acceptance result=%t nested_calls=%d\n%s", provider.resultSeen, nestedCalls.Load(), output)
	}
}
