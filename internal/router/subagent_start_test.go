package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSubagentStartReportsObservedModelOnce(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			root, _ := prepareActivityTest(t, proxy, "root-session", "root", "", "/root", nil)
			prepareChild := func(session, model, effort string) *mekugiResponseTransform {
				t.Helper()
				request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
					"model": model, "reasoning": map[string]any{"effort": effort},
					"input": []any{testCodeModeAdditionalTools(testCodeModeDescription)},
					"tools": []any{},
				}))
				if err != nil {
					t.Fatal(err)
				}
				child, err := proxy.prepareRequest(t.Context(), &request, session, "child", codexTurnMetadata{
					RequestKind: "turn", ThreadID: "child", ParentThreadID: "root",
					AgentName: "/root/explorer", SubagentKind: "thread_spawn",
				}, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(child.Close)
				return child
			}
			child := prepareChild("child-session", "gpt-effective", "high")
			answer := map[string]json.RawMessage{
				"type": mustMarshalJSON("message"), "id": mustMarshalJSON("answer"),
				"role": mustMarshalJSON("assistant"), "phase": mustMarshalJSON("final_answer"),
				"content": mustMarshalJSON([]any{map[string]any{"type": "output_text", "text": "Substantive answer."}}),
			}
			response := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{answer}})
			emit := func(transform *mekugiResponseTransform) []byte {
				t.Helper()
				if stream {
					events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
						"type": "response.completed", "response": json.RawMessage(response),
					}))
					if err != nil {
						t.Fatal(err)
					}
					return bytes.Join(events, nil)
				}
				result, err := transform.TransformJSON(response)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			if result := emit(child); bytes.Contains(result, []byte("Started.")) || bytes.Contains(result, []byte("Journal saved:")) || !bytes.Contains(result, []byte("Substantive answer.")) {
				t.Fatalf("start notice changed the child's result: %s", result)
			}
			result := emit(root)
			for _, want := range []string{"[`/root/explorer`] Started.", "Model: `gpt-effective`", "Reasoning effort: `high`"} {
				if !bytes.Contains(result, []byte(want)) {
					t.Fatalf("missing %q in %s", want, result)
				}
			}
			// SSE repeats the same item in the completed event and terminal snapshot.
			if bytes.Count(result, []byte("Started.")) != map[bool]int{false: 1, true: 2}[stream] {
				t.Fatalf("duplicate start: %s", result)
			}
			// A remapped session or a later model change must not announce another start.
			child.Close()
			prepareChild("remapped-child-session", "gpt-later", "low")
			root.Close()
			next, _ := prepareActivityTest(t, proxy, "remapped-root-session", "root", "", "/root", nil)
			if result := emit(next); bytes.Contains(result, []byte("Started.")) {
				t.Fatalf("start repeated on a later request: %s", result)
			}
			// Generated root notices remain user-only even in inherited child history.
			var notices []map[string]json.RawMessage
			if stream {
				// SSE output items are concatenated JSON objects; decode each event.
				decoder := json.NewDecoder(bytes.NewReader(result))
				for decoder.More() {
					var event struct {
						Item map[string]json.RawMessage `json:"item"`
					}
					if err := decoder.Decode(&event); err != nil {
						t.Fatal(err)
					}
					if event.Item != nil {
						notices = append(notices, event.Item)
					}
				}
			} else {
				var decoded struct{ Output []map[string]json.RawMessage }
				if err := json.Unmarshal(result, &decoded); err != nil {
					t.Fatal(err)
				}
				notices = decoded.Output
			}
			fields := map[string]json.RawMessage{"input": mustMarshalJSON(append(notices, answer))}
			proxy.activity.stripInput(fields)
			if strings.Contains(string(fields["input"]), "Started.") || !bytes.Contains(fields["input"], []byte("Substantive answer.")) {
				t.Fatalf("incorrect replay filtering: %s", fields["input"])
			}
		})
	}
}
