package executionobservation

import (
	"bytes"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"

	json "encoding/json/v2"

	"testing"
)

func TestFinalAnswerStreamTerminalFinishesPartialLifecycle(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		for _, scenario := range []string{"completed", "failed", "incomplete", "already-done"} {
			t.Run(map[bool]string{false: "main", true: "child"}[child]+"/"+scenario, func(t *testing.T) {
				transform, _, _, _ := newMekugiTestTransform(t)
				transform.subagentTurn = child
				answer := finalAnswerTestEvents(t, "final_answer")
				prefix := answer[:3]
				status := scenario
				if scenario == "already-done" {
					prefix, status = answer, "completed"
				}
				for _, event := range prefix {
					event, err := responses.ReplaceField(event, "output_index", mustTestJSON(t, 7))
					if err != nil {
						t.Fatal(err)
					}
					visible, err := transform.TransformSSE(event)
					if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], event) {
						t.Fatalf("answer did not stream: %s, %v", visible, err)
					}
				}
				var done struct {
					Item jsonv1.RawMessage `json:"item"`
				}
				if err := json.Unmarshal(answer[len(answer)-1], &done); err != nil {
					t.Fatal(err)
				}
				terminal := mustTestJSON(t, map[string]any{"type": "response." + status, "response": map[string]any{"id": "partial", "status": status, "output": []jsonv1.RawMessage{done.Item}}})
				visible, err := transform.TransformSSE(terminal)
				if err != nil {
					t.Fatal(err)
				}
				wantDone := 0
				if scenario == "completed" {
					wantDone = 1
				}
				if len(visible) != wantDone+1 {
					t.Fatalf("terminal output = %s", visible)
				}
				if wantDone == 1 {
					var event struct {
						Type        string            `json:"type"`
						OutputIndex int               `json:"output_index"`
						Item        jsonv1.RawMessage `json:"item"`
					}
					if err := json.Unmarshal(visible[0], &event); err != nil {
						t.Fatal(err)
					}
					if event.Type != "response.output_item.done" || event.OutputIndex != 7 || !bytes.Equal(event.Item, done.Item) {
						t.Fatalf("answer identity or content changed: %s", visible[0])
					}
				}
				var terminalEvent struct {
					Type     string `json:"type"`
					Response struct {
						Output []jsonv1.RawMessage `json:"output"`
					} `json:"response"`
				}
				if err := json.Unmarshal(visible[len(visible)-1], &terminalEvent); err != nil {
					t.Fatal(err)
				}
				if terminalEvent.Type != "response."+status || len(terminalEvent.Response.Output) != 1 || !bytes.Equal(terminalEvent.Response.Output[0], done.Item) {
					t.Fatalf("terminal changed: %s", visible)
				}
				if repeated, err := transform.TransformSSE(terminal); err != nil || len(repeated) != 1 {
					t.Fatalf("repeated terminal duplicated completion: %s, %v", repeated, err)
				}
			})
		}
	}
}
