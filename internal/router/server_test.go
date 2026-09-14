package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/internal/responses"

	"github.com/yusing/mekugi/internal/patchtest"
)

type serverForwardResult struct {
	response *http.Response
	err      error
}

type serverFakeProvider struct {
	results           []serverForwardResult
	forwarded         [][]byte
	forwardedHeaders  []http.Header
	forwardedCacheKey []string
}

func (f *serverFakeProvider) forwardExecution(_, _ context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	f.forwardedHeaders = append(f.forwardedHeaders, headers.Clone())
	f.forwardedCacheKey = append(f.forwardedCacheKey, cacheKey)
	return f.next(body)
}

func (f *serverFakeProvider) next(body []byte) (*http.Response, error) {
	f.forwarded = append(f.forwarded, bytes.Clone(body))
	if len(f.results) == 0 {
		return nil, errors.New("unexpected upstream forward")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.response, result.err
}

func serverHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func serverRequest(t *testing.T, mutate func(map[string]any)) parsedResponsesRequest {
	t.Helper()
	request := map[string]any{
		"model":     "gpt-test",
		"reasoning": map[string]any{"effort": "high"},
		"input": []any{
			testFlatCodeModeAdditionalTools(testCodeModeDescription),
			map[string]any{"role": "user", "content": "task"},
		},
		"tools":       []any{map[string]any{"type": "function", "name": "lookup"}},
		"tool_choice": "auto",
	}
	if mutate != nil {
		mutate(request)
	}
	parsed, err := parseResponsesRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func serverMetadataHeaders(t *testing.T, requestKind string, workspaces map[string]json.RawMessage) http.Header {
	t.Helper()
	encoded, err := json.Marshal(codexTurnMetadata{RequestKind: responses.RequestKind(requestKind), Directories: workspaces})
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set(codexTurnMetadataHeader, string(encoded))
	headers.Set(threadIDHeader, "thread-1")
	return headers
}

func serverCompactionMetadataHeaders(t *testing.T) http.Header {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"request_kind": "compaction",
		"turn_id":      "turn-1",
		"compaction": map[string]any{
			"trigger": "auto", "reason": "context_limit", "implementation": "responses",
			"phase": "standalone_turn", "strategy": "memento",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return http.Header{codexTurnMetadataHeader: []string{string(encoded)}}
}

func TestExecuteRequestFailsClosedBeforeUpstreamWhenRewriteIsIneligible(t *testing.T) {
	workspace := t.TempDir()
	validHeaders := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	missingThreadHeaders := validHeaders.Clone()
	missingThreadHeaders.Del(threadIDHeader)
	tests := []struct {
		name      string
		sessionID string
		headers   http.Header
		mutate    func(map[string]any)
		want      string
	}{
		{name: "missing session", headers: validHeaders, want: "valid session ID"},
		{name: "missing thread", sessionID: "session", headers: missingThreadHeaders, want: "valid Codex thread ID"},
		{name: "invalid metadata", sessionID: "session", headers: http.Header{}, want: "valid turn metadata"},
		{name: "unknown request kind", sessionID: "session", headers: serverMetadataHeaders(t, "other", nil), want: "valid turn metadata"},
		{name: "legacy compact request kind", sessionID: "session", headers: serverMetadataHeaders(t, "compact", nil), want: "valid turn metadata"},
		{name: "compaction metadata on ordinary turn", sessionID: "session", headers: serverCompactionMetadataHeaders(t), mutate: func(request map[string]any) {
			delete(request, "tools")
			request["stream"] = true
			request["parallel_tool_calls"] = false
		}, want: "compaction request cannot expose tools"},
		{name: "missing apply_patch", sessionID: "session", headers: validHeaders, mutate: func(request map[string]any) {
			input := request["input"].([]any)
			additional := input[0].(map[string]any)
			tools := additional["tools"].([]any)
			tools[0].(map[string]any)["description"] = "### exec_command\nRun a command without an editing tool."
		}, want: "unsupported_tool_catalog"},
		{name: "restricted Code Mode tool", sessionID: "session", headers: validHeaders, mutate: func(request map[string]any) {
			request["tool_choice"] = map[string]any{"type": "custom", "name": "exec"}
		}, want: "restricted_tool_choice"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &serverFakeProvider{}
			err := executeRequest(t.Context(), t.Context(), serverRequest(t, test.mutate), test.headers, test.sessionID, provider, io.Discard, nil, newManagedMekugiProxy(t, testTranslator(t, new(int))), nil, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if len(provider.forwarded) != 0 {
				t.Fatalf("ineligible request reached upstream: %s", provider.forwarded[0])
			}
		})
	}
}

func TestExecuteRequestDoesNotRequireWorkspaceMetadata(t *testing.T) {
	tests := []struct {
		name        string
		directories map[string]json.RawMessage
	}{
		{name: "omitted"},
		{name: "empty", directories: map[string]json.RawMessage{}},
		{name: "unusable", directories: map[string]json.RawMessage{"/missing": nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responseBody := string(mustTestJSON(t, map[string]any{"status": "completed"}))
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
			proxy := newManagedMekugiProxy(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
				t.Fatal("response without an mekugi call reached the translator")
				return nil, nil
			}))
			var output bytes.Buffer
			err := executeRequest(
				t.Context(),
				t.Context(),
				serverRequest(t, nil),
				serverMetadataHeaders(t, "turn", test.directories),
				"session",
				provider,
				&output,
				nil,
				proxy,
				nil, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 {
				t.Fatalf("upstream requests = %d, want 1", len(provider.forwarded))
			}
		})
	}
}

func TestExecuteRequestSupportsNativeToolsOnTheSameResponsesPath(t *testing.T) {
	workspace := t.TempDir()
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{map[string]any{
			"type": "custom_tool_call", "id": "item-H", "call_id": "call-H",
			"name": mekugiToolName, "input": testMekugiScript, "status": "completed",
		}},
	})))}}}
	request := serverRequest(t, func(request map[string]any) {
		request["input"] = []any{map[string]any{"role": "user", "content": "task"}}
		request["tools"] = testNativeResponsesTools()
	})
	var output bytes.Buffer
	err := executeRequest(
		t.Context(),
		t.Context(),
		request,
		serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}),
		"native-session",
		provider,
		&output,
		nil,
		newManagedMekugiProxy(t, testTranslator(t, new(int))),
		nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 || bytes.Contains(provider.forwarded[0], []byte(`"name":"apply_patch"`)) ||
		!bytes.Contains(provider.forwarded[0], []byte(`"name":"hpatch"`)) {
		t.Fatalf("native forwarded request = %s", provider.forwarded)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || jsonString(response.Output[0], "type") != "function_call" ||
		jsonString(response.Output[0], "name") != nativeExecCommandToolName {
		t.Fatalf("native client response = %s", output.Bytes())
	}
}

