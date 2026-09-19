package router

import (
	"bytes"
	"context"
	"encoding/json"
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
	case <-time.After(25 * time.Millisecond):
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

func TestLiveDiffTurnCompletesOnlyWhenRetainedUsageIsDelivered(t *testing.T) {
	const workspace, thread, turnID = "/work", "root", "turn"
	auto, sub := newLiveDiffTurnTest(t, workspace, thread)
	auto.beginTurn(workspace, thread, codexTurnMetadata{RequestKind: "turn", ThreadID: thread, TurnID: turnID})
	requireLiveDiffTurnEvent(t, sub, "active")

	transform := &mekugiResponseTransform{
		proxy:           &mekugiProxy{autoLiveDiff: auto},
		directory:       workspace,
		threadID:        thread,
		shellTurnID:     turnID,
		liveDiffUsageID: "usage",
	}
	transform.Delivered([]byte(`{"type":"response.output_item.done","item":{"id":"tool","type":"function_call"}}`))
	requireNoLiveDiffTurnEvent(t, sub)
	if transform.liveDiffUsageID != "usage" {
		t.Fatal("unrelated delivery consumed retained usage identity")
	}

	transform.Delivered([]byte(`{"type":"response.output_item.done","item":{"id":"usage","type":"message"}}`))
	requireLiveDiffTurnEvent(t, sub, "completed")
	if transform.liveDiffUsageID != "" {
		t.Fatal("delivered usage identity was not consumed")
	}
	transform.Delivered([]byte(`{"type":"response.output_item.done","item":{"id":"usage","type":"message"}}`))
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
