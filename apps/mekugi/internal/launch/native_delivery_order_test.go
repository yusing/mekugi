package launch

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestNativeTranscriptShowsOnlyLocalAcceptedMailLiveAndRetained(t *testing.T) {
	u, _, at := nativeRunningUI(t)
	outgoing := nativeTrafficFixture(t, u, "sent", "/root", "/root/worker", "MESSAGE", "authored outbox\n", "", *at)
	// The outbound observation is an event, not a model response item.
	outgoing = map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "event_msg",
		"payload": map[string]any{"type": "raw_response_item", "item": outgoing["payload"]}}
	incoming := nativeTrafficFixture(t, u, "received", "/root/worker", "/root", "MESSAGE", "authored inbox\n", "", at.Add(time.Second))
	foreign := nativeTrafficFixture(t, u, "foreign", "/root/worker", "/root/other", "MESSAGE", "not for Main", "", at.Add(2*time.Second))
	final := nativeTrafficFixture(t, u, "final", "/root/worker", "/root", "FINAL_ANSWER", "authored final\n", "", at.Add(3*time.Second))
	item := final["payload"].(map[string]any)
	item["content"] = append(item["content"].([]map[string]string), map[string]string{"type": "input_text", "text": "internal capture"})
	appServerTestNotify(t, u, "rawResponseItem/completed", map[string]any{"threadId": "main", "item": item})

	assertMail := func(view *appServerUI) {
		t.Helper()
		type route struct{ from, to, body string }
		var got []route
		for _, value := range view.attached.source.Entries {
			if message := value.Source.Message; message != nil {
				got = append(got, route{message.From, message.To, message.Text})
			}
		}
		want := []route{{"Main", "/root/worker", "authored outbox\n"}, {"/root/worker", "Main", "authored inbox\n"}, {"/root/worker", "Main", "authored final\n"}}
		if !slices.Equal(got, want) {
			t.Fatalf("transcript mail = %#v, want %#v", got, want)
		}
	}
	assertMail(u)
	restored, _, _ := nativeRunningUI(t)
	path := writeTestRollout(t, "main", outgoing, incoming, foreign, final, final)
	if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: restored.attached.cwd, Path: path}); err != nil {
		t.Fatal(err)
	}
	assertMail(restored)
}

func TestNativeMessageVisibleBeforeWaitContinuation(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	body := "Ready to continue.\nPayload:\nKeep these exact authored bytes.\n"
	nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, nil, []appserver.CoordinationWaitMessage{{DeliveryID: "message", SenderTaskName: new("/root/worker"), Recipient: "/root", Body: body}})
	view := u.testActivity(u.shell.Transcript)
	view.Conversation = true
	if text := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n")); !strings.Contains(text, "Ready to continue.") {
		t.Fatalf("confirmed message waited for a later event to become visible:\n%s", text)
	}
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
