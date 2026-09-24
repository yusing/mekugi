//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type routerFaultCodexProvider struct {
	mu        sync.Mutex
	mode      string
	requests  int
	toolNames []string
}

func (provider *routerFaultCodexProvider) forwardExecution(_, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	provider.mu.Lock()
	provider.requests++
	request := provider.requests
	if request == 1 {
		provider.toolNames = routerFaultCodexToolNames(body)
	}
	provider.mu.Unlock()

	if provider.mode == "transform" {
		if request == 1 {
			return routerFaultCodexTransformResponse(), nil
		}
		// If the terminal failure regresses, complete retries quickly so the
		// fixture detects them without leaving Codex reconnecting indefinitely.
		return routerFaultCodexSuccessResponse(), nil
	}
	if request == 1 {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{"0"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"server_error","message":"temporary upstream failure"}}`)),
		}, nil
	}
	return routerFaultCodexSuccessResponse(), nil
}

func routerFaultCodexToolNames(body []byte) []string {
	var request map[string]jsontext.Value
	if json.Unmarshal(body, &request) != nil {
		return nil
	}
	var names []string
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if name, ok := value["name"].(string); ok {
				if kind, ok := value["type"].(string); ok {
					names = append(names, kind+":"+name)
				} else {
					names = append(names, name)
				}
			}
			for _, nested := range value {
				visit(nested)
			}
		case []any:
			for _, nested := range value {
				visit(nested)
			}
		}
	}
	for _, field := range []string{"tools", "additional_tools"} {
		var catalog any
		if json.Unmarshal(request[field], &catalog) == nil {
			visit(catalog)
		}
	}
	return names
}

func routerFaultCodexTransformResponse() *http.Response {
	return routerFaultCodexStreamResponse(routerFaultSSE(
		map[string]any{"type": "response.created", "response": map[string]any{"id": "native-fault-response", "status": "in_progress", "output": []any{}}},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
			"type": "function_call", "id": "native-fault-item", "call_id": "native-fault-call",
			"name": "journal", "namespace": "functions", "arguments": "{}", "status": "in_progress",
		}},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{
			// The intercepted local call is malformed: it lost its call_id before
			// completion. The router, not Codex's tool dispatcher, owns this fault.
			"type": "function_call", "id": "native-fault-item",
			"name": "journal", "namespace": "functions", "arguments": "{}", "status": "completed",
		}},
	))
}

func routerFaultCodexSuccessResponse() *http.Response {
	const answer = "Recovered after a retry."
	item := map[string]any{
		"type": "message", "id": "retry-answer", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": answer, "annotations": []any{}}},
	}
	return routerFaultCodexStreamResponse(routerFaultSSE(
		map[string]any{"type": "response.created", "response": map[string]any{"id": "retry-response", "status": "in_progress", "output": []any{}}},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
			"type": "message", "id": "retry-answer", "role": "assistant", "status": "in_progress", "content": []any{},
		}},
		map[string]any{"type": "response.content_part.added", "item_id": "retry-answer", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}},
		map[string]any{"type": "response.output_text.delta", "item_id": "retry-answer", "output_index": 0, "content_index": 0, "delta": answer},
		map[string]any{"type": "response.output_text.done", "item_id": "retry-answer", "output_index": 0, "content_index": 0, "text": answer},
		map[string]any{"type": "response.content_part.done", "item_id": "retry-answer", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": answer, "annotations": []any{}}},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
		map[string]any{"type": "response.completed", "response": map[string]any{"id": "retry-response", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}},
	))
}

func routerFaultCodexStreamResponse(wire string) *http.Response {
	response := serverHTTPResponse(wire)
	response.Header.Set("Content-Type", "text/event-stream")
	return response
}

func routerFaultCodexEnvironment(t *testing.T) []string {
	t.Helper()
	configRoot := t.TempDir()
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "CODEX_HOME" || key == "XDG_CONFIG_HOME" || key == "BASH_ENV" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "CODEX_HOME="+configRoot, "XDG_CONFIG_HOME="+configRoot)
}

func runRouterFaultNativeCodex(t *testing.T, serverURL, prompt string) (stdout, stderr string, runErr error) {
	t.Helper()
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("native Codex is required for the router-fault acceptance gate")
	}
	config := `model_providers.router_fault_fixture={name="router_fault_fixture",base_url=` + strconv.Quote(serverURL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="router_fault_fixture"`,
		"-c", "tools.update_plan.enabled=false",
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"-C", t.TempDir(), prompt)
	cmd.Env = routerFaultCodexEnvironment(t)
	var output, errors bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &errors
	runErr = cmd.Run()
	return output.String(), errors.String(), runErr
}

func routerFaultNativeEventCount(t *testing.T, output, wantedType string) (count int, rawEvents []string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]jsontext.Value
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid native Codex JSON event: %v\nline: %s", err, line)
		}
		var eventType string
		_ = json.Unmarshal(event["type"], &eventType)
		if eventType == wantedType {
			count++
			rawEvents = append(rawEvents, line)
		}
	}
	return count, rawEvents
}

