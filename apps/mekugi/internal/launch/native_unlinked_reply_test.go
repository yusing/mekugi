package launch

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
)

func unlinkedReplyUI(t *testing.T) (*appServerUI, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/replies/live-section.txt")
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	body := string(raw)
	entry := sessionpresentation.NativeDeliveryEntry(sessioncontrol.NativeDelivery{Thread: "main", Sender: "/root/live-section", Recipient: "/root", Kind: "MESSAGE", Message: body}, "retained-message", time.Date(2026, 10, 28, 23, 27, 0, 0, time.Local), false)
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
	semantic := nativeSessionView{attached: u.attached, thread: u.Thread}
	if refusal := semantic.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: u.attached.source.Entries[0].Source.Seq}); refusal != "" {
		t.Fatal(refusal)
	}
	renderer := nativeDetailTestRenderer(t, u, &semantic)
	renderer.terminal.Dialog.Layout(160)
	if renderer.terminal.Dialog.Document.Text != body {
		t.Fatal("original full-copy body changed")
	}
	var details []string
	for _, line := range renderer.terminal.Dialog.Document.Lines {
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

func TestNativeSideQuestionReplyDisclosure(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := sideQuestionActivityItem(t, "completed")
	item.Result = []byte(`{"content":[{"type":"text","text":"{\"thread_id\":\"worker-thread\",\"task_name\":\"/root/worker\",\"answer\":\"## Summary\\nOutcome.\\n\\n## Details\\nEvidence.\"}"}]}`)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	terminal.Width, terminal.Height = 100, 30
	c := nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil)}})
	if _, err := c.renderer.render(t.Context(), io.Discard, time.Now(), "test"); err != nil {
		t.Fatal(err)
	}
	view := terminal.Transcript
	for row, line := range terminal.Frame.Rows {
		if !strings.Contains(ansi.Strip(line), "Open reply") {
			continue
		}
		x, y := terminal.Layout.Main.X+view.FeedLeft+3, row+1
		c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dM", x, y)})
		c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dm", x, y)})
		if len(c.calls) != 1 || c.calls[0].operation.Details == nil {
			t.Fatalf("reply click did not request details: calls=%d notice=%s", len(c.calls), c.notice)
		}
		request := *c.calls[0].operation.Details
		if request.Operation != 3 {
			t.Fatalf("answer identity lost: %+v", request)
		}
		semantic := nativeSessionView{attached: u.attached, thread: u.Thread}
		if refusal := semantic.openDetails(request); refusal != "" {
			t.Fatal(refusal)
		}
		c.renderer.updateNativeDetailTest(&semantic)
		c.renderer.terminal.Dialog.Layout(100)
		if got := c.renderer.terminal.Dialog.Document.Text; got != "## Summary\nOutcome.\n\n## Details\nEvidence." {
			t.Fatalf("wrong native reply: %q", got)
		}
		request.Operation = 99
		if semantic.openDetails(request) == "" {
			t.Fatal("unknown answer operation accepted")
		}
		return
	}
	t.Fatal("no native Open reply link")
}