func TestExecuteRequestForwardsCompactionWithoutRouterRewrite(t *testing.T) {
	repeated := strings.Repeat("exact compaction text with a reserved !V prefix and repeated content; ", 20)
	parsed, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{}},
			map[string]any{"type": "message", "role": "developer", "content": "instructions"},
			map[string]any{"type": "message", "role": "user", "content": repeated},
		},
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"stream":              true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	originalBody, err := json.Marshal(parsed.fields)
	if err != nil {
		t.Fatal(err)
	}
	completed := mustTestJSON(t, map[string]any{
		"type": "response.completed",
		"response": map[string]any{"status": "completed", "output": []any{
			map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": "!ctp2 R\n@{0}"},
			}},
		}},
	})
	responseBody := "event: response.completed\ndata: " + string(completed) + "\n\n"
	response := serverHTTPResponse(responseBody)
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	proxy := newManagedMekugiProxy(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
		t.Fatal("compaction reached the mekugi translator")
		return nil, nil
	}))
	codec := mustCTP2Codec(t)
	var output bytes.Buffer
	err = executeRequest(t.Context(), t.Context(), parsed, serverCompactionMetadataHeaders(t), "session", provider, &output, nil, proxy, codec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(provider.forwarded))
	}
	if !bytes.Equal(provider.forwarded[0], originalBody) {
		t.Fatalf("compaction request was rewritten:\n got %s\nwant %s", provider.forwarded[0], originalBody)
	}
	if output.String() != responseBody {
		t.Fatalf("visible response = %s, want %s", output.String(), responseBody)
	}
}

func TestExecuteRequestPassesThroughOriginalRequestAndRecordsUsage(t *testing.T) {
	parsed := serverRequest(t, func(request map[string]any) {
		request["prompt_cache_key"] = "control-cache"
	})
	originalBody, err := json.Marshal(parsed.fields)
	if err != nil {
		t.Fatal(err)
	}
	responseBody := string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"usage": map[string]any{
			"input_tokens": 12, "input_tokens_details": map[string]any{"cached_tokens": 5},
			"output_tokens": 7, "output_tokens_details": map[string]any{"reasoning_tokens": 3},
		},
	}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	var output bytes.Buffer
	issues := NewCriticalErrors()
	if err := executeRequest(t.Context(), t.Context(), parsed, http.Header{}, "session", provider, &output, issues, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], originalBody) {
		t.Fatalf("forwarded request = %q, want original %q", provider.forwarded, originalBody)
	}
	if got := provider.forwardedCacheKey[0]; got != "control-cache" {
		t.Fatalf("upstream cache key = %q, want control-cache", got)
	}
	if output.String() != responseBody {
		t.Fatalf("visible response = %q, want %q", output.String(), responseBody)
	}
	if len(issues.Pending()) != 0 {
		t.Fatal("successful request emitted critical notice")
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestExecuteRequestForwardsRewrittenRequestAndRecordsUsage(t *testing.T) {
	workspace := t.TempDir()
	parsed := serverRequest(t, func(request map[string]any) {
		request["prompt_cache_key"] = "prompt-cache"
	})

	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	headers.Add(sessionIDHeader, "session-primary")
	headers.Add(sessionIDHeader, "session-secondary")
	headers.Set(threadIDHeader, "thread")
	headers.Set(clientRequestIDHeader, "client-request")
	headers.Set(codexWindowIDHeader, "window:0")
	headers.Set(codexBetaFeaturesHeader, "feature")
	headers.Set(codexResponsesLiteHeader, "true")
	headers.Set(openAISubagentHeader, threadSpawnSubagent)
	responseBody := string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"usage": map[string]any{
			"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 4},
			"output_tokens": 6, "output_tokens_details": map[string]any{"reasoning_tokens": 2},
		},
		"future": map[string]any{"kept": true},
	}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	proxy := newManagedMekugiProxy(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
		t.Fatal("response without an mekugi call reached the translator")
		return nil, nil
	}))
	var output bytes.Buffer
	err := executeRequest(t.Context(), t.Context(), parsed, headers, "session", provider, &output, nil, proxy, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(provider.forwarded))
	}
	if got := provider.forwardedCacheKey[0]; got != "prompt-cache" {
		t.Fatalf("upstream cache key = %q, want prompt_cache_key", got)
	}
	for _, name := range []string{sessionIDHeader, threadIDHeader, clientRequestIDHeader, codexWindowIDHeader, codexBetaFeaturesHeader, codexResponsesLiteHeader, openAISubagentHeader, codexTurnMetadataHeader} {
		if got, want := provider.forwardedHeaders[0].Values(name), headers.Values(name); !slices.Equal(got, want) {
			t.Fatalf("forwarded header %s = %q, want %q", name, got, want)
		}
	}
	forwarded, err := parseResponsesRequest(provider.forwarded[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(forwarded.fields["tools"]), "\"name\":\"hpatch\"") || strings.Contains(string(forwarded.fields["tools"]), workspace) {
		t.Fatalf("unstable rewritten tools = %s", forwarded.fields["tools"])
	}
	if strings.Contains(string(forwarded.fields["input"]), workspace) {
		t.Fatalf("single-workspace input contains redundant metadata: %s", forwarded.fields["input"])
	}
	input := string(forwarded.fields["input"])
	if strings.Contains(input, codeModeApplyPatchHeading) ||
		strings.Contains(input, codeModeExecCommandHeading) ||
		!strings.Contains(input, `"name":"exec"`) {
		t.Fatalf("flat app-server exec was not rewritten: %s", input)
	}
	var forwardedTools []map[string]json.RawMessage
	if err := json.Unmarshal(forwarded.fields["tools"], &forwardedTools); err != nil {
		t.Fatal(err)
	}
	shellIndex := slices.IndexFunc(forwardedTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "shell"
	})
	if shellIndex < 0 {
		t.Fatalf("rewritten tools lost shell: %#v", forwardedTools)
	}
	shellDescription := jsonString(forwardedTools[shellIndex], "description")
	for _, required := range []string{"Run free-form scripts", "### `#!params`", "{ workdir?: string }"} {
		if !strings.Contains(shellDescription, required) {
			t.Fatalf("shell description is missing %q: %q", required, shellDescription)
		}
	}
	if strings.Contains(shellDescription, "exec_command") || strings.Contains(shellDescription, "cmd: string") {
		t.Fatalf("shell description exposes nested command syntax: %q", shellDescription)
	}
	for _, persistent := range []string{"#!cmd=", "@shell/", "#!batch=", "#!batch-stop=", "Multiline commands share", "shell & and wait"} {
		if strings.Contains(shellDescription, persistent) {
			t.Fatalf("shell description duplicates persistent workflow %q: %q", persistent, shellDescription)
		}
	}
	if string(forwarded.fields["reasoning"]) != "{\"effort\":\"high\"}" {
		t.Fatalf("reasoning request changed: %s", forwarded.fields["reasoning"])
	}
	if strings.Contains(output.String(), "selected_reasoning_effort") || !strings.Contains(output.String(), "\"future\":{\"kept\":true}") {
		t.Fatalf("visible response changed unexpectedly: %s", output.String())
	}
}

