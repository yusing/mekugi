package router

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"
)

func newLiveDiffTurnTest(t *testing.T, workspace, thread string) (*autoLiveDiff, *liveDiffSubscriber) {
	t.Helper()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {thread: true}}})
	auto := &autoLiveDiff{
		events: broker,
		scope:  liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {thread: true}}},
	}
	auto.enabled.Store(true)
	sub := broker.subscribe()
	<-sub.events // retained scope
	return auto, sub
}

func requireLiveDiffTurnEvent(t *testing.T, sub *liveDiffSubscriber, status string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-sub.events:
			if event.Kind != "turn" {
				continue
			}
			if event.Status != status {
				t.Fatalf("turn event = %+v, want %s", event, status)
			}
			return
		case <-timer.C:
			t.Fatalf("missing %s turn event", status)
		}
	}
}

func requireNoLiveDiffTurnEvent(t *testing.T, sub *liveDiffSubscriber) {
	t.Helper()
	select {
	case event := <-sub.events:
		t.Fatalf("unexpected event: %+v", event)
	default: // Publication is synchronous; any unexpected event is already queued.
	}
}

func TestAutoLiveDiffTurnTransitionsAreIdentityBound(t *testing.T) {
	const workspace, thread = "/work", "root"
	auto, sub := newLiveDiffTurnTest(t, workspace, thread)
	valid := codexTurnMetadata{RequestKind: "turn", ThreadID: thread, TurnID: "one"}

	auto.beginTurn(workspace, thread, valid)
	requireLiveDiffTurnEvent(t, sub, "active")

	auto.beginTurn(workspace, thread, valid)
	auto.finishTurn(workspace, thread, "stale")
	requireNoLiveDiffTurnEvent(t, sub)

	next := valid
	next.TurnID = "two"
	auto.beginTurn(workspace, thread, next)
	requireLiveDiffTurnEvent(t, sub, "active")
	auto.finishTurn(workspace, thread, "one")
	requireNoLiveDiffTurnEvent(t, sub)
	auto.finishTurn(workspace, thread, "two")
	requireLiveDiffTurnEvent(t, sub, "completed")
	auto.finishTurn(workspace, thread, "two")
	requireNoLiveDiffTurnEvent(t, sub)

	next.TurnID = "three"
	auto.beginTurn(workspace, thread, next)
	requireLiveDiffTurnEvent(t, sub, "active")
}

func TestAutoLiveDiffTurnRejectsInvalidRequests(t *testing.T) {
	const workspace, thread = "/work", "root"
	auto, sub := newLiveDiffTurnTest(t, workspace, thread)
	for _, metadata := range []codexTurnMetadata{
		{RequestKind: "prewarm", ThreadID: thread, TurnID: "prewarm"},
		{RequestKind: "turn", SubagentKind: "thread_spawn", ThreadID: thread, TurnID: "child"},
		{RequestKind: "turn", ThreadID: "other", TurnID: "mismatch"},
		{RequestKind: "turn", ThreadID: thread},
		{RequestKind: "turn", ThreadID: thread, TurnID: "invalid", activityIdentityInvalid: true},
	} {
		auto.beginTurn(workspace, thread, metadata)
	}
	auto.beginTurn("/unobserved", thread, codexTurnMetadata{RequestKind: "turn", ThreadID: thread, TurnID: "unobserved"})
	requireNoLiveDiffTurnEvent(t, sub)
}

func TestRequestPreparationBeginsOnlyNewRootTurn(t *testing.T) {
	const thread = "thread-1"
	workspace := t.TempDir()
	proxy := newManagedMekugiProxy(t)
	auto, sub := newLiveDiffTurnTest(t, workspace, thread)
	proxy.autoLiveDiff = auto
	executor := requestExecutor{
		provider:    &serverFakeProvider{},
		issues:      NewCriticalErrors(),
		mekugiCalls: proxy,
		mentor:      newMentorHandoff(true, true),
	}
	headers := func(turnID string) http.Header {
		metadata := codexTurnMetadata{
			RequestKind: "turn", ThreadID: thread, TurnID: turnID,
			Directories: map[string]json.RawMessage{workspace: nil},
		}
		headers := make(http.Header)
		headers.Set(codexTurnMetadataHeader, string(mustMarshalJSON(metadata)))
		headers.Set(threadIDHeader, thread)
		return headers
	}
	prepare := func(ctx context.Context, turnID string) *requestAttempt {
		attempt := newRequestAttempt(executor, ctx, ctx, serverRequest(t, nil), headers(turnID), "session")
		if err := attempt.prepare(); err != nil {
			t.Fatalf("prepare %s: %v", turnID, err)
		}
		t.Cleanup(func() {
			if attempt.mekugiTransform != nil {
				attempt.mekugiTransform.Close()
			}
		})
		return attempt
	}

	prepare(t.Context(), "one")
	requireLiveDiffTurnEvent(t, sub, "active")
	prepare(t.Context(), "one")
	requireNoLiveDiffTurnEvent(t, sub)

	continuation := context.WithValue(t.Context(), journalContinuationKey{}, journalContinuation{})
	prepare(continuation, "two")
	requireNoLiveDiffTurnEvent(t, sub)

	prepare(t.Context(), "two")
	requireLiveDiffTurnEvent(t, sub, "active")
}

