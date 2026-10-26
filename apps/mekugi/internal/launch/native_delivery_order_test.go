package launch

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeMessageVisibleBeforeWaitContinuation(t *testing.T) {
	u, threads, at := logicalWaitUI(t, false, nil)
	service := threads.shared
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	body := "Ready to continue.\nPayload:\nKeep these exact authored bytes.\n"
	caller := control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "message", Item: "exec-message"}
	action, err := service.PrepareAction(t.Context(), caller, "/root", "message", body)
	check(err)
	_, err = service.ClaimEffect(t.Context(), "child", action.ID)
	check(err)
	// Start at the receiving event loop's confirmed-publication boundary.
	// Transport acknowledgement order needs separate host-runtime evidence.
	publishNativeDelivery(&u.attached, control.NativeInput{Thread: "main", ID: action.ID, Sender: "/root/worker", Recipient: "/root", Kind: "MESSAGE", Message: body}, *at)
	view := u.testActivity(u.shell.Transcript)
	view.Conversation = true
	if text := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n")); !strings.Contains(text, "Ready to continue.") {
		t.Fatalf("confirmed message waited for a later event to become visible:\n%s", text)
	}
	check(service.EffectAcknowledged(t.Context(), "child", action.ID, control.Receipt{}))
	wake, err := service.PrepareWake(t.Context(), "main", *at)
	check(err)
	if wake == nil {
		t.Fatal("confirmed message did not wake the selected wait")
	}
	_, err = service.ClaimEffect(t.Context(), "main", wake.ID)
	check(err)
	check(service.EffectAcknowledged(t.Context(), "main", wake.ID, control.Receipt{Turn: "continued"}))
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
	for _, title := range []string{"First subsequent reasoning", "Second subsequent reasoning"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "continued", "item": binding.ThreadItem{ID: title, Type: "reasoning", Summary: []string{title}}})
	}
	rollCommandOutput(u)
	text := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n"))
	message, finished, reasoning := strings.Index(text, "Ready to continue."), strings.Index(text, "Finished waiting"), strings.Index(text, "First subsequent reasoning")
	if message < 0 || finished <= message || reasoning <= finished || strings.Index(text, "Second subsequent reasoning") <= reasoning || strings.Count(text, "Ready to continue.") != 1 {
		t.Fatalf("delivery was delayed behind its continuation:\n%s", text)
	}
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Kind == "reply" && entry.Source.Text == body {
			return
		}
	}
	t.Fatal("delivery lost its exact authored message")
}
