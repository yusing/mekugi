package router

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerPublicSummaryNotifications(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		for _, delta := range []string{"**Checking " + thread + "**", "\n\nPublic " + thread + " summary."} {
			appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "same-id", "delta": delta})
		}
		appServerTestNotify(t, u, "item/reasoning/textDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "same-id", "delta": "PRIVATE"})
	}
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	child := ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	if !strings.Contains(main, "Public main summary.") || strings.Contains(main, "Public child") || !strings.Contains(child, "Public child summary.") || strings.Contains(child, "Public main") {
		t.Fatalf("summary routing: main=%q, child=%q", main, child)
	}
	for _, thread := range []string{"main", "child"} {
		// Real reasoning content is a string array, unlike user input blocks.
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{
			"id": "same-id", "type": "reasoning", "summary": []string{"**Complete**\n\nFinal public " + thread + "."}, "content": []string{"PRIVATE"}}})
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
	}
	u.agents.only, u.agents.selected = true, "/root/worker"
	main = ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	child = ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	if strings.Count(main, "Final public main.") != 1 || strings.Count(child, "Final public child.") != 1 || strings.Contains(main+child, "PRIVATE") || strings.Contains(main+child, "Public main summary") {
		t.Fatalf("completed summaries: main=%q, child=%q", main, child)
	}
	// A reused item ID in a new turn must not replace earlier summaries.
	appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "next", "itemId": "same-id", "delta": "Next public summary."})
	main = ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	if !strings.Contains(main, "Final public main.") || !strings.Contains(main, "Next public summary.") {
		t.Fatalf("turn identity lost: %q", main)
	}
}

func TestAppServerRestorePublicSummaries(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	var turns []appServerHistoryTurn
	if err := json.Unmarshal([]byte(`[{"id":"t","status":"completed","items":[{"id":"r","type":"reasoning","summary":["Public restored summary."],"content":["PRIVATE"]}]}]`), &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
	u.restoreActivityThread(appServerThreadInfo{ID: "child", Turns: turns})
	u.agents.only, u.agents.selected = true, "/root/worker"
	for _, view := range []*liveActivityView{u.view, u.agents} {
		got := ansi.Strip(strings.Join(view.renderFeed(90, 40).lines, "\n"))
		if strings.Count(got, "Public restored summary.") != 1 || strings.Contains(got, "PRIVATE") {
			t.Fatalf("restored summary: %q", got)
		}
	}
	// Raw-only items are decodable but produce no visible entry.
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":"raw","type":"reasoning","summary":[],"content":["PRIVATE"]}}}`)
	if len(u.view.entries) != 1 {
		t.Fatalf("raw reasoning reached transcript: %+v", u.view.entries)
	}
}