func TestShellHCatAfterAppliedMekugiCarrierRemainsModelVisible(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	initial := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	mekugiScript := "in file.txt\ntype 2:f44e \"B\"\n"
	shellInput := "hcat file.txt 1:3"
	provider := &serverFakeProvider{
		results: []serverForwardResult{
			{response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
				"status": "completed",
				"output": []any{map[string]any{
					"type": "custom_tool_call", "id": "item-H", "call_id": "call-H",
					"name": mekugiToolName, "input": mekugiScript, "status": "completed",
				}},
			})))},
			{response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
				"status": "completed",
				"output": []any{map[string]any{
					"type": "custom_tool_call", "id": "item-R", "call_id": "call-R",
					"name": "shell", "input": shellInput, "status": "completed",
				}},
			})))},
			{response: serverHTTPResponse(`{"status":"completed","output":[]}`)},
		},
	}
	translator := newInProcessMekugiTranslator(t.TempDir())
	proxy := newManagedMekugiProxy(t, translator)
	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	const sessionID = "session-mekugi-shell-hcat"

	requestWith := func(items ...any) parsedResponsesRequest {
		return serverRequest(t, func(request map[string]any) {
			additional := request["input"].([]any)[0]
			request["input"] = append([]any{additional}, items...)
		})
	}
	runRequest := func(request parsedResponsesRequest) []byte {
		var output bytes.Buffer
		if err := executeRequest(
			t.Context(),
			t.Context(),
			request,
			headers,
			sessionID,
			provider,
			&output,
			nil,
			proxy,
			nil, nil,
		); err != nil {
			t.Fatal(err)
		}
		return output.Bytes()
	}
	outputItems := func(response []byte) []map[string]json.RawMessage {
		var envelope struct {
			Output []map[string]json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(response, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Output
	}

	firstVisible := runRequest(requestWith(map[string]any{"role": "user", "content": "edit file.txt"}))
	firstItems := outputItems(firstVisible)
	if len(firstItems) != 1 || jsonString(firstItems[0], "name") != "exec" {
		t.Fatalf("translated mekugi response = %s", firstVisible)
	}
	mekugiCarrier := firstItems[0]
	carrierInput := jsonString(mekugiCarrier, "input")
	program, ok := strings.CutPrefix(
		carrierInput,
		mekugiApplyExecMarker+"await tools.apply_patch(",
	)
	if !ok {
		t.Fatalf("mekugi carrier input = %q", carrierInput)
	}
	encodedPatch, encodedReport, ok := strings.Cut(program, ");\ntext(")
	if !ok {
		t.Fatalf("mekugi carrier framing = %q", carrierInput)
	}
	encodedReport = strings.TrimSuffix(encodedReport, ");")
	patch, err := strconv.Unquote(encodedPatch)
	if err != nil {
		t.Fatalf("decode carrier patch: %v", err)
	}
	report, err := strconv.Unquote(encodedReport)
	if err != nil {
		t.Fatalf("decode carrier report: %v", err)
	}
	applied, err := patchtest.Apply(map[string]string{"file.txt": initial}, patch)
	if err != nil {
		t.Fatalf("execute carrier patch: %v", err)
	}
	if err := os.WriteFile(path, []byte(applied["file.txt"]), 0o600); err != nil {
		t.Fatal(err)
	}

	mekugiOutput := map[string]any{
		"type": "custom_tool_call_output", "call_id": "call-H", "output": report,
	}
	secondVisible := runRequest(requestWith(mekugiCarrier, mekugiOutput))
	secondItems := outputItems(secondVisible)
	if len(secondItems) != 1 ||
		jsonString(secondItems[0], "name") != "exec" ||
		!strings.Contains(jsonString(secondItems[0], "input"), shellInput) {
		t.Fatalf("translated shell response = %s", secondVisible)
	}
	shellCarrier := secondItems[0]

	if _, exists := proxy.registry.contribution("shell"); !exists {
		t.Fatal("shell worker is unavailable")
	}
	invocation := newShellWorkerTestInvocation(workspace)
	shellStdout, shellStderr, exitCode := runShellWorkerTest(
		t,
		proxy.registry,
		"bash",
		nil,
		"hcat file.txt 1:3",
		os.Stdin,
		invocation,
	)
	wantRows := "1:8ed3 alpha\n2:df7e B\n3:be9d gamma\n"
	if exitCode != 0 || shellStdout != wantRows || shellStderr != "" {
		t.Fatalf(
			"hcat worker exit %d, stdout %q, stderr %q",
			exitCode,
			shellStdout,
			shellStderr,
		)
	}

	shellOutput := map[string]any{
		"type": "custom_tool_call_output", "call_id": "call-R", "output": shellStdout,
	}
	runRequest(requestWith(mekugiCarrier, mekugiOutput, shellCarrier, shellOutput))
	if len(provider.forwarded) != 3 {
		t.Fatalf("upstream requests = %d, want 3", len(provider.forwarded))
	}
	thirdForwarded, err := parseResponsesRequest(provider.forwarded[2])
	if err != nil {
		t.Fatal(err)
	}
	var forwardedItems []map[string]json.RawMessage
	if err := json.Unmarshal(thirdForwarded.fields["input"], &forwardedItems); err != nil {
		t.Fatal(err)
	}
	sawShell, sawRows := false, false
	for _, item := range forwardedItems {
		if jsonString(item, "name") == "shell" && jsonString(item, "input") == shellInput {
			sawShell = true
		}
		if jsonString(item, "type") == "custom_tool_call_output" &&
			jsonString(item, "call_id") == "call-R" &&
			jsonString(item, "output") == wantRows {
			sawRows = true
		}
	}
	if !sawShell || !sawRows {
		t.Fatalf("model-visible shell history = %s", thirdForwarded.fields["input"])
	}
}

func TestExecuteRequestRejectsDirectAdditionalApplyPatchWithoutExecCarrier(t *testing.T) {
	workspace := t.TempDir()
	parsed := serverRequest(t, func(request map[string]any) {
		additional := request["input"].([]any)[0].(map[string]any)
		additional["tools"] = []any{
			map[string]any{"type": "custom", "name": "unrelated"},
			map[string]any{"type": "custom", "name": applyPatchToolName, "description": "Apply a patch."},
		}
	})
	responseBody := string(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{}}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	var output bytes.Buffer
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	err := executeRequest(
		t.Context(),
		t.Context(),
		parsed,
		serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}),
		"session-direct",
		provider,
		&output,
		nil,
		proxy,
		nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported flat apply_patch") {
		t.Fatalf("direct request error = %v", err)
	}
	if len(provider.forwarded) != 0 || output.Len() != 0 {
		t.Fatalf("direct request was forwarded %d time(s), output %q", len(provider.forwarded), output.String())
	}
}

