package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
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
			call.Complete("root", control.SessionDetails{Model: new("actual"), Compactions: new(uint64(0))})
			if err := <-done; (err != nil) != (target == "foreign") {
				t.Fatalf("target %s: %v", target, err)
			}
		}
		if requests.Pending() || len(transport.sends) != 0 {
			t.Fatal("inspection dispatched a host operation")
		}
	})
}

func TestAttachmentContextReadsHistoryOncePerWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "7"}
		requests := NewRequests[struct{}](transport)
		var cache composer.AttachmentContext
		draft := composer.Draft{Text: "[Selected 1]", Selections: []composer.Token{{Start: 0, End: 12, Text: "selected bytes"}}}
		draft.SnapshotFiles("")
		if err := draft.IdentifyAttachments("source"); err != nil {
			t.Fatal(err)
		}
		content, _ := json.Marshal(conversationbinding.Input(draft))
		response, _ := json.Marshal(map[string]any{"thread": binding.ThreadInfo{ID: "root", Turns: []binding.HistoryTurn{{Items: []binding.ThreadItem{{ID: "user", Type: "userMessage", Content: content}, {ID: "reset", Type: "contextCompaction", Status: "completed"}}}}}})
		for _, window := range []string{"one", "one", "two", "two"} {
			result := make(chan composer.AttachmentSnapshot, 1)
			failure := make(chan error, 1)
			go func() {
				values, err := requests.ContextAttachments(t.Context(), "root", window)
				failure <- err
				result <- values
			}()
			query := <-requests.Inspections()
			_, cached := cache.Get(window)
			query.CompleteAttachments("root", &cache)
			if !cached {
				call := <-requests.Calls()
				var params struct {
					Thread  string `json:"threadId"`
					Include bool   `json:"includeTurns"`
				}
				if json.Unmarshal(call.params, &params) != nil || call.method != "thread/read" || params.Thread != "root" || !params.Include {
					t.Fatalf("unexpected history acquisition: %s %s", call.method, call.params)
				}
				requests.Dispatch(call)
				if !requests.CompleteCall(appserver.Message{ID: jsontext.Value(`7`), Result: response}) {
					t.Fatal("history reply not consumed")
				}
				query = <-requests.Inspections()
				query.CompleteAttachments("root", &cache)
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
	})
}

func TestCurrentAttachmentsReadsUncachedPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "7"}
		requests := NewRequests[struct{}](transport)
		for range 2 {
			done := make(chan []composer.ContextAttachment, 1)
			go func() {
				values, err := requests.CurrentAttachments(t.Context(), "root")
				if err != nil {
					t.Error(err)
				}
				done <- values
			}()
			(<-requests.Inspections()).Complete("root", control.SessionDetails{})
			call := <-requests.Calls()
			if call.method != "thread/read" {
				t.Fatal(call.method)
			}
			requests.Dispatch(call)
			response := jsontext.Value(`{"thread":{"id":"root","turns":[{"items":[{"id":"reset","type":"contextCompaction","status":"completed"},{"id":"later","type":"userMessage","content":[{"type":"image","url":"https://example.test/later.png"}]}]}]}}`)
			if !requests.CompleteCall(appserver.Message{ID: jsontext.Value(`7`), Result: response}) {
				t.Fatal("reply not consumed")
			}
			if values := <-done; len(values) != 1 || len(values[0].Images) != 1 {
				t.Fatalf("post-boundary attachment missing: %+v", values)
			}
		}
		if len(transport.sends) != 2 {
			t.Fatalf("current prefix was cached: %d", len(transport.sends))
		}
	})
}
