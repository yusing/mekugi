package requestexecution

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

// EstimateResetInput uses the prepared instruction/tool projection as the next
// reset's baseline, plus admitted reload results. It estimates text framing with
// the local tokenizer, not provider billing or future compact-hook output.
func EstimateResetInput(request *codexresponses.Request, reload []jsontext.Value) (uint64, error) {
	developers, additional := instructionItems(request.Fields["input"])
	items := append(developers, reload...)
	for _, item := range additional {
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(item, &fields); err != nil {
			return 0, err
		}
		if responses.StringField(fields, "role") != "developer" {
			items = append(items, item)
		}
	}
	context, err := binding.ResetUserContext(request.Fields["input"])
	if err != nil {
		return 0, err
	}
	items = append(items, context...)
	value, err := json.Marshal(map[string]any{"instructions": request.Fields["instructions"], "tools": request.Fields["tools"], "input": items}, json.Deterministic(true))
	if err != nil {
		return 0, err
	}
	codec, err := tokenizer.New()
	if err != nil {
		return 0, err
	}
	tokens, err := codec.Count(string(value))
	return uint64(tokens), err
}
