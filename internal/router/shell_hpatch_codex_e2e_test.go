//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/shellruntime"
)

type shellEditCodexProvider struct {
	mu         sync.Mutex
	turns      int
	resultSeen bool
}

func (p *shellEditCodexProvider) forwardExecution(_, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns++
	var item map[string]any
	switch p.turns {
	case 1:
		item = map[string]any{
			"type": "custom_tool_call", "id": "edit-item", "call_id": "edit-call",
			"name": "shell", "namespace": "functions", "status": "completed",
			"input": "hpatch native.txt \"$(python3 generator.py)\"",
		}
	case 2:
		var request struct {
			Input []map[string]jsonv1.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		for _, input := range request.Input {
			if jsonString(input, "type") == "custom_tool_call_output" && jsonString(input, "call_id") == "edit-call" {
				output := string(input["output"])
				p.resultSeen = strings.Contains(output, "change ") && strings.Contains(output, "native success")
			}
		}
		if !p.resultSeen {
			return nil, fmt.Errorf("native shell edit result missing")
		}
		item = map[string]any{
			"type": "function_call", "id": "finish-item", "call_id": "finish-call",
			"name": "journal", "namespace": "functions", "status": "completed",
			"arguments": `{"op":"finish","journal":[{"op":"add","text":"native hpatch complete","answer":true}]}`,
		}

	default:
		return nil, fmt.Errorf("unexpected provider turn %d", p.turns)
	}
	response := map[string]any{
		"id": fmt.Sprintf("edit-response-%d", p.turns), "status": "completed", "output": []any{item},
		"usage": map[string]any{"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 0},
			"output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 15},
	}

	wire := ""
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": response},
	} {
		if event["type"] == "response.output_item.done" && item["type"] == "function_call" {
			wire += "data: " + string(mustMarshalJSON(map[string]any{
				"type": "response.function_call_arguments.done", "item_id": item["id"], "arguments": item["arguments"],
			})) + "\n\n"
		}
		wire += "data: " + string(mustMarshalJSON(event)) + "\n\n"
	}

	result := serverHTTPResponse(wire)
	result.Header.Set("Content-Type", "text/event-stream")
	return result, nil
}

func TestShellHpatchNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	// Codex advertises a live-view workspace for repositories, not bare /tmp directories.
	workspace := t.TempDir()
	if output, err := exec.CommandContext(t.Context(), "git", "init", "--quiet", workspace).CombinedOutput(); err != nil {
		t.Fatalf("initialize disposable repository: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(workspace, "native.txt"), []byte("old old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "generator.py"), []byte("print('type \"old\" \"native success\" 2')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &shellEditCodexProvider{}
	proxy := newManagedMekugiProxy(t)
	manifest, err := readToolWorkerManifest(filepath.Join(proxy.registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore, err = openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var previewMu sync.Mutex
	var previews []liveDiffPreview
	proxy.commentary.previewPublisher = func(selected, thread, author string, preview liveDiffPreview) {
		previewMu.Lock()
		defer previewMu.Unlock()
		previews = append(previews, preview)
		if selected != workspace || thread == "" || author != "" && author != "/root" {
			t.Errorf("pre-write attribution: workspace=%q thread=%q author=%q", selected, thread, author)
		}
		if !preview.Complete {
			before, readErr := os.ReadFile(filepath.Join(workspace, "native.txt"))
			if readErr != nil || string(before) != "old old\n" {
				t.Errorf("pre-write preview arrived after the write: %q, %v", before, readErr)
			}
		}
	}
	issues := NewCriticalErrors()
	responses := responsesHandler(t.Context(), time.Minute, provider, issues, proxy, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == commentaryPublisherPath {
			record := httptest.NewRecorder()
			proxy.commentary.serveHTTP(record, r)
			if record.Code >= 400 {
				t.Errorf("runtime publication rejected: HTTP %d: %s", record.Code, record.Body.String())
			}
			for key, values := range record.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(record.Code)
			_, _ = w.Write(record.Body.Bytes())
			return

		}
		responses.ServeHTTP(w, r)
	}))
	defer server.Close()
	proxy.commentaryEndpoint = server.URL + commentaryPublisherPath

	helperDirectory := t.TempDir()
	build := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(helperDirectory, "shell"), "../../cmd/shell")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shell: %v\n%s", err, output)
	}
	t.Setenv("PATH", helperDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(shellruntime.RuntimeDirectoryEnvironment, proxy.shellDirectory)
	t.Setenv("MEKUGI_JOURNAL_E2E_WORKER", "1")
	config := `model_providers.edit_fixture={name="edit_fixture",base_url=` + strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "-c", config, "-c", `model_provider="edit_fixture"`,
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"-C", workspace, "Exercise the native shell edit fixture.")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Codex: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	content, err := os.ReadFile(filepath.Join(workspace, "native.txt"))
	if err != nil || string(content) != "native success native success\n" {
		t.Fatalf("actual edit %q: %v", content, err)
	}
	previewMu.Lock()
	defer previewMu.Unlock()
	if len(previews) != 2 || previews[0].Complete || !previews[1].Complete ||
		!previews[0].Evaluated || len(previews[0].Files) != 1 ||
		!strings.Contains(previews[0].Files[0].Diff, "+native success native success") {
		t.Fatalf("native pre-write snapshots: %+v", previews)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 2 || !provider.resultSeen || !strings.Contains(stdout.String(), `"exit_code":0`) || !strings.Contains(stdout.String(), "native hpatch complete") {
		t.Fatalf("native acceptance: turns=%d result=%t stdout=%s", provider.turns, provider.resultSeen, stdout.String())
	}
	issues.mu.Lock()
	defer issues.mu.Unlock()
	if len(issues.entries) != 0 {
		t.Fatalf("critical errors: %+v", issues.entries)
	}
}
