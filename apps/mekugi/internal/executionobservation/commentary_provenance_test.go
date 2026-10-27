package executionobservation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/legacycall"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// cancelLookupContext schedules cancellation at a synchronous context check,
// avoiding a race between filesystem validation and a timer in the test.
type cancelLookupContext struct {
	context.Context
	checks atomic.Int64
	after  int64
	cancel context.CancelFunc
}

func (ctx *cancelLookupContext) Err() error {
	if ctx.checks.Add(1) == ctx.after {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestVisibleHistoryValidationPrecedesLaterCancellation(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, legacycall.Name("workspace", "invalid", true)), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	proxy := testProxyWithStore(store)
	request := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{assistantCommentaryMessage("invalid", "text")}),
	}}
	// Calibrate admission and first-selection checks without binding the test
	// to the lock implementation's number of cancellation checks.
	probe := &cancelLookupContext{Context: t.Context()}
	_, want := proxy.reconcileVisibleInput(probe, request, "workspace", "session")
	if want == nil {
		t.Fatal("invalid retained record was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	controlled := &cancelLookupContext{Context: ctx, after: probe.checks.Load() + 1, cancel: cancel}
	request.Fields["input"] = mustTestJSON(t, []any{assistantCommentaryMessage("invalid", "text"), assistantCommentaryMessage("later", "text")})
	_, got := proxy.reconcileVisibleInput(controlled, request, "workspace", "session")
	if ctx.Err() == nil || got == nil || got.Error() != want.Error() {
		t.Fatalf("earlier validation error lost to cancellation: got %v, want %v, canceled=%v", got, want, ctx.Err())
	}
}

func BenchmarkReconcileVisibleInput(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			store, err := openMekugiReplayStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			proxy := testProxyWithStore(store)
			items := make([]any, 0, count*3)
			for i := range count {
				id := fmt.Sprint(i)
				items = append(items, assistantCommentaryMessage(id, "model-authored"),
					map[string]string{"type": "function_call", "call_id": id, "name": "functions.exec", "arguments": "{}"},
					map[string]string{"type": "function_call_output", "call_id": id, "output": "done"})
			}
			raw := responseevents.MustMarshalJSON(items)
			ctx, release, err := store.beginSession(b.Context(), "benchmark", "benchmark")
			if err != nil {
				b.Fatal(err)
			}
			defer release()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				request := &codexresponses.Request{Fields: map[string]json.RawMessage{"input": raw}}
				if _, err := proxy.reconcileVisibleInput(ctx, request, "workspace", "session"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestCommentaryReplayFilteringRequiresExactRetainedID(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	known := "retained-commentary"
	unknown := "unrelated-commentary"
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error {
		return store.write(replayRecord{Version: 1, Workspace: "workspace", CallID: known, Commentary: true})
	}); err != nil {
		t.Fatal(err)
	}

	request := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{
			assistantCommentaryMessage(known, "router-authored"),
			assistantCommentaryMessage(unknown, "model-authored"),
		}),
	}}
	proxy := testProxyWithStore(store)
	if _, err := proxy.reconcileVisibleInput(t.Context(), request, "workspace", "session"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request.Fields["input"], []byte(known)) {
		t.Fatalf("known router commentary survived replay: %s", request.Fields["input"])
	}
	if !bytes.Contains(request.Fields["input"], []byte(unknown)) {
		t.Fatalf("unretained prefix-matching message was removed: %s", request.Fields["input"])
	}
	peer, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.locked(t.Context(), func(peer *mekugiReplayStore) error {
		return peer.write(replayRecord{Version: 1, Workspace: "workspace", CallID: unknown, Commentary: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.reconcileVisibleInput(t.Context(), request, "workspace", "session"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request.Fields["input"], []byte(unknown)) {
		t.Fatalf("peer-published commentary survived the next request: %s", request.Fields["input"])
	}
	if err := os.WriteFile(filepath.Join(store.directory, legacycall.Name("workspace", known, true)), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	request.Fields["input"] = mustTestJSON(t, []any{assistantCommentaryMessage(known, "router-authored")})
	if _, err := proxy.reconcileVisibleInput(t.Context(), request, "workspace", "session"); err == nil {
		t.Fatal("a previously valid commentary record bypassed validation")
	}
}

// assistantCommentaryMessage creates an assistant commentary message with the given ID and text.
func assistantCommentaryMessage(id, text string) map[string]json.RawMessage {
	type outputText struct {
		Annotations []any  `json:"annotations"`
		Text        string `json:"text"`
		Type        string `json:"type"`
	}
	type outputMessage struct {
		Type    string       `json:"type"`
		ID      string       `json:"id,omitempty"`
		Role    string       `json:"role"`
		Status  string       `json:"status"`
		Phase   string       `json:"phase"`
		Content []outputText `json:"content"`
	}
	encoded := responseevents.MustMarshalJSON(outputMessage{
		Role:   "assistant",
		Type:   "message",
		ID:     id,
		Status: "completed",
		Phase:  "commentary",
		Content: []outputText{{
			Type:        "output_text",
			Text:        text,
			Annotations: []any{},
		}},
	})
	var message map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &message); err != nil {
		panic(err)
	}
	return message
}

// assistantCommentaryDoneEvent creates a response.output_item.done event for a commentary message.
func assistantCommentaryDoneEvent(message map[string]json.RawMessage) []byte {
	return responseevents.MustMarshalJSON(struct {
		Type string                     `json:"type"`
		Item map[string]json.RawMessage `json:"item"`
	}{
		Type: responseevents.OutputItemDone,
		Item: message,
	})
}
