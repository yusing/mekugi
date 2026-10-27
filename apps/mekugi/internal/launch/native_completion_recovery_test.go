package launch

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeCompletionRequestRestoresOutcome(t *testing.T) {
	for _, tc := range []struct {
		outcome control.Status
		glyph   string
		label   string
	}{
		{control.Completed, "✓", "finished"},
		{control.Interrupted, "■", "interrupted"},
		{control.Failed, "✗", "failed"},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				live := newAppServerSessionTestUI(t, t.TempDir())
				wire := live.Host.Input.(*appServerTestInput)
				wire.Reset()
				input := control.NativeInput{
					Thread: "main", ID: "child:completion", Sender: "/root/worker", Recipient: "/root",
					Kind: "FINAL_ANSWER", Outcome: tc.outcome, SourceGeneration: 2, ParentTurn: "child-turn",
					Message: "Status: interrupted\nStatus: failed\nOutcome: failed\nPayload:\nVerbatim final evidence.\n",
				}
				done := make(chan error, 1)
				go func() { _, err := live.Requests.Input(t.Context(), input); done <- err }()
				synctest.Wait()
				live.Requests.Dispatch(<-live.Calls())
				var sent struct {
					ID     jsontext.Value `json:"id"`
					Method string         `json:"method"`
					Params struct {
						Thread string           `json:"threadId"`
						Items  []map[string]any `json:"items"`
					} `json:"params"`
				}
				if err := json.Unmarshal(wire.Bytes(), &sent); err != nil {
					t.Fatal(err)
				}
				if sent.Method != "thread/inject_items" || sent.Params.Thread != "main" || len(sent.Params.Items) != 1 {
					t.Fatalf("completion injection = %+v", sent)
				}
				if !live.Requests.CompleteCall(appserver.Message{ID: sent.ID, Result: jsontext.Value(`{}`)}) {
					t.Fatal("completion injection acknowledgement missing")
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}

				restored := newAppServerSessionTestUI(t, live.attached.cwd)
				root := binding.ThreadInfo{ID: "main", Cwd: live.attached.cwd,
					Path: writeTestRollout(t, "main", rolloutRecord(1, "response_item", sent.Params.Items[0]))}
				if err := restored.restoreContent(root); err != nil {
					t.Fatal(err)
				}
				if entries := restored.attached.source.Entries; len(entries) != 1 {
					t.Fatalf("restored completion entries = %+v", entries)
				}
				entry := restored.attached.source.Entries[0]
				if source := entry.Source; source.Kind != "final" || source.Agent != input.Sender || source.Text != input.Message ||
					source.Native == nil || source.Native.Thread != input.Thread || source.Native.Item != control.MessageItemID(input.ID) || source.Native.Status != string(tc.outcome) {
					t.Fatalf("restored completion lost payload, identity or outcome: %+v", source)
				}
				view := restored.testActivity(restored.shell.Transcript)
				rows := view.testRenderConversation(100).Lines
				var header string
				for _, row := range rows {
					if plain := ansi.Strip(row); strings.Contains(plain, tc.glyph) {
						header = plain
						break
					}
				}
				if !strings.Contains(header, tc.label) {
					t.Fatalf("restored terminal outcome header missing %q: %s", tc.label, ansi.Strip(strings.Join(rows, "\n")))
				}
				if err := restored.testPaint(&bytes.Buffer{}, 100, 28); err != nil {
					t.Fatal(err)
				}
				if !terminalTest(restored).openActivityReply(entry.Source.Seq) {
					t.Fatal("restored completion cannot open its Activity reply")
				}
				if err := restored.testPaint(&bytes.Buffer{}, 100, 28); err != nil {
					t.Fatal(err)
				}
				if terminalTest(restored).Dialog.Document.Text != input.Message {
					t.Fatal("restored reply disclosure lost the verbatim status-like payload")
				}
				if requests := restoreContentRequests(t, restored.Host.Input.(*appServerTestInput)); len(requests) != 0 {
					t.Fatalf("restoration dispatched host work: %+v", requests)
				}
			})
		})
	}
}