type serverErrorWriter struct{ err error }

func (w serverErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExecuteRequestRecordsUsageAndFailureWhenDeliveryFails(t *testing.T) {
	workspace := t.TempDir()
	responseBody := string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"usage":  map[string]any{"input_tokens": 10},
	}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	issues := NewCriticalErrors()
	err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, serverErrorWriter{err: io.ErrClosedPipe}, issues, newManagedMekugiProxy(t, testTranslator(t, new(int))), nil, nil)
	if err == nil {
		t.Fatal("delivery failure returned no error")
	}
}

type serverRepeatingReader struct{}

func (serverRepeatingReader) Read(content []byte) (int, error) {
	for index := range content {
		content[index] = 'x'
	}
	return len(content), nil
}

func TestResponsesHandlerRejectsBackgroundBeforeUpstream(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"gpt-test","input":"task","background":true}`),
	)
	recorder := httptest.NewRecorder()
	provider := &serverFakeProvider{}
	responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil, nil)(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "background Responses requests are not supported") {
		t.Fatalf("response = %q", recorder.Body.String())
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("background request reached upstream")
	}
}

func TestResponsesHandlerRejectsBodyBeyondRouterBufferBudget(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", io.LimitReader(serverRepeatingReader{}, responsesRequestBufferBytes+1))
	recorder := httptest.NewRecorder()
	provider := &serverFakeProvider{}

	responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil, nil)(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("request beyond router buffer budget reached upstream")
	}
}

type serverProviderFunc func(context.Context, context.Context, []byte, http.Header, string) (*http.Response, error)

func (f serverProviderFunc) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	return f(startCtx, responseCtx, body, headers, cacheKey)
}

type serverCancelableSSEBody struct {
	ctx      context.Context
	content  *strings.Reader
	canceled chan struct{}
	once     sync.Once
}

func (body *serverCancelableSSEBody) Read(content []byte) (int, error) {
	if body.content.Len() > 0 {
		return body.content.Read(content)
	}
	<-body.ctx.Done()
	body.once.Do(func() { close(body.canceled) })
	return 0, body.ctx.Err()
}

func (*serverCancelableSSEBody) Close() error { return nil }

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestResponsesHandlerDoesNotLogClientCancellationAsOperationalEvent(t *testing.T) {
	upstreamEvent := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	upstreamCanceled := make(chan struct{})
	provider := serverProviderFunc(func(_, responseCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &serverCancelableSSEBody{
				ctx: responseCtx, content: strings.NewReader(upstreamEvent), canceled: upstreamCanceled,
			},
		}, nil
	})
	issues := NewCriticalErrors()
	handler := responsesHandler(t.Context(), time.Minute, provider, issues, newManagedMekugiProxy(t, nil), nil, nil)
	handled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler(writer, request)
		close(handled)
	}))
	defer server.Close()

	body := mustTestJSON(t, map[string]any{
		"model": "gpt-test",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{}},
			map[string]any{"type": "message", "role": "developer", "content": "instructions"},
			map[string]any{"type": "message", "role": "user", "content": "summarize the conversation"},
		},
		"tool_choice": "auto", "parallel_tool_calls": false, "stream": true,
	})
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = serverCompactionMetadataHeaders(t)
	request.Header.Set(sessionIDHeader, "session")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	visible := make([]byte, len(upstreamEvent))
	if _, err := io.ReadFull(response.Body, visible); err != nil {
		t.Fatalf("read initial upstream event: %v", err)
	}
	if string(visible) != upstreamEvent {
		t.Fatalf("visible event = %q, want %q", visible, upstreamEvent)
	}
	cancelRequest()
	response.Body.Close()

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not stop after the client canceled its request")
	}
	select {
	case <-upstreamCanceled:
	default:
		t.Fatal("client cancellation did not reach the upstream response body")
	}
}

func TestExecuteRequestSuccessfulStreamLifecycle(t *testing.T) {
	completed := mustTestJSON(t, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"usage": map[string]any{
				"input_tokens": 11, "input_tokens_details": map[string]any{"cached_tokens": 4},
				"output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		},
	})
	response := serverHTTPResponse("event: response.completed\ndata: " + string(completed) + "\n\n")
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	recorder := httptest.NewRecorder()
	writer := &trackedResponseWriter{ResponseWriter: recorder}
	issues := NewCriticalErrors()
	err := executeRequest(
		t.Context(), t.Context(),
		serverRequest(t, func(request map[string]any) { request["stream"] = true }),
		http.Header{}, "stream-session", provider, writer, issues, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

}

func TestExecuteRequestTerminalOutcomes(t *testing.T) {
	failed := mustTestJSON(t, map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"status": "failed",
			"usage":  map[string]any{"input_tokens": 9},
		},
	})
	failedResponse := serverHTTPResponse("event: response.failed\ndata: " + string(failed) + "\n\n")
	failedResponse.Header.Set("Content-Type", "text/event-stream")
	tests := []struct {
		name             string
		response         *http.Response
		mutate           func(map[string]any)
		wantOutcome      requestOutcome
		wantUsage        uint64
		wantFailurePhase requestFailurePhase
	}{
		{
			name:             "upstream terminal failure",
			response:         failedResponse,
			mutate:           func(request map[string]any) { request["stream"] = true },
			wantOutcome:      requestOutcomeFailed,
			wantUsage:        9,
			wantFailurePhase: requestFailureTerminalValidation,
		},
		{
			name: "non-2xx upstream response",
			response: &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`)),
			},
			wantOutcome:      requestOutcomeFailed,
			wantFailurePhase: requestFailureTerminalValidation,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &serverFakeProvider{results: []serverForwardResult{{response: test.response}}}
			issues := NewCriticalErrors()
			err := executeRequest(
				t.Context(), t.Context(), serverRequest(t, test.mutate), http.Header{}, "session",
				provider, io.Discard, issues, nil, nil, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(issues.Pending()) != 1 {
				t.Fatal("terminal failure must queue one notice")
			}
		})
	}
}

