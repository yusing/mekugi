package launch

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func unlinkedReplyUI(t *testing.T) (*appServerUI, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/replies/live-section.txt")
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	body := string(raw)
	entry := nativeDeliveryEntry(control.NativeInput{Thread: "main", Sender: "/root/live-section", Recipient: "/root", Kind: "MESSAGE", Message: body}, "retained-message", time.Date(2026, 10, 28, 23, 27, 0, 0, time.Local), false)
	// This is the captured live receiving-runtime state: complete parsed reply
	// content in Main, with no corresponding child-feed entry or Activity link.
	u.attached.source.ApplyMain(entry)
	return u, body
}

func TestNativeIncomingReplyWithoutActivityLink(t *testing.T) {
	u, body := unlinkedReplyUI(t)
	view := u.testActivity(u.shell.Transcript)
	rendered := ansi.Strip(strings.Join(view.testRenderConversation(160).Lines, "\n"))
	if strings.Contains(rendered, "Summary") || strings.Contains(rendered, "Details") || strings.Contains(rendered, "A bounded temporary") || !strings.Contains(rendered, "↩ Open reply") {
		t.Fatalf("captured incoming reply bypassed summary disclosure:\n%s", rendered)
	}
	view.Expansion = 1
	rendered = ansi.Strip(strings.Join(view.testRenderConversation(160).Lines, "\n"))
	if strings.Contains(rendered, "Summary") || strings.Contains(rendered, "Details") || !strings.Contains(rendered, "A bounded temporary") || strings.Count(rendered, "Confirmed the separate") != 1 {
		t.Fatalf("expanded reply repeats delimiters or content:\n%s", rendered)
	}
	semantic := nativeSessionView{attached: u.attached, shell: u.shell, thread: u.Thread}
	if refusal := semantic.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: u.attached.source.Entries[0].Source.Seq}); refusal != "" {
		t.Fatal(refusal)
	}
	semantic.shell.Dialog.Layout(160)
	if terminalTest(u).Dialog.Document.Text != body {
		t.Fatal("original full-copy body changed")
	}
	var details []string
	for _, line := range terminalTest(u).Dialog.Document.Lines {
		details = append(details, ansi.Strip(line.Text))
	}
	shown := strings.Join(details, "\n")
	if strings.Contains(shown, "Summary") || strings.Contains(shown, "Details") || strings.Contains(shown, "Confirmed the separate") || !strings.Contains(shown, "A bounded temporary") {
		t.Fatalf("detail view repeats summary or delimiters:\n%s", shown)
	}
}

func TestUISnapshotNativeUnlinkedReply(t *testing.T) {
	u, _ := unlinkedReplyUI(t)
	assertNativeUISnapshot(t, "native-unlinked-reply", u.testActivity(u.shell.Transcript).testRenderConversation(100).Lines)
}
