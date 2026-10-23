package responses

import (
	"bytes"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// StandaloneFunctionOutput is client-supplied context, not a correlated tool
// completion. Its shape grants no evidence or execution authority.
// Source: codex-rs/protocol/src/models.rs:1156,3402@0df4aa677 FunctionCallOutput.
func StandaloneFunctionOutput(item map[string]jsontext.Value) bool {
	call := bytes.TrimSpace(item["call_id"])
	return responses.StringField(item, "type") == "function_call_output" && responses.StringField(item, "name") != "" && (len(call) == 0 || bytes.Equal(call, []byte("null")))
}