func TestExecuteRequestCancellationBeforeResponseLifecycle(t *testing.T) {
	started := make(chan struct{})
	provider := serverProviderFunc(func(startCtx, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		close(started)
		<-startCtx.Done()
		return nil, startCtx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	issues := NewCriticalErrors()
	result := make(chan error, 1)
	go func() {
		result <- executeRequest(
			ctx, ctx, serverRequest(t, nil), http.Header{}, "session", provider, io.Discard,
			issues, nil, nil, nil,
		)
	}()
	<-started
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}

}

type serverBlockingSSEBody struct {
	ctx     context.Context
	content *strings.Reader
	blocked chan struct{}
	once    sync.Once
}

func (body *serverBlockingSSEBody) Read(content []byte) (int, error) {
	if body.content.Len() > 0 {
		return body.content.Read(content)
	}
	body.once.Do(func() { close(body.blocked) })
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (*serverBlockingSSEBody) Close() error { return nil }

type serverBlockingWriter struct {
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (writer *serverBlockingWriter) Write(content []byte) (int, error) {
	writer.once.Do(func() { close(writer.blocked) })
	<-writer.release
	return len(content), nil
}

func TestStreamIdleTimeoutPausesDuringDownstreamBackpressure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstreamReader, upstreamWriter := io.Pipe()
		stream := newStreamIdleReadCloser(t.Context(), upstreamReader, time.Minute)
		defer func() { _ = stream.Close() }()
		defer func() { _ = upstreamWriter.Close() }()

		downstream := &serverBlockingWriter{blocked: make(chan struct{}), release: make(chan struct{})}
		type copyResult struct {
			state responseTerminalState
			err   error
		}
		result := make(chan copyResult, 1)
		go func() {
			state, err := copySSETransformed(downstream, stream, nil, nil)
			result <- copyResult{state: state, err: err}
		}()

		if _, err := io.WriteString(upstreamWriter, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"); err != nil {
			t.Fatal(err)
		}
		<-downstream.blocked
		upstreamResult := make(chan error, 1)
		go func() {
			_, err := io.WriteString(upstreamWriter, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			upstreamResult <- err
		}()
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if stream.timedOut.Load() {
			t.Fatal("upstream stream timed out while downstream processing was blocked")
		}

		close(downstream.release)
		if err := <-upstreamResult; err != nil {
			t.Fatal(err)
		}
		got := <-result
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.state != responseTerminalCompleted {
			t.Fatalf("terminal state = %s, want completed", got.state)
		}
	})
}

func TestStreamIdleTimeoutResetsOnPartialSSEBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstreamReader, upstreamWriter := io.Pipe()
		stream := newStreamIdleReadCloser(t.Context(), upstreamReader, time.Minute)
		defer func() { _ = stream.Close() }()
		defer func() { _ = upstreamWriter.Close() }()

		type copyResult struct {
			state responseTerminalState
			err   error
		}
		result := make(chan copyResult, 1)
		go func() {
			state, err := copySSETransformed(io.Discard, stream, nil, nil)
			result <- copyResult{state: state, err: err}
		}()

		for _, fragment := range []string{
			"event: response.",
			"completed\n",
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n",
			"\n",
		} {
			if _, err := io.WriteString(upstreamWriter, fragment); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			time.Sleep(45 * time.Second)
		}

		got := <-result
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.state != responseTerminalCompleted {
			t.Fatalf("terminal state = %s, want completed", got.state)
		}
	})
}

func TestExecuteRequestStreamIdleTimeoutLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstreamReader, upstreamWriter := io.Pipe()
		defer func() { _ = upstreamWriter.Close() }()

		response := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       newStreamIdleReadCloser(t.Context(), upstreamReader, time.Minute),
		}
		provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
		recorder := httptest.NewRecorder()
		writer := &trackedResponseWriter{ResponseWriter: recorder}
		issues := NewCriticalErrors()
		result := make(chan error, 1)
		go func() {
			result <- executeRequest(
				t.Context(), t.Context(),
				serverRequest(t, func(request map[string]any) { request["stream"] = true }),
				http.Header{}, "idle-session", provider, writer, issues, nil, nil, nil,
			)
		}()

		if _, err := io.WriteString(upstreamWriter, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()

		err := <-result
		if !errors.Is(err, errUpstreamStreamIdleTimeout) {
			t.Fatalf("error = %v, want stream idle timeout", err)
		}

	})
}

