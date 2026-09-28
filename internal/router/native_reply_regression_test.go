package router

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeActivityKeepsAttachedRootFamilyIsolated(t *testing.T) {
	a := newSubagentActivity()
	a.attachNativePane("main")
	a.observe("child", "main", "/root/worker", true)
	a.observe("grandchild", "child", "/root/worker/helper", true)
	a.observe("other", "", "/root", false)
	for _, thread := range []string{"main", "child", "grandchild", "other"} {
		a.collect(thread, "reply-"+thread, "reply", "Native reply from "+thread)
	}
	entries := a.takeNativeActivity("main")
	if len(entries) != 3 {
		t.Fatalf("attached root family = %+v", entries)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Text, "other") {
			t.Fatalf("borrowed unrelated root: %+v", entries)
		}
	}
	a.releasePane()
	a.attachNativePane("other")
	entries = a.takeNativeActivity("other")
	if len(entries) != 1 || entries[0].Text != "Native reply from other" {
		t.Fatalf("other root lost its activity: %+v", entries)
	}
}

func TestNativeChildAnswerPromotionRequiresMatchingSuccessfulTurn(t *testing.T) {
	for _, tc := range []struct {
		name, status, completionTurn string
		wantFinal                    bool
	}{
		{"successful", "completed", "child-turn", true},
		{"failed", "failed", "child-turn", false},
		{"interrupted", "interrupted", "child-turn", false},
		{"different turn", "completed", "other-turn", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "child-turn"}})
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": map[string]any{"id": "note", "type": "agentMessage", "text": "An interim note."}})
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": tc.completionTurn, "status": tc.status}})
			finals := 0
			for _, entry := range u.view.entries {
				if entry.Agent == "/root/worker" && entry.Kind == "final" {
					finals++
				}
			}
			if (finals == 1) != tc.wantFinal {
				t.Fatalf("final promotion for status %q and turn %q: %d", tc.status, tc.completionTurn, finals)
			}
		})
	}
}

func TestNativeFailedCollaborationDoesNotClaimDelivery(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "send", "type": "collabAgentToolCall", "status": "failed", "tool": "sendMessage",
		"senderThreadId": "main", "receiverThreadIds": []string{"child"}, "prompt": "Undelivered message.",
	}})
	for _, entry := range u.view.entries {
		if entry.Kind == "reply" || strings.Contains(entry.Text, "Message sent") {
			t.Fatalf("failed send claimed successful delivery: %+v", entry)
		}
	}
}

func TestNativeMainAnswerBindsOwnTurnPromptAcrossUpdates(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	user := func(id, turn, message string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": turn,
			"item": map[string]any{"id": id, "type": "userMessage", "content": []map[string]string{{"type": "text", "text": message}}}})
	}
	user("old", "old-turn", "Earlier question")
	user("current", "current-turn", "Current question")
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "current-turn", "itemId": "answer", "delta": "First "})
	user("later", "later-turn", "Later question")
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "current-turn", "itemId": "answer", "delta": "part"})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "current-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "First part"}})
	var current uint64
	for _, entry := range u.view.entries {
		if entry.native != nil && entry.native.item == "current" {
			current = entry.Seq
		}
	}
	if current == 0 {
		t.Fatal("current user prompt missing")
	}
	answerCount := 0
	for _, entry := range u.view.entries {
		if entry.native != nil && entry.native.item == "answer" {
			answerCount++
			if entry.native.question != current || entry.Text != "First part" {
				t.Fatalf("answer lost its original same-turn prompt: %+v", entry)
			}
		}
	}
	if answerCount != 1 {
		t.Fatalf("delta/completion did not share one answer identity: %d", answerCount)
	}
	u.view.conversation = true
	feed := u.view.renderFeed(90, 40)
	if !strings.Contains(ansi.Strip(strings.Join(feed.lines, "\n")), "↩ re: your message") {
		t.Fatal("answer has no visible reply link")
	}
	found := false
	for _, target := range feed.questions {
		found = found || target == current
	}
	if !found {
		t.Fatal("answer link did not target its own turn prompt")
	}
}

func TestNativeDirectedMessagesDoNotInjectLegacyEnvelopes(t *testing.T) {
	p := newManagedMekugiProxy(t)
	prepareActivityTest(t, p, "root", "r", "", "/root", nil)
	p.activity.attachNativePane("r")
	envelope := map[string]any{"type": "agent_message", "id": "parent-message", "author": "/root", "recipient": "/root/reviewer", "content": []any{map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root/reviewer\nSender: /root\nPayload:\nPlease finish."}}}
	child, request := prepareActivityTest(t, p, "child", "reviewer", "r", "/root/reviewer", []any{envelope})
	if !strings.Contains(string(request.fields["input"]), "Please finish.") {
		t.Fatal("native display changed model input")
	}
	output, err := child.TransformJSON([]byte(`{"status":"completed","output":[]}`))
	if err != nil || strings.Contains(string(output), "Message received") {
		t.Fatalf("legacy envelope leaked into child: %s, %v", output, err)
	}
}

func TestNativeDirectedMessageDisplayBudget(t *testing.T) {
	for _, size := range []int{17 << 10, 70 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := newManagedMekugiProxy(t)
			prepareActivityTest(t, p, "root", "r", "", "/root", nil)
			p.activity.attachNativePane("r")
			prepareActivityTest(t, p, "child", "child", "r", "/root/worker", nil)
			text := strings.Repeat("界", size/3)
			envelope := map[string]any{"type": "agent_message", "id": "large-message", "author": "/root/worker", "recipient": "/root", "content": []any{map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\n" + text}}}
			transform, request := prepareActivityTest(t, p, "root-next", "r", "", "/root", []any{envelope})
			defer transform.Close()
			if !strings.Contains(string(request.fields["input"]), text) {
				t.Fatal("display limit altered model input")
			}
			var reply activityPaneEntry
			for _, entry := range p.activity.takeNativeActivity("r") {
				if entry.Kind == "reply" {
					reply = entry
				}
			}
			if reply.Text == "" || !utf8.ValidString(reply.Text) {
				t.Fatal("native body was dropped or corrupted")
			}
			if size < maxNativeActivityMessageBytes {
				if !strings.Contains(reply.Text, text) {
					t.Fatal("inline publication limit truncated native message")
				}
			} else if len(reply.Text) > maxNativeActivityMessageBytes || !strings.Contains(reply.Text, "message clipped at the native Activity") {
				t.Fatal("native display bound is unmarked or exceeded")
			}
		})
	}
}
