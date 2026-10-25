package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeDeliveredTrafficSurvivesResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		live := newAppServerSessionTestUI(t, t.TempDir())
		wire := live.Host.Input.(*appServerTestInput)
		var records []map[string]any
		for i, kind := range []string{"MESSAGE", "FINAL_ANSWER"} {
			input := control.NativeInput{Thread: "main", ID: kind, Sender: "/root/worker", Recipient: "/root", Kind: kind, Message: kind + "\nPayload:\nunaltered body\n"}
			wire.Reset()
			result := make(chan error, 1)
			go func() { _, err := live.Requests.Input(t.Context(), input); result <- err }()
			synctest.Wait()
			live.Requests.Dispatch(<-live.Calls())
			var sent struct {
				ID     jsontext.Value `json:"id"`
				Params struct {
					Items []map[string]any `json:"items"`
				} `json:"params"`
			}
			if err := json.Unmarshal(wire.Bytes(), &sent); err != nil {
				t.Fatal(err)
			}
			request, _ := live.Requests.Lookup(string(sent.ID))
			if !live.Requests.CompleteCall(appserver.Message{ID: sent.ID, Result: []byte(`{}`)}) {
				t.Fatal("delivery acknowledgement missing")
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			publishNativeDelivery(&live.attached, *request.NativeDelivery(), time.Now())
			// Both deliveries arrive while idle, one before any host turn.
			record := rolloutRecord(i*3+1, "response_item", sent.Params.Items[0])
			records = append(records, record, record) // Repeated retained identity must coalesce.
			if i == 0 {
				records = append(records,
					rolloutRecord(2, "event_msg", map[string]any{"type": "task_started", "turn_id": "saved"}),
					rolloutCompleted(3, "saved", "answer"),
					rolloutRecord(3, "event_msg", map[string]any{"type": "task_complete", "turn_id": "saved"}))
			}
			// An ordinary assistant's printed envelope supplies no delivery provenance.
			lookalike := map[string]any{"type": "message", "id": control.MessageItemID("lookalike-" + kind), "role": "assistant", "phase": "commentary", "content": sent.Params.Items[0]["content"]}
			records = append(records, rolloutRecord(i*3+1, "response_item", lookalike))
		}
		for _, idle := range []bool{false, true} {
			restored := newAppServerSessionTestUI(t, live.attached.cwd)
			root := binding.ThreadInfo{ID: "main", Cwd: live.attached.cwd, Path: writeTestRollout(t, "main", records...)}
			want := []string{"MESSAGE\nPayload:\nunaltered body\n", "host answer", "FINAL_ANSWER\nPayload:\nunaltered body\n"}
			if idle {
				root.Path = writeTestRollout(t, "main", records[0], records[1])
				want = want[:1]
			} else {
				root.Turns = []binding.HistoryTurn{{ID: "saved", Status: "completed", Items: []binding.ThreadItem{{ID: "answer", Type: "agentMessage", Text: "host answer"}}}}
			}
			if err := restored.restoreContent(root); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, entry := range restored.attached.source.Entries {
				got = append(got, entry.Source.Text)
				if entry.Source.Text == "host answer" {
					continue
				}
				if entry.Source.Agent != "/root/worker" || entry.Source.Native == nil || entry.Source.Native.Thread != "main" {
					t.Fatalf("delivery lost attribution: %+v", entry.Source)
				}
				if entry.Source.Kind != "reply" && entry.Source.Kind != "final" {
					t.Fatalf("delivery lost message/result kind: %+v", entry.Source)
				}
				if entry.Source.Kind == "reply" && (entry.Source.Message == nil || entry.Source.Message.From != "/root/worker" || entry.Source.Message.To != "Main") {
					t.Fatalf("message lost receiver presentation: %+v", entry.Source)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("idle=%v: restored traffic = %q, want %q", idle, got, want)
			}
			if requests := restoreContentRequests(t, restored.Host.Input.(*appServerTestInput)); len(requests) != 0 {
				t.Fatalf("restoration dispatched host requests: %+v", requests)
			}
		}
	})
}
