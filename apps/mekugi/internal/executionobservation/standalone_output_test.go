package executionobservation

import (
	"bytes"
	"encoding/json/jsontext"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestRetainedStandaloneOutputPreservesContextWithoutCompletion(t *testing.T) {
	for _, item := range []string{
		`{"type":"function_call_output","name":"wait_agent","namespace":"threadcontrol","output":"wake"}`,
		`{"type":"function_call_output","call_id":null,"name":"followup_task","namespace":"threadcontrol","output":{"text":"assignment"}}`,
	} {
		proxy := newManagedMekugiProxy(t)
		request := activityAdmissionRequest(t, []any{jsontext.Value(item)})
		before := bytes.Clone(request.Fields["input"])
		visible, err := proxy.reconcileVisibleInput(t.Context(), &request, t.TempDir(), "standalone")
		if err != nil || len(visible) != 0 || !bytes.Equal(before, request.Fields["input"]) {
			t.Fatalf("standalone output changed or became a completion: %v, %+v", err, visible)
		}
	}
}

func TestRetainedStandaloneOutputDoesNotRelaxHostCorrelation(t *testing.T) {
	for _, item := range []string{
		`{"type":"function_call_output","call_id":"","name":"wait_agent","output":"wake"}`,
		`{"type":"function_call_output","call_id":12,"name":"wait_agent","output":"wake"}`,
		`{"type":"function_call_output","output":"wake"}`,
		`{"type":"custom_tool_call_output","name":"wait_agent","output":"wake"}`,
		`{"type":"function_call","name":"wait_agent","arguments":"{}"}`,
	} {
		proxy := newManagedMekugiProxy(t)
		request, err := codexresponses.ParseRequest([]byte(`{"model":"fixture","input":[` + item + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.reconcileVisibleInput(t.Context(), &request, t.TempDir(), "invalid"); err == nil {
			t.Fatalf("uncorrelated invalid item accepted: %s", item)
		}
	}
}
