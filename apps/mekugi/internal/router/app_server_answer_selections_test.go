package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

func TestQuestionSelectedTextUsesComposerAttachments(t *testing.T) {
	for _, mode := range []string{"async", "sync note", "async option"} {
		t.Run(mode, func(t *testing.T) {
			sync := mode == "sync note"
			u, wire := newAppServerTestUI()
			u.Turn = "turn"
			if sync {
				questionTestSync(t, u, "selected", false)
			} else {
				questionTestAsync(t, u, "selected", "Which scope?")
			}
			questionTestPaint(t, u, 70)
			if sync {
				appServerTestKeys(t, u, "1\t")
			}
			shell := selectionTestUI("quoted")
			shell.main = u
			selectionTestDrag(t, shell, 0, 0, 5, 0)
			shell.selectionAction('r')
			if u.draft != "[Selected message] " || len(u.selections) != 1 {
				t.Fatalf("reference did not reuse composer: %q", u.draft)
			}
			appServerTestKeys(t, u, "**This?**")
			if mode == "async option" {
				appServerTestKeys(t, u, "\x1b[A\r")
				replies := questionReplies(u.inputPayloads[u.Submission().ID].text)
				if len(replies) != 1 || replies[0].Answer != "Internal" || len(u.inputPayloads[u.Submission().ID].input()) != 1 {
					t.Fatalf("option sent unsubmitted free text: %+v", u.inputPayloads[u.Submission().ID].input())
				}
				return
			}
			appServerTestKeys(t, u, "\r")
			if sync {
				if !strings.Contains(wire.String(), `"Narrow","user_note: [Selected message] **This?**"`) || strings.Contains(wire.String(), "quoted") {
					t.Fatalf("sync answer changed: %s", wire.String())
				}
				questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "selected"})
			} else {
				replies := questionReplies(u.inputPayloads[u.Submission().ID].text)
				if len(replies) != 1 || replies[0].Answer != "[Selected message] **This?**" {
					t.Fatalf("wrong async answer: %+v", replies)
				}
			}
			input := u.inputPayloads[u.Submission().ID].input()
			frames, ok := attachments.Decode(input[len(input)-1]["text"].(string))
			if !ok || len(frames) != 1 || !strings.HasSuffix(frames[0], "\nquoted") || !strings.Contains(frames[0], `"[Selected message]"`) {
				t.Fatalf("selection lost in transport: %+v", input)
			}
		})
	}
}