func TestCopySSETransformedKeepsMalformedStateInvalid(t *testing.T) {
	completed := mustTestJSON(t, map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"status": "completed"},
	})
	body := "data: {not-json}\n\n" +
		"event: response.completed\n" +
		"data: " + string(completed) + "\n\n"
	state, err := copySSETransformed(io.Discard, strings.NewReader(body), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != responseTerminalInvalid {
		t.Fatalf("terminal state = %v, want invalid", state)
	}

	for _, test := range []struct {
		name              string
		current, observed responseTerminalState
	}{
		{name: "invalid then completed", current: responseTerminalInvalid, observed: responseTerminalCompleted},
		{name: "invalid then failed", current: responseTerminalInvalid, observed: responseTerminalFailed},
		{name: "completed then invalid", current: responseTerminalCompleted, observed: responseTerminalInvalid},
		{name: "failed then invalid", current: responseTerminalFailed, observed: responseTerminalInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mergeResponseTerminalState(test.current, test.observed); got != responseTerminalInvalid {
				t.Fatalf("mergeResponseTerminalState() = %v, want invalid", got)
			}
		})
	}
}

func TestExecuteRequestCancellationAfterResponseLifecycle(t *testing.T) {
	blocked := make(chan struct{})
	provider := serverProviderFunc(func(_, executionCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: newStreamIdleReadCloser(executionCtx, &serverBlockingSSEBody{
				ctx: executionCtx, content: strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"), blocked: blocked,
			}, time.Hour),
		}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	recorder := httptest.NewRecorder()
	writer := &trackedResponseWriter{ResponseWriter: recorder}
	issues := NewCriticalErrors()
	result := make(chan error, 1)
	go func() {
		result <- executeRequest(
			ctx, ctx,
			serverRequest(t, func(request map[string]any) { request["stream"] = true }),
			http.Header{}, "session", provider, writer, issues, nil, nil, nil,
		)
	}()
	<-blocked
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}

}

type serverCancelAfterTerminalResponseWriter struct {
	http.ResponseWriter
	cancel context.CancelFunc
	once   sync.Once
}

func (writer *serverCancelAfterTerminalResponseWriter) Write(content []byte) (int, error) {
	written, err := writer.ResponseWriter.Write(content)
	if bytes.Contains(content, []byte(`"type":"response.completed"`)) {
		writer.once.Do(writer.cancel)
	}
	return written, err
}

func (writer *serverCancelAfterTerminalResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func TestExecuteRequestCompletesAtTerminalEventBeforeStreamEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	blocked := make(chan struct{})
	completed := mustTestJSON(t, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"usage":  map[string]any{"input_tokens": 7},
		},
	})
	provider := serverProviderFunc(func(_, executionCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &serverBlockingSSEBody{
				ctx: executionCtx, content: strings.NewReader("event: response.completed\ndata: " + string(completed) + "\n\n"), blocked: blocked,
			},
		}, nil
	})
	recorder := httptest.NewRecorder()
	downstream := &serverCancelAfterTerminalResponseWriter{ResponseWriter: recorder, cancel: cancel}
	writer := &trackedResponseWriter{ResponseWriter: downstream}
	issues := NewCriticalErrors()

	err := executeRequest(
		ctx, ctx,
		serverRequest(t, func(request map[string]any) { request["stream"] = true }),
		http.Header{}, "session", provider, writer, issues, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("terminal response returned error: %v", err)
	}
	select {
	case <-blocked:
		t.Fatal("router read upstream after the terminal response event")
	default:
	}

}

func TestExecuteRequestResponseStartDeadlineLifecycle(t *testing.T) {
	started := make(chan struct{})
	provider := serverProviderFunc(func(startCtx, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		close(started)
		<-startCtx.Done()
		return nil, startCtx.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- executeRequest(
			ctx, t.Context(), serverRequest(t, nil), http.Header{}, "session", provider, io.Discard,
			nil, nil, nil, nil,
		)
	}()
	<-started
	err := <-result
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
}

func TestExecuteRequestIndependentUpstreamCancellationIsFailure(t *testing.T) {
	provider := serverProviderFunc(func(context.Context, context.Context, []byte, http.Header, string) (*http.Response, error) {
		return nil, context.Canceled
	})
	issues := NewCriticalErrors()
	err := executeRequest(
		t.Context(), t.Context(), serverRequest(t, nil), http.Header{}, "session",
		provider, io.Discard, issues, nil, nil, nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want wrapped upstream cancellation", err)
	}
}

func TestExecuteRequestTransformFailureLifecycle(t *testing.T) {
	workspace := t.TempDir()
	item := testMekugiItem()
	item["type"] = "message"
	responseBody := string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{item},
	}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	issues := NewCriticalErrors()
	err := executeRequest(
		t.Context(), t.Context(), serverRequest(t, nil),
		serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}),
		"session", provider, io.Discard, issues,
		newManagedMekugiProxy(t, testTranslator(t, new(int))), nil, nil,
	)
	if err == nil {
		t.Fatal("transform failure returned no error")
	}
}

func TestCommittedSSETransformFailureDefersSafeCauseToCriticalNotice(t *testing.T) {
	workspace := t.TempDir()
	added := testMekugiItem()
	added["status"] = "in_progress"
	added["input"] = ""
	secret := "Authorization Bearer token-plain prompt unquoted-secret-script"
	responseBody := "data: " + string(mustTestJSON(t, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "response", "status": "in_progress", "output": []any{}},
	})) + "\n\n" +
		"data: " + string(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})) + "\n\n" +
		"data: " + string(mustTestJSON(t, map[string]any{
		"type": "response.function_call_arguments.done", "item_id": "item-H", "arguments": secret,
	})) + "\n\n"
	response := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	issues := NewCriticalErrors()
	request := serverRequest(t, func(fields map[string]any) { fields["stream"] = true })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(request.originalBody)))
	req.Header = serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	req.Header.Set(sessionIDHeader, "session")
	output := httptest.NewRecorder()
	responsesHandler(t.Context(), time.Minute, provider, issues,
		newManagedMekugiProxy(t, testTranslator(t, new(int))), nil, nil)(output, req)

	if output.Code != http.StatusOK || !strings.Contains(output.Body.String(), "response.created") {
		t.Fatalf("stream was not committed before transform failure: %d %s", output.Code, output.Body.String())
	}
	if strings.Contains(output.Body.String(), "unsupported HPATCH-related") || strings.Contains(output.Body.String(), secret) {
		t.Fatalf("failure detail was written into the active response: %s", output.Body.String())
	}
	pending := strings.Join(issues.Pending(), "\n")
	if !strings.Contains(pending, `response.function_call_arguments.done`) ||
		!strings.Contains(pending, "Diagnostic reference:") || strings.Contains(pending, secret) {
		t.Fatalf("safe underlying cause was not retained: %s", pending)
	}
	visible := issues.transform("session", false)
	if len(visible.messages) != 1 {
		t.Fatalf("critical notice was not available to a later response: %d", len(visible.messages))
	}
	encoded := string(mustTestJSON(t, visible.messages[0]))
	visible.finish(false)
	if !strings.Contains(encoded, `response.function_call_arguments.done`) || strings.Contains(encoded, secret) {
		t.Fatalf("user-only notice was unsafe or hid the cause: %s", encoded)
	}
}

