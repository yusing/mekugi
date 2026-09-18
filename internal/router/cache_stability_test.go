package router

import (
	"strings"
	"testing"
)

func TestKnownReplayCarrierRejectsTamperedIdentity(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	history := mekugiHistory{CarrierName: "exec", Patch: "patch", Report: "report"}
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{"call-1": history}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		kind string
		tool string
		want string
	}{
		{name: "name", kind: "custom_tool_call", tool: "lookup", want: "changed carrier name"},
		{name: "type", kind: "function_call", tool: "exec", want: "changed item type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{map[string]any{
				"type": test.kind, "name": test.tool, "call_id": "call-1", "input": history.carrierInput(),
			}}}))
			if err != nil {
				t.Fatal(err)
			}
			err = proxy.reconcileInputPrefix(&request, "session")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestKnownReplayOutputRemainsValid(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{"call-1": {CarrierName: "exec"}}); err != nil {
		t.Fatal(err)
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{map[string]any{
		"type": "custom_tool_call_output", "call_id": "call-1", "output": "ok",
	}}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&request, "session"); err != nil {
		t.Fatal(err)
	}
}
