package router

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func newLiveDiffTurnTest(t *testing.T, workspace, thread string) (*previewmodel.Session, *sessionreview.Subscription) {
	t.Helper()
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {thread: true}}})
	auto := broker
	auto.Enable()
	sub := testReview(broker).Subscribe()
	<-sub.Events() // retained scope
	return auto, sub
}

func requireLiveDiffTurnEvent(t *testing.T, review *sessionreview.Review, sub *sessionreview.Subscription, status string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		for _, event := range review.TakePreviews(sub) {
			if event.Kind != "turn" {
				continue
			}
			if event.Status != status {
				t.Fatalf("turn event = %+v, want %s", event, status)
			}
			return
		}
		select {
		case <-sub.Ready():
		case <-timer.C:
			t.Fatalf("missing %s turn event", status)
		}
	}
}

func requireNoLiveDiffTurnEvent(t *testing.T, review *sessionreview.Review, sub *sessionreview.Subscription) {
	t.Helper()
	if events := review.TakePreviews(sub); len(events) != 0 {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestAutoLiveDiffTurnTransitionsAreIdentityBound(t *testing.T) {
	const workspace, thread = "/work", "root"
	auto, sub := newLiveDiffTurnTest(t, workspace, thread)
	valid := codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: thread, TurnID: "one"}

	beginLiveDiffTurn(auto, workspace, thread, valid)
	requireLiveDiffTurnEvent(t, testReview(auto), sub, "active")

	beginLiveDiffTurn(auto, workspace, thread, valid)
	auto.FinishTurn(workspace, thread, "stale")
	requireNoLiveDiffTurnEvent(t, testReview(auto), sub)

	next := valid
	next.TurnID = "two"
	beginLiveDiffTurn(auto, workspace, thread, next)
	requireLiveDiffTurnEvent(t, testReview(auto), sub, "active")
	auto.FinishTurn(workspace, thread, "one")
	requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
	auto.FinishTurn(workspace, thread, "two")
	requireLiveDiffTurnEvent(t, testReview(auto), sub, "completed")
	auto.FinishTurn(workspace, thread, "two")
	requireNoLiveDiffTurnEvent(t, testReview(auto), sub)

	next.TurnID = "three"
	beginLiveDiffTurn(auto, workspace, thread, next)
	requireLiveDiffTurnEvent(t, testReview(auto), sub, "active")
}

func TestAutoLiveDiffTurnRejectsInvalidRequests(t *testing.T) {
	const workspace, thread = "/work", "root"
	auto, sub := newLiveDiffTurnTest(t, workspace, thread)
	for _, metadata := range []codexresponses.TurnMetadata{
		{RequestKind: "prewarm", ThreadID: thread, TurnID: "prewarm"},
		{RequestKind: "turn", SubagentKind: "thread_spawn", ThreadID: thread, TurnID: "child"},
		{RequestKind: "turn", ThreadID: "other", TurnID: "mismatch"},
		{RequestKind: "turn", ThreadID: thread},
		{RequestKind: "turn", ThreadID: thread, TurnID: "invalid", ActivityIdentityInvalid: true},
	} {
		beginLiveDiffTurn(auto, workspace, thread, metadata)
	}
	beginLiveDiffTurn(auto, "/unobserved", thread, codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: thread, TurnID: "unobserved"})
	requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
}

func TestRequestPreparationBeginsOnlyNewRootTurn(t *testing.T) {
	const thread = "thread-1"
	workspace := t.TempDir()
	proxy := newManagedMekugiProxy(t)
	auto, sub := newLiveDiffTurnTest(t, workspace, thread)

	proxy.review = testReview(auto)
	headers := func(turnID string) http.Header {
		metadata := codexresponses.TurnMetadata{
			RequestKind: "turn", ThreadID: thread, TurnID: turnID,
			Directories: map[string]json.RawMessage{workspace: nil},
		}
		headers := make(http.Header)
		headers.Set(codexresponses.TurnMetadataHeader, string(responses.MustMarshalJSON(metadata)))
		headers.Set(codexresponses.ThreadIDHeader, thread)
		return headers
	}
	prepare := func(ctx context.Context, turnID string) {
		request := serverRequest(t, nil)
		metadata, valid := codexresponses.DecodeTurnMetadata(headers(turnID))
		prepared, err := proxy.Prepare(ctx, &request, requestexecution.Preparation{Session: "session", Thread: thread, Metadata: metadata, MetadataValid: valid})
		if err != nil {
			t.Fatalf("prepare %s: %v", turnID, err)
		}
		if prepared != nil {
			t.Cleanup(prepared.Close)
		}
	}

	prepare(t.Context(), "one")
	requireLiveDiffTurnEvent(t, testReview(auto), sub, "active")
	prepare(t.Context(), "one")
	requireNoLiveDiffTurnEvent(t, testReview(auto), sub)

	prepare(t.Context(), "two")
	requireLiveDiffTurnEvent(t, testReview(auto), sub, "active")
}

