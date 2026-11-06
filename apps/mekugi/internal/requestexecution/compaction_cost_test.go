package requestexecution

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestResetEstimateUsesPreparedInstructionsAndReload(t *testing.T) {
	fields := map[string]any{"instructions": "base", "tools": []any{map[string]any{"type": "function", "name": "test"}}, "input": []any{
		map[string]any{"role": "developer", "content": "policy"},
		map[string]any{"role": "user", "content": "# AGENTS.md instructions\n<INSTRUCTIONS>policy</INSTRUCTIONS>"},
	}}
	count := func(extra ...any) uint64 {
		t.Helper()
		copy := maps.Clone(fields)
		copy["input"] = append(fields["input"].([]any)[:2:2], extra...)
		raw, _ := json.Marshal(copy)
		request, err := codexresponses.ParseRequest(raw)
		if err != nil {
			t.Fatal(err)
		}
		tokens, err := EstimateResetInput(&request, nil)
		if err != nil {
			t.Fatal(err)
		}
		return tokens
	}
	base := count()
	if got := count(map[string]any{"role": "user", "content": strings.Repeat("old task ", 1000)}, map[string]any{"type": "function_call_output", "output": strings.Repeat("old result ", 1000)}); got != base {
		t.Fatalf("retained discarded conversation: %d != %d", got, base)
	}
	if got := count(map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "<skill>old skill body</skill>"}, map[string]string{"type": "input_image", "image_url": "data:image/png,OLD"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"skills.instructions", "user.image"}}}, map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "<environment_context>quoted task</environment_context>"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"user.text"}}}); got != base {
		t.Fatalf("old skill/media or quoted context survived reset estimate: %d != %d", got, base)
	}
	raw, _ := json.Marshal(fields)
	request, _ := codexresponses.ParseRequest(raw)
	reload, _ := json.Marshal(strings.Repeat("selected nested skill ", 1000))
	if got, err := EstimateResetInput(&request, []jsontext.Value{reload}); err != nil || got <= base+1000 {
		t.Fatalf("reload missing from reset estimate: %d, %v", got, err)
	}
}
