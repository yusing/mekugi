package launch

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestNativeMessageVisibleBeforeWaitContinuation(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	body := "Ready to continue.\nPayload:\nKeep these exact authored bytes.\n"
	nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, nil, []appserver.CoordinationWaitMessage{{DeliveryID: "message", SenderTaskName: new("/root/worker"), Recipient: "/root", Body: body}})
	view := u.testActivity(u.shell.Transcript)
	view.Conversation = true
	if text := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n")); !strings.Contains(text, "Ready to continue.") {
		t.Fatalf("confirmed message waited for a later event to become visible:\n%s", text)
	}
	root := threads.native.agent("main")
	root.Wait = nil
	root.Work.Disposition = "running"
	nativeTreeFixture(t, threads)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
	for _, title := range []string{"First subsequent reasoning", "Second subsequent reasoning"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "continued", "item": binding.ThreadItem{ID: title, Type: "reasoning", Summary: []string{title}}})
	}
	rollCommandOutput(u)
	text := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n"))
	message, finished, reasoning := strings.Index(text, "Ready to continue."), strings.Index(text, "Finished waiting"), strings.Index(text, "Second subsequent reasoning")
	if message < 0 || finished <= message || reasoning <= finished || strings.Count(text, "Ready to continue.") != 1 {
		t.Fatalf("delivery was delayed behind its continuation:\n%s", text)
	}
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Kind == "reply" && entry.Source.Text == body {
			return
		}
	}
	t.Fatal("delivery lost its exact authored message")
}