func TestLiveDiffTerminalCompletesAfterTransformedDelivery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			name := map[bool]string{false: "json", true: "sse"}[stream] + map[bool]string{false: "/root", true: "/child"}[child]
			t.Run(name, func(t *testing.T) {
				transform, proxy, _, workspace := newMekugiTestTransform(t)
				auto, sub := newLiveDiffTurnTest(t, workspace, transform.threadID)

				proxy.review = testReview(auto)
				transform.shellTurnID = "host-turn"
				transform.subagentTurn = child
				beginLiveDiffTurn(auto, workspace, transform.threadID, codexresponses.TurnMetadata{
					RequestKind: "turn", ThreadID: transform.threadID, TurnID: transform.shellTurnID,
				})
				requireLiveDiffTurnEvent(t, testReview(auto), sub, "active")

				response := map[string]any{"id": "terminal", "status": "completed", "output": []any{map[string]any{
					"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Finished."}},
				}}}
				var delivered [][]byte
				if stream {
					var err error
					delivered, err = transform.TransformSSE(mustTestJSON(t, map[string]any{
						"type": "response.completed", "response": response,
					}))
					if err != nil {
						t.Fatal(err)
					}
				} else {
					wire, err := transform.TransformJSON(mustTestJSON(t, response))
					if err != nil {
						t.Fatal(err)
					}
					delivered = [][]byte{wire}
				}
				if len(bytes.Join(delivered, nil)) == 0 {
					t.Fatal("terminal transform emitted no payload")
				}
				requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
				for _, payload := range delivered {
					transform.Delivered(payload)
				}
				if child {
					requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
				} else {
					requireLiveDiffTurnEvent(t, testReview(auto), sub, "completed")
					for _, payload := range delivered {
						transform.Delivered(payload)
					}
					requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
				}
			})
		}
	}
}

func TestLiveDiffCompletionOnlyForSuccessfulFinal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"final", "tool-only", "terminal-tool", "failed"} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+"/"+scenario, func(t *testing.T) {
				t.Setenv("TMPDIR", t.TempDir())
				transform, proxy, _, workspace := newMekugiTestTransform(t)
				turnID := "usage-off-" + map[bool]string{false: "json", true: "sse"}[stream] + "-" + scenario
				transform.shellTurnID = turnID
				auto, sub := newLiveDiffTurnTest(t, workspace, transform.shellThreadID)

				proxy.review = testReview(auto)
				beginLiveDiffTurn(auto, workspace, transform.shellThreadID, codexresponses.TurnMetadata{
					RequestKind: "turn", ThreadID: transform.shellThreadID, TurnID: turnID,
				})
				requireLiveDiffTurnEvent(t, testReview(auto), sub, "active")

				ordinaryAnswer := map[string]any{
					"type": "message", "id": "final-answer", "role": "assistant", "phase": "final_answer", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Finished the task."}},
				}
				toolCall := map[string]any{
					"type": "function_call", "id": "tool-call-item", "call_id": "tool-call", "name": "ordinary_tool",
					"arguments": "{}", "status": "completed",
				}
				status := "completed"
				var output []any
				switch scenario {
				case "final":
					output = []any{ordinaryAnswer}
				case "tool-only":
					output = []any{toolCall}
				case "terminal-tool":
					output = []any{ordinaryAnswer, toolCall}
				case "failed":
					status, output = "failed", []any{ordinaryAnswer}
				}

				if !stream {
					wire := mustTestJSON(t, map[string]any{"id": "usage-off-json-response", "status": status, "output": output})
					transformed, err := transform.TransformJSON(wire)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(transformed, []byte("Router session usage")) {
						t.Fatalf("off mode emitted a usage report: %s", transformed)
					}
					requireNoLiveDiffTurnEvent(t, testReview(auto), sub) // Transformation is not downstream delivery.
					transform.Delivered(transformed)
				} else {
					var events [][]byte
					if scenario == "tool-only" {
						added := maps.Clone(toolCall)
						added["status"] = "in_progress"
						events = append(events,
							mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": added}),
							mustTestJSON(t, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": toolCall}),
						)
					} else {
						events = append(events, finalAnswerTestEvents(t, "final_answer")...)
					}
					terminalEvent := finalAnswerTestTerminal(t, status, false)
					if scenario == "terminal-tool" {
						terminalEvent = mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{
							"id": "terminal-tool", "status": "completed", "output": output,
						}})
					}
					events = append(events, terminalEvent)
					var terminal [][]byte
					for i, event := range events {
						visible, err := transform.TransformSSE(event)
						if err != nil {
							t.Fatalf("transform event %d: %v", i, err)
						}
						requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
						if i == len(events)-1 {
							terminal = visible
							continue
						}
						for _, payload := range visible {
							transform.Delivered(payload)
						}
						requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
					}
					if len(terminal) == 0 {
						t.Fatal("terminal event was not transformed")
					}
					for i, payload := range terminal {
						transform.Delivered(payload)
						var envelope struct {
							Type string `json:"type"`
						}
						if err := json.Unmarshal(payload, &envelope); err != nil {
							t.Fatal(err)
						}
						if envelope.Type != "response.completed" && envelope.Type != "response.failed" {
							requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
						}
						if i < len(terminal)-1 && envelope.Type == "response.completed" {
							t.Fatal("terminal event unexpectedly preceded trailing transformed output")
						}
					}
				}

				if scenario == "final" {
					requireLiveDiffTurnEvent(t, testReview(auto), sub, "completed")
				} else {
					requireNoLiveDiffTurnEvent(t, testReview(auto), sub)
				}
			})
		}
	}
}
