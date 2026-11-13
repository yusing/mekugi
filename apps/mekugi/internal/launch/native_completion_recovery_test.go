package launch

import (
	"bytes"
	"strings"
	"testing"
	"testing/synctest"

	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestNativeCompletionRequestRestoresOutcome(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		glyph   string
		label   string
	}{
		{"completed", "✓", "finished"},
		{"failed", "✗", "failed"},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				live := newAppServerSessionTestUI(t, t.TempDir())
				input := sessioncontrol.NativeDelivery{Thread: "main", ID: "amsg_child_completion", Sender: "/root/worker", Recipient: "/root", Kind: "FINAL_ANSWER", Outcome: tc.outcome, Message: "Status: interrupted\nStatus: failed\nOutcome: failed\nPayload:\nVerbatim final evidence.\n"}
				record := nativeTrafficFixture(t, live, input.ID, input.Sender, input.Recipient, input.Kind, input.Message, input.Outcome, time.Now())

				restored := newAppServerSessionTestUI(t, live.attached.cwd)
				root := binding.ThreadInfo{ID: "main", Cwd: live.attached.cwd,
					Path: writeTestRollout(t, "main", record)}
				if err := restored.restoreContent(root); err != nil {
					t.Fatal(err)
				}
				if entries := restored.attached.source.Entries; len(entries) != 1 {
					t.Fatalf("restored completion entries = %+v", entries)
				}
				entry := restored.attached.source.Entries[0]
				if source := entry.Source; source.Kind != "final" || source.Agent != input.Sender || source.Text != input.Message ||
					source.Native == nil || source.Native.Thread != input.Thread || source.Native.Item != "native:delivery:"+input.ID || source.Native.Status != string(tc.outcome) {
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
