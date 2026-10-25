package executionobservation

import (
	"bytes"

	"encoding/json"

	"slices"
	"strings"

	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestPrewarmProjectionMatchesFirstTurnPrefix(t *testing.T) {
	const conflictingProgress = "As you work, you send messages to the `commentary` channel."
	stock, projected := recordedInstructionCleanup(t)
	for _, topLevel := range []bool{false, true} {
		t.Run(map[bool]string{false: "additional tools", true: "top-level tools"}[topLevel], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			leading := []any{
				map[string]any{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + "\n" + stock + "\n" + instructionOmitStart + "omitted-rtk-policy" + instructionOmitEnd},
				map[string]any{"type": "message", "role": "user", "content": "<environment_context>cwd</environment_context>"},
			}
			tools := []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
			if topLevel {
				tools = append(testExecResponsesTools(), tools...)
			} else {
				leading = append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, leading...)
			}
			fields := func(input []any) map[string]any {
				return map[string]any{
					"model": "gpt-test", "instructions": testBaseInstructions + "\n" + stock, "tools": tools, "tool_choice": "auto",
					"parallel_tool_calls": true, "reasoning": map[string]any{"effort": "high"}, "input": input,
				}
			}
			warmFields := fields(leading)
			warmFields["generate"] = false
			warm, err := codexresponses.ParseRequest(mustTestJSON(t, warmFields))
			if err != nil {
				t.Fatal(err)
			}
			if transform, err := proxy.prepareModelRequest(t.Context(), &warm, "", "", codexresponses.TurnMetadata{RequestKind: "prewarm"}, true, true); err != nil || transform != nil {
				t.Fatalf("prewarm projection: %v, %v", transform, err)
			}
			turn, err := codexresponses.ParseRequest(mustTestJSON(t, fields(append(slices.Clone(leading), map[string]any{"type": "message", "role": "user", "content": "task"}))))
			if err != nil {
				t.Fatal(err)
			}
			transform, err := proxy.prepareRequest(t.Context(), &turn, "prefix-session", "prefix-thread", codexresponses.TurnMetadata{
				RequestKind: "turn", Directories: map[string]json.RawMessage{t.TempDir(): nil},
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(transform.Close)

			for name, value := range warm.Fields {
				if name == "input" || name == "generate" {
					continue
				}
				if !responses.SameJSONValue(value, turn.Fields[name]) {
					t.Errorf("first turn changed prewarmed %s:\nprewarm: %s\nturn:    %s", name, value, turn.Fields[name])
				}
			}
			for name := range turn.Fields {
				if _, present := warm.Fields[name]; !present && name != "input" {
					t.Errorf("first turn added %s absent from the prewarm", name)
				}
			}
			var warmInput, turnInput []json.RawMessage
			if err := json.Unmarshal(warm.Fields["input"], &warmInput); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(turn.Fields["input"], &turnInput); err != nil {
				t.Fatal(err)
			}
			if len(turnInput) != len(warmInput)+1 {
				t.Fatalf("first turn input items = %d, want prewarm %d plus the user message", len(turnInput), len(warmInput))
			}
			for index := range warmInput {
				if !responses.SameJSONValue(warmInput[index], turnInput[index]) {
					t.Errorf("first turn changed prewarmed input item %d:\nprewarm: %s\nturn:    %s", index, warmInput[index], turnInput[index])
				}
			}
			if got := responses.StringField(warm.Fields, "instructions"); got != testBaseInstructions+"\n"+projected+"\n\n"+proxy.registry.FrontendGuidance.Workflow {
				t.Fatalf("effective instruction cleanup = %q", got)
			}
			for _, raw := range warmInput {
				var item map[string]json.RawMessage
				_ = json.Unmarshal(raw, &item)
				if responses.StringField(item, "type") == "message" && responses.StringField(item, "role") == "developer" && responses.StringField(item, "content") != "Follow the task.\n\n"+projected+"\n"+instructionOmitStart+"omitted-rtk-policy"+instructionOmitEnd {
					t.Fatal("effective developer cleanup differs from recorded projection")
				}
			}
			for _, guidance := range []string{"Default output composes selected captured diffs", "--history shows individual attempts", "--net is a compatibility alias", "Piping partial stdout can modify the index", "validate the collected output", "pure moves use display-only headers"} {
				if !strings.Contains(string(warm.Fields["tools"])+string(warm.Fields["input"]), guidance) {
					t.Errorf("prepared frontend guidance lacks %q", guidance)
				}
			}
			// The comparison is meaningful only if the prewarm was projected.
			if bytes.Contains(warm.Fields["input"], []byte(conflictingProgress)) || !bytes.Contains(append(slices.Clone(warm.Fields["tools"]), warm.Fields["input"]...), []byte(frontendGuidanceStart)) {
				t.Fatalf("prewarm skipped Mekugi projection: %s", responses.MustMarshalJSON(warm.Fields))
			}
		})
	}
}

func TestPrewarmUnsupportedCatalogRemainsNative(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	stock, _ := recordedInstructionCleanup(t)
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "generate": false, "instructions": stock, "tools": []any{map[string]string{"type": "web_search"}}, "input": []any{}}))
	if err != nil {
		t.Fatal(err)
	}
	before := responses.MustMarshalJSON(request.Fields)
	transform, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexresponses.TurnMetadata{RequestKind: "prewarm"}, true, true)
	if err != nil || transform != nil {
		t.Fatalf("native handshake initialized execution or failed: %v, %v", transform, err)
	}
	if !responses.SameJSONValue(before, responses.MustMarshalJSON(request.Fields)) {
		t.Fatal("unsupported prewarm catalog was partially rewritten")
	}
}

func TestPrewarmMalformedCatalogRejectsWithoutPanic(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	for _, catalog := range []string{`[42]`, `"invalid"`, `[{"type":"function","name":"lookup"},42]`} {
		request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-test","generate":false,"tools":` + catalog + `,"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexresponses.TurnMetadata{RequestKind: "prewarm"}, true, true); err == nil {
			t.Fatalf("malformed catalog accepted: %s", catalog)
		}
	}
}
