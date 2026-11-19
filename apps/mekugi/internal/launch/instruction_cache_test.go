package launch

import (
	"encoding/json"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestInstructionCacheAutomaticSuccessorCannotSilentlyDropChanges(t *testing.T) {
	original := json.RawMessage(`{"role":"developer","content":"rules"}`)
	exchange := confirmedHistoryExchange(t, []json.RawMessage{original}, true)
	request := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"previous_response_id": json.RawMessage(`"parent"`),
		"input":                responses.MustMarshalJSON([]json.RawMessage{original}),
	}}
	if reason, err := exchange.Reconcile(request, true); err != nil || reason != "matching_prefix" || request.CachedInput != 1 {
		t.Fatalf("confirmed automatic prefix = %q, %v; cached=%d", reason, err, request.CachedInput)
	}
	request.Fields["input"] = json.RawMessage(`[{"role":"developer","content":"changed"}]`)
	_, err := exchange.Reconcile(request, true)
	if err == nil || !strings.Contains(err.Error(), "cached provider history") {
		t.Fatalf("automatic successor silently accepted a different prefix: %v", err)
	}
}
