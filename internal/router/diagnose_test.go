package router

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportIssueOptInUsesOneRouterLocalFunctionAndStartupHookSnapshot(t *testing.T) {
	data := t.TempDir()
	output := filepath.Join(t.TempDir(), "report.txt")
	settings := `{"hooks":{"diagnose":["printf '%s\\n%s' {{shellquote .Title}} {{shellquote (format_markdown .Body)}} > ` + shellQuoteArgument(output) + `"]}}`
	if err := os.WriteFile(filepath.Join(data, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := buildToolRegistryForTest(t, t.Context(), data, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if !registry.diagnoseEnabled || len(registry.diagnoseHooks) != 1 {
		t.Fatalf("diagnose snapshot = %+v", registry.diagnoseHooks)
	}
	if _, exists := registry.frontends[reportIssueToolName]; exists {
		t.Fatal("router-local issue report gained an executable frontend")
	}
	if err := os.WriteFile(filepath.Join(data, "settings.json"), []byte(`{"hooks":{"diagnose":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithSharedTestRegistry(t, registry)
	index := filepath.Join(t.TempDir(), "session_index.jsonl")
	if err := os.WriteFile(index, []byte(`{"id":"session-1","thread_name":"Visible task"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy.titles = newSessionTitleCacheAt(index)
	attachTestReplayStore(t, proxy)
	transform, _, request, _ := newMekugiTestTransformWithProxy(t, proxy)
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tool := range tools {
		if jsonString(tool, "name") == reportIssueToolName {
			count++
			if jsonString(tool, "type") != "function" {
				t.Fatalf("report tool = %s", mustMarshalJSON(tool))
			}
		}
	}
	if count != 1 {
		t.Fatalf("report_issue specifications = %d", count)
	}
	call := map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call"), "id": mustMarshalJSON("issue-item"),
		"call_id": mustMarshalJSON("issue-call"), "name": mustMarshalJSON(reportIssueToolName),
		"arguments": mustMarshalJSON(`{"markdown":"The observed result was wrong."}`),
	}
	added := maps.Clone(call)
	added["arguments"] = mustMarshalJSON("")
	if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil {
		t.Fatal(err)
	}
	if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "issue-item", "delta": `{"markdown":"The observed result was wrong."}`})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("incomplete report ran hook: %v", err)
	}
	visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}))
	if err != nil || len(visible) != 1 || !strings.Contains(string(visible[0]), `"name":"report_issue"`) {
		t.Fatalf("completed report result = %q, %v", visible, err)
	}
	if len(transform.journalResults) != 1 {
		t.Fatalf("retained report results = %d", len(transform.journalResults))
	}
	first := transform.journalResults[0]
	if jsonString(first, "output") != "Issue reported." {
		t.Fatalf("first report = %s", mustMarshalJSON(first))
	}
	second, err := transform.executeRouterLocalCall(call)
	if err != nil || !sameJSONValue(mustMarshalJSON(first), mustMarshalJSON(second)) {
		t.Fatalf("duplicate report = %s, %v", mustMarshalJSON(second), err)
	}
	content, err := os.ReadFile(output)
	if err != nil || string(content) != "Visible task\nThe observed result was wrong." {
		t.Fatalf("hook output = %q, %v", content, err)
	}
	if !strings.Contains(jsonString(journalClientResult(first, reportIssueToolName), "id"), "fco_mekugi_journal_") {
		t.Fatal("client result lacks durable local-call identity")
	}
}

func TestReportIssueHookFailureIsAWarningAndDoesNotRunDisabled(t *testing.T) {
	registry := &toolRegistry{diagnoseEnabled: true, diagnoseHooks: diagnoseHooks{"exit 7"}}
	proxy := newProxyWithSharedTestRegistry(t, registry)
	attachTestReplayStore(t, proxy)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	call := map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON("issue-call"),
		"name": mustMarshalJSON(reportIssueToolName), "arguments": mustMarshalJSON(`{"markdown":"details"}`),
	}
	result, err := transform.executeReportIssueCall(call)
	if err != nil || !strings.Contains(jsonString(result, "output"), "mekugi: warning:") {
		t.Fatalf("hook failure result = %s, %v", mustMarshalJSON(result), err)
	}
	registry.diagnoseEnabled = false
	if _, err := transform.executeReportIssueCall(call); err == nil {
		t.Fatal("disabled issue report executed")
	}
}