func TestLiveDiffJournalTerminalCompletesAfterTransformedDelivery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			name := map[bool]string{false: "json", true: "sse"}[stream] + map[bool]string{false: "/root", true: "/child"}[child]
			t.Run(name, func(t *testing.T) {
				transform, proxy, _, workspace := newMekugiTestTransform(t)
				auto, sub := newLiveDiffTurnTest(t, workspace, transform.threadID)
				proxy.autoLiveDiff = auto
				transform.shellTurnID = "journal-turn"
				transform.subagentTurn = child
				auto.beginTurn(workspace, transform.threadID, codexTurnMetadata{
					RequestKind: "turn", ThreadID: transform.threadID, TurnID: transform.shellTurnID,
				})
				requireLiveDiffTurnEvent(t, sub, "active")
				requestJournalFinish(t, transform)

				response := map[string]any{"id": "terminal", "status": "completed", "output": []any{}}
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
				requireNoLiveDiffTurnEvent(t, sub)
				for _, payload := range delivered {
					transform.Delivered(payload)
				}
				if child {
					requireNoLiveDiffTurnEvent(t, sub)
				} else {
					requireLiveDiffTurnEvent(t, sub, "completed")
					for _, payload := range delivered {
						transform.Delivered(payload)
					}
					requireNoLiveDiffTurnEvent(t, sub)
				}
			})
		}
	}
}

func TestLiveDiffCompletionMetricsOnlyPersistForSuccessfulFinal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"final", "tool-only", "failed"} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+"/"+scenario, func(t *testing.T) {
				t.Setenv("TMPDIR", t.TempDir())
				transform, proxy, _, workspace := newMekugiTestTransform(t)
				turnID := "usage-off-" + map[bool]string{false: "json", true: "sse"}[stream] + "-" + scenario
				transform.shellTurnID = turnID
				transform.usageTracker = proxy.usage.observationForTurn(transform.shellThreadID, transform.shellThreadID, turnID, "gpt-6-sol", "")
				transform.observeResponseUsage(tokenCounts{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5})
				auto, sub := newLiveDiffTurnTest(t, workspace, transform.shellThreadID)
				proxy.autoLiveDiff = auto
				auto.beginTurn(workspace, transform.shellThreadID, codexTurnMetadata{
					RequestKind: "turn", ThreadID: transform.shellThreadID, TurnID: turnID,
				})
				requireLiveDiffTurnEvent(t, sub, "active")

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
					requireNoLiveDiffTurnEvent(t, sub) // Transformation is not downstream delivery.
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
					events = append(events, finalAnswerTestTerminal(t, status, false))
					var terminal [][]byte
					for i, event := range events {
						visible, err := transform.TransformSSE(event)
						if err != nil {
							t.Fatalf("transform event %d: %v", i, err)
						}
						requireNoLiveDiffTurnEvent(t, sub)
						if i == len(events)-1 {
							terminal = visible
							continue
						}
						for _, payload := range visible {
							transform.Delivered(payload)
						}
						requireNoLiveDiffTurnEvent(t, sub)
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
							requireNoLiveDiffTurnEvent(t, sub)
						}
						if i < len(terminal)-1 && envelope.Type == "response.completed" {
							t.Fatal("terminal event unexpectedly preceded trailing transformed output")
						}
					}
				}

				if scenario == "final" {
					requireLiveDiffTurnEvent(t, sub, "completed")
				} else {
					requireNoLiveDiffTurnEvent(t, sub)
				}
				if len(proxy.tokenMetricPaths()) != map[bool]int{true: 1, false: 0}[scenario == "final"] {
					t.Fatalf("scenario %s persisted unexpected token metrics: %q", scenario, proxy.tokenMetricPaths())
				}
			})
		}
	}
}