const testProviderBaseURL = "https://provider.example"

type serverRoundTripper func(*http.Request) (*http.Response, error)

func (f serverRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestModelsHandlerForwardsCodexAuthenticationQueryAndResponse(t *testing.T) {
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != testProviderBaseURL+"/models?client_version=0.146.0" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		for name, want := range map[string]string{
			"Authorization":      "Bearer caller-token",
			"ChatGPT-Account-ID": "caller-account",
			"Accept":             "application/json",
			"Originator":         codexClientIdentity,
			"User-Agent":         codexClientIdentity,
		} {
			if got := request.Header.Get(name); got != want {
				t.Errorf("header %s = %q, want %q", name, got, want)
			}
		}
		responseHeaders := make(http.Header)
		responseHeaders.Set("Content-Type", "application/json")
		responseHeaders.Set("Cache-Control", "max-age=60")
		responseHeaders.Set("ETag", "catalog-version")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     responseHeaders,
			Body:       io.NopCloser(strings.NewReader(`{"models":[]}`)),
		}, nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.146.0", nil)
	request.Header = codexAuthHeaders()
	recorder := httptest.NewRecorder()

	modelsHandler(newProviderClient(testProviderBaseURL, httpClient), nil)(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"models":[]}` {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	for name, want := range map[string]string{
		"Content-Type":  "application/json",
		"Cache-Control": "max-age=60",
		"ETag":          "catalog-version",
	} {
		if got := recorder.Header().Get(name); got != want {
			t.Errorf("response header %s = %q, want %q", name, got, want)
		}
	}
}

func TestModelsHandlerRejectsUpstreamBodyReadFailure(t *testing.T) {
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Cache-Control": []string{"max-age=60"}},
			Body: io.NopCloser(io.MultiReader(
				strings.NewReader(`{"models":[`),
				iotest.ErrReader(errors.New("upstream read failed")),
			)),
		}, nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	recorder := httptest.NewRecorder()

	modelsHandler(newProviderClient(testProviderBaseURL, httpClient), nil)(recorder, request)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "upstream read failed") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `{"models":[`) {
		t.Fatalf("response exposes partial upstream body: %q", recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Errorf("response Cache-Control = %q, want empty", got)
	}
}

func TestModelsHandlerRejectsBodyBeyondRouterBufferBudget(t *testing.T) {
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(io.LimitReader(serverRepeatingReader{}, modelsResponseBufferBytes+1)),
		}, nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	recorder := httptest.NewRecorder()

	modelsHandler(newProviderClient(testProviderBaseURL, httpClient), nil)(recorder, request)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "router buffer budget") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestModelsHandlerRejectsMissingAuthentication(t *testing.T) {
	var forwarded bool
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		forwarded = true
		return serverHTTPResponse("{}"), nil
	})}
	recorder := httptest.NewRecorder()

	modelsHandler(newProviderClient(testProviderBaseURL, httpClient), nil)(
		recorder,
		httptest.NewRequest(http.MethodGet, "/v1/models", nil),
	)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "Authorization") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if forwarded {
		t.Fatal("unauthenticated models request reached upstream")
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestProviderClientForwardsCodexAuthenticationAndRequestHeaders(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set("Originator", "caller-originator")
	headers.Set("User-Agent", "caller-agent")
	headers.Set("X-Unrelated-Caller-Data", "not-forwarded")

	headers.Add(sessionIDHeader, "session-primary")
	headers.Add(sessionIDHeader, "session-secondary")
	headers.Set(threadIDHeader, "thread")
	headers.Set(clientRequestIDHeader, "client-request")
	headers.Set(codexWindowIDHeader, "window:0")
	headers.Set(codexBetaFeaturesHeader, "feature")
	headers.Set(codexResponsesLiteHeader, "true")
	headers.Set(openAISubagentHeader, threadSpawnSubagent)
	headers.Set(codexTurnMetadataHeader, "metadata")
	headers.Set("x-codex-turn-state", "opaque-turn-state")
	headers.Set(mekugiCaptureIDHeader, "capture")
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		trusted := map[string]string{
			"Authorization":      "Bearer caller-token",
			"ChatGPT-Account-ID": "caller-account",
			"Originator":         codexClientIdentity,
			"User-Agent":         codexClientIdentity,
		}
		for name, value := range trusted {
			if got := request.Header.Get(name); got != value {
				t.Errorf("header %s = %q, want %q", name, got, value)
			}
		}
		for _, name := range []string{threadIDHeader, clientRequestIDHeader, codexWindowIDHeader, codexBetaFeaturesHeader, codexResponsesLiteHeader, openAISubagentHeader, codexTurnMetadataHeader, "x-codex-turn-state", mekugiCaptureIDHeader} {
			if got, want := request.Header.Values(name), headers.Values(name); !slices.Equal(got, want) {
				t.Errorf("header %s = %q, want %q", name, got, want)
			}
		}
		if got := request.Header[codexSessionIDHeader]; !slices.Equal(got, []string{"cache-key"}) {
			t.Errorf("%s = %q, want cache key", codexSessionIDHeader, got)
		}
		if got := request.Header.Get(sessionIDHeader); got != "" {
			t.Errorf("%s = %q, want empty", sessionIDHeader, got)
		}
		if got := request.Header.Values("X-Unrelated-Caller-Data"); len(got) != 0 {
			t.Errorf("unrelated caller header was forwarded: %q", got)
		}
		return serverHTTPResponse("{}"), nil
	})}
	client := newProviderClient(testProviderBaseURL, httpClient)
	client.streamIdleTimeout = time.Minute
	response, err := client.forwardExecution(t.Context(), t.Context(), []byte("{}"), headers, "cache-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.Body.(*streamIdleReadCloser); !ok {
		t.Fatalf("response body = %T, want stream idle wrapper", response.Body)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func codexAuthHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer caller-token")
	headers.Set(chatGPTAccountIDHeader, "caller-account")
	return headers
}

func TestProviderClientRejectsMissingOrMalformedCodexAuthentication(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{"missing authorization", http.Header{chatGPTAccountIDHeader: []string{"account"}}, "Authorization"},
		{"multiple authorizations", http.Header{"Authorization": []string{"Bearer one", "Bearer two"}, chatGPTAccountIDHeader: []string{"account"}}, "Authorization"},
		{"unsupported scheme", http.Header{"Authorization": []string{"Basic token"}, chatGPTAccountIDHeader: []string{"account"}}, "invalid bearer"},
		{"empty token", http.Header{"Authorization": []string{"Bearer "}, chatGPTAccountIDHeader: []string{"account"}}, "invalid bearer"},
		{"missing account", http.Header{"Authorization": []string{"Bearer token"}}, "ChatGPT-Account-ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var forwarded bool
			httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				forwarded = true
				return serverHTTPResponse("{}"), nil
			})}
			client := newProviderClient(testProviderBaseURL, httpClient)
			_, err := client.forwardExecution(t.Context(), t.Context(), []byte("{}"), test.headers, "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if forwarded {
				t.Fatal("request with invalid authentication reached upstream")
			}
		})
	}
}

func TestProviderClientOmitsUnsafeCodexCacheKey(t *testing.T) {
	for name, cacheKey := range map[string]string{
		"control byte": "cache\nkey",
	} {
		t.Run(name, func(t *testing.T) {
			var forwarded bool
			httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
				forwarded = true
				if values, ok := request.Header[codexSessionIDHeader]; ok {
					t.Errorf("%s = %q, want omitted", codexSessionIDHeader, values)
				}
				return serverHTTPResponse("{}"), nil
			})}
			client := newProviderClient(testProviderBaseURL, httpClient)
			response, err := client.forwardExecution(t.Context(), t.Context(), []byte("{}"), codexAuthHeaders(), cacheKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if !forwarded {
				t.Fatal("request did not reach upstream")
			}
		})
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestProviderClientPreservesCodexRequestHeadersAcrossRetries(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "session")
	headers.Set(threadIDHeader, "thread")
	headers.Set(clientRequestIDHeader, "client-request")
	headers.Set(codexWindowIDHeader, "window:0")
	headers.Set(codexBetaFeaturesHeader, "feature")
	headers.Set(codexResponsesLiteHeader, "true")
	headers.Set(codexTurnMetadataHeader, "metadata")
	headers.Set("x-codex-turn-state", "opaque-turn-state")
	var attempts []http.Header
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		attempts = append(attempts, request.Header.Clone())
		if len(attempts) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Retry-After": []string{"0"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"selected model is at capacity"}}`)),
			}, nil
		}
		return serverHTTPResponse("{}"), nil
	})}
	client := newProviderClient(testProviderBaseURL, httpClient)
	response, err := client.forwardExecution(t.Context(), t.Context(), []byte("{}"), headers, "cache-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("upstream attempts = %d, want 2", len(attempts))
	}
	for attempt, forwarded := range attempts {
		if got := forwarded[codexSessionIDHeader]; !slices.Equal(got, []string{"cache-key"}) {
			t.Errorf("attempt %d %s = %q, want cache key", attempt+1, codexSessionIDHeader, got)
		}
		if got := forwarded.Get(sessionIDHeader); got != "" {
			t.Errorf("attempt %d %s = %q, want empty", attempt+1, sessionIDHeader, got)
		}
		for _, name := range []string{threadIDHeader, clientRequestIDHeader, codexWindowIDHeader, codexBetaFeaturesHeader, codexResponsesLiteHeader, codexTurnMetadataHeader, "x-codex-turn-state"} {
			if got, want := forwarded.Values(name), headers.Values(name); !slices.Equal(got, want) {
				t.Errorf("attempt %d header %s = %q, want %q", attempt+1, name, got, want)
			}
		}
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestProviderClientCancelsRequestWithCodexRequestHeaders(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "session")
	started := make(chan http.Header, 1)
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		started <- request.Header.Clone()
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	client := newProviderClient(testProviderBaseURL, httpClient)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := client.forwardExecution(ctx, ctx, []byte("{}"), headers, "cache-key")
		result <- err
	}()

	var forwarded http.Header
	select {
	case forwarded = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not start")
	}
	if got := forwarded[codexSessionIDHeader]; !slices.Equal(got, []string{"cache-key"}) {
		t.Fatalf("forwarded %s = %q, want cache key", codexSessionIDHeader, got)
	}
	if got := forwarded.Get(sessionIDHeader); got != "" {
		t.Fatalf("forwarded %s = %q, want empty", sessionIDHeader, got)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled request error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not stop after cancellation")
	}
}

func TestCopyJSONTransformedRejectsBodyBeyondRouterBufferBudget(t *testing.T) {
	t.Parallel()
	_, err := copyJSONTransformed(io.Discard, io.LimitReader(serverRepeatingReader{}, upstreamJSONBufferBytes+1), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "router buffer budget") {
		t.Fatalf("error = %v, want router buffer budget rejection", err)
	}
}

func TestExecuteRequestUnsafeCacheKeyRetainsSessionAffinity(t *testing.T) {
	for _, key := range []string{" padded ", "line\nbreak"} {
		parsed := serverRequest(t, func(request map[string]any) { request["prompt_cache_key"] = key })
		original := bytes.Clone(parsed.originalBody)
		provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
		if err := executeRequest(t.Context(), t.Context(), parsed, http.Header{}, "stable-session", provider, io.Discard, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if provider.forwardedCacheKey[0] != "stable-session" {
			t.Fatal("unsafe body key disabled valid session affinity")
		}
		if !bytes.Equal(provider.forwarded[0], original) {
			t.Fatal("cache routing fallback rewrote client request")
		}
	}
}
