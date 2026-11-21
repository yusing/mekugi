package codexbinding

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestEmptyPollRequiresCompleteOutput(t *testing.T) {
	call := binding.RolloutCall{Item: binding.RolloutItem{
		Type: "custom_tool_call", Name: "exec",
		Input: `text(await tools.write_stdin({session_id:42,chars:""}));`,
	}}
	for _, tc := range []struct {
		suffix string
		want   bool
	}{
		{"", true},
		{" (more output: wait with cell ID g1:7)", false},
	} {
		call.Outputs = []binding.RolloutItem{{Output: responses.MustMarshalJSON([]map[string]string{
			{"type": "input_text", "text": "Script completed" + tc.suffix + "\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": `{"session_id":42,"output":""}`},
		})}}
		if got := (callEvidence{call: call}).EmptyPoll(nil); got != tc.want {
			t.Errorf("empty poll with suffix %q = %v, want %v", tc.suffix, got, tc.want)
		}
	}
}
