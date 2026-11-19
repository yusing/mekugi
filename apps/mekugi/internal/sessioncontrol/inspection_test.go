package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/testsupport/moduletest"

	"github.com/yusing/mekugi/contracts/observation"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestInspectionUsesLocalRuntimeHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{}
		requests := NewRequests[struct{}](transport)
		for _, target := range []string{"root", "foreign"} {
			done := make(chan error, 1)
			go func() {
				got, err := requests.Inspect(t.Context(), target)
				if target == "root" && ((got.Model == nil || *got.Model != "actual") || got.ContextTokens != nil || got.Compactions == nil || *got.Compactions != 0) {
					t.Errorf("snapshot = %+v", got)
				}
				done <- err
			}()
			call := <-requests.Inspections()
			call.Complete("root", observation.SessionDetails{Model: new("actual"), SessionMetrics: observation.SessionMetrics{Compactions: new(uint64(0))}})
			if err := <-done; (err != nil) != (target == "foreign") {
				t.Fatalf("target %s: %v", target, err)
			}
		}
		if requests.Pending() || len(transport.sends) != 0 {
			t.Fatal("inspection dispatched a host operation")
		}
	})
}

func TestAttachmentContextReadsHistoryOncePerWindowWithDirectInputDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "7"}
		requests := NewRequests[struct{}](transport)
		client := &Client[struct{}]{Requests: requests}
		client.Thread = "child"
		client.NativeInputPending = true
		if client.Calls() != nil {
			t.Fatal("ordinary host calls admitted with direct input disabled")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		mutation := make(chan error, 1)
		go func() {
			_, err := requests.call(ctx, client.Thread, "turn/start", map[string]any{"threadId": client.Thread})
			mutation <- err
		}()
		var cache composer.AttachmentContext
		draft := composer.Draft{Text: "[Selected 1]", Selections: []composer.Token{{Start: 0, End: 12, Text: "selected bytes"}}}
		draft.Attachments = []string{attachments.Encode(moduletest.SelectionFrames(draft.Text[draft.Selections[0].Start:draft.Selections[0].End], draft.Selections[0].Source, draft.Selections[0].Text))}
		if err := moduletest.IdentifyAttachments(&draft.Attachments, "source"); err != nil {
			t.Fatal(err)
		}
		content, _ := json.Marshal(moduletest.WithLegacyAttachments(conversationbinding.Input(draft), draft.Attachments))
		response, _ := json.Marshal(map[string]any{"thread": binding.ThreadInfo{ID: client.Thread, Turns: []binding.HistoryTurn{{Items: []binding.ThreadItem{{ID: "user", Type: "userMessage", Content: content}, {ID: "reset", Type: "contextCompaction", Status: "completed"}}}}}})
		for _, window := range []string{"one", "one", "two", "two"} {
			result := make(chan composer.AttachmentSnapshot, 1)
			failure := make(chan error, 1)
			go func() {
				values, err := requests.ContextAttachments(ctx, client.Thread, window)
				failure <- err
				result <- values
			}()
			query := <-requests.Inspections()
			_, cached := cache.Get(window)
			if query.Target() != client.Thread || query.ContextWindow() != window {
				t.Fatalf("unexpected cache inspection: target=%q window=%q", query.Target(), query.ContextWindow())
			}
			query.CompleteAttachments(client.Thread, &cache)
			if !cached {
				synctest.Wait()
				var inspection *Inspection
				select {
				case inspection = <-requests.Inspections():
				default:
					t.Fatal("cold-window history is not available on the inspection lane")
				}
				call := inspection.measurement
				var params struct {
					Thread  string `json:"threadId"`
					Include bool   `json:"includeTurns"`
				}
				if call == nil || json.Unmarshal(call.params, &params) != nil || call.method != "thread/read" || params.Thread != client.Thread || !params.Include {
					t.Fatalf("unexpected history acquisition: %+v", call)
				}
				if !requests.DispatchMeasurement(inspection) || !client.CompleteCall(appserver.Message{ID: jsontext.Value(`7`), Result: response}) {
					t.Fatal("history reply not consumed")
				}
				query = <-requests.Inspections()
				query.CompleteAttachments(client.Thread, &cache)
			}
			values := (<-result).Values
			if err := <-failure; err != nil {
				t.Fatal(err)
			}
			if len(values) != 1 || len(values[0].Selection.Frames) != 1 || !attachments.SelectionFrame(values[0].Selection.Frames[0]) {
				t.Fatalf("lost submitted selection: %+v", values)
			}
			values[0].Placeholders[0] = "changed"
			values[0].Selection.Frames[0] = "changed"
			again, _ := cache.Get(window)
			if again.Values[0].Placeholders[0] == "changed" || again.Values[0].Selection.Frames[0] == "changed" {
				t.Fatal("inspection mutated the scoped cache")
			}
		}
		if len(transport.sends) != 2 {
			t.Fatalf("history read count=%d", len(transport.sends))
		}
		cancel()
		err := <-mutation
		if failure, ok := errors.AsType[*binding.DispatchError](err); !ok || failure.Outcome != binding.NotDispatched || !errors.Is(err, context.Canceled) {
			t.Fatalf("direct-input mutation did not remain gated: %v", err)
		}
	})
}