func routerFaultNativeReference(t *testing.T, event string) string {
	t.Helper()
	_, tail, found := strings.Cut(event, "Diagnostic reference: ")
	if !found {
		t.Fatalf("native failure event omitted diagnostic reference: %s", event)
	}
	reference, _, found := strings.Cut(tail, ".")
	if !found || len(reference) != 12 {
		t.Fatalf("native failure event has malformed diagnostic reference %q", reference)
	}
	return reference
}

func TestRouterTransformFaultNativeCodexE2E(t *testing.T) {
	provider := &routerFaultCodexProvider{mode: "transform"}
	proxy := newManagedMekugiProxy(t)
	replayDirectory := t.TempDir()
	store, err := openMekugiReplayStore(replayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	issues := NewCriticalErrors()
	issues.failureStore = store
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, issues, proxy, nil))
	defer server.Close()

	stdout, stderr, runErr := runRouterFaultNativeCodex(t, server.URL, "Run the deterministic router fault fixture.")
	provider.mu.Lock()
	requests := provider.requests
	toolNames := append([]string(nil), provider.toolNames...)
	provider.mu.Unlock()
	if requests != 1 {
		t.Fatalf("native Codex sent %d upstream requests after response.created, want one terminal failure (initial tool names: %v)\nstdout: %.8000s\nstderr: %.8000s", requests, toolNames, stdout, stderr)
	}
	if runErr == nil {
		t.Fatalf("native Codex unexpectedly accepted the router transform fault (initial tool names: %v)\nstdout: %.8000s\nstderr: %.8000s", toolNames, stdout, stderr)
	}
	failedCount, failedEvents := routerFaultNativeEventCount(t, stdout, "turn.failed")
	if failedCount != 1 {
		t.Fatalf("native turn failures = %d, want exactly one: %s", failedCount, stdout)
	}
	reference := routerFaultNativeReference(t, failedEvents[0])
	allOutput := stdout + stderr
	for _, want := range []string{
		"Retrying will fail the same way", "Switch model", "passthrough mode",
		"--debug", "report the diagnostic reference", "mekugi_sse",
	} {
		if !strings.Contains(allOutput, want) {
			t.Fatalf("native terminal failure omitted %q\nstdout: %.8000s\nstderr: %.8000s", want, stdout, stderr)
		}
	}
	if count := strings.Count(strings.Join(failedEvents, "\n"), "Diagnostic reference:"); count != 1 {
		t.Fatalf("native terminal event carried %d notices, want one: %s", count, failedEvents)
	}
	issues.mu.Lock()
	defer issues.mu.Unlock()
	if len(issues.entries) != 1 || issues.entries[0].count != 1 {
		t.Fatalf("native fault notice count = %+v, want one", issues.entries)
	}
	server.Close()
	if _, err := openMekugiReplayStore(replayDirectory); err != nil {
		t.Fatalf("reopen failure store after native Codex exits: %v", err)
	}
	var inspected, diagnostic bytes.Buffer
	status := RunSessionInspection(t.Context(), []string{
		"--failures", "--replay-dir", replayDirectory, reference,
	}, &inspected, &diagnostic)
	if status != 0 {
		t.Fatalf("post-run failure lookup status=%d stderr=%q", status, diagnostic.String())
	}
	var records []failureRecord
	if err := json.Unmarshal(inspected.Bytes(), &records); err != nil {
		t.Fatalf("failure lookup output is invalid: %s: %v", inspected.String(), err)
	}
	if len(records) != 1 || records[0].Reference != reference || records[0].Phase != requestFailureTransform || records[0].Code != "mekugi_sse" {
		t.Fatalf("post-run lookup did not resolve the sanitized transform failure: %+v", records)
	}
}

func TestRetryablePrestream5xxStillRetriesInNativeCodexE2E(t *testing.T) {
	provider := &routerFaultCodexProvider{mode: "retry"}
	proxy := newManagedMekugiProxy(t)
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, NewCriticalErrors(), proxy, nil))
	defer server.Close()

	stdout, stderr, runErr := runRouterFaultNativeCodex(t, server.URL, "Complete after one retryable provider error.")
	if runErr != nil {
		t.Fatalf("native Codex did not recover from a retryable prestream 5xx: %v\nstdout: %.8000s\nstderr: %.8000s", runErr, stdout, stderr)
	}
	provider.mu.Lock()
	requests := provider.requests
	provider.mu.Unlock()
	if requests != 2 {
		t.Fatalf("native upstream requests = %d, want one 503 followed by one successful retry\nstdout: %.8000s\nstderr: %.8000s", requests, stdout, stderr)
	}
	completedCount, _ := routerFaultNativeEventCount(t, stdout, "turn.completed")
	if completedCount != 1 || !strings.Contains(stdout, "Recovered after a retry.") {
		t.Fatalf("native Codex did not complete the retried turn: %s", stdout)
	}
}
