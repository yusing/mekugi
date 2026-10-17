package router

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

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
			shell := selectionTestForMain(u, "quoted")
			selectionTestDrag(t, shell, 0, 0, 5, 0)
			shell.selectionAction('r')
			if u.Text != "[Selected message] " || len(u.Selections) != 1 {
				t.Fatalf("reference did not reuse composer: %q", u.Text)
			}
			appServerTestKeys(t, u, "**This?**")
			if mode == "async option" {
				appServerTestKeys(t, u, "\x1b[A\r")
				replies := binding.QuestionReplies(u.attached.conversation.Submitted[u.Submission().ID].Text)
				if len(replies) != 1 || replies[0].Answer != "Internal" || len(conversationbinding.Input(u.attached.conversation.Submitted[u.Submission().ID].Draft)) != 1 {
					t.Fatalf("option sent unsubmitted free text: %+v", conversationbinding.Input(u.attached.conversation.Submitted[u.Submission().ID].Draft))
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
				replies := binding.QuestionReplies(u.attached.conversation.Submitted[u.Submission().ID].Text)
				if len(replies) != 1 || replies[0].Answer != "[Selected message] **This?**" {
					t.Fatalf("wrong async answer: %+v", replies)
				}
			}
			input := conversationbinding.Input(u.attached.conversation.Submitted[u.Submission().ID].Draft)
			frames, ok := attachments.Decode(input[len(input)-1]["text"].(string))
			if !ok || len(frames) != 1 || !strings.HasSuffix(frames[0], "\nquoted") || !strings.Contains(frames[0], `"[Selected message]"`) {
				t.Fatalf("selection lost in transport: %+v", input)
			}
		})
	}
}

func TestQuestionSecretAndOrdinarySelectionsKeepDistinctLabels(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "turn"
	questionTestMessage(t, u, "mixed-selection", "item/tool/requestUserInput", map[string]any{
		"threadId": "main", "turnId": "turn", "itemId": "mixed-selection",
		"questions": []any{
			map[string]any{"id": "private", "question": "Private context?", "isSecret": true},
			map[string]any{"id": "ordinary", "question": "Ordinary context?"},
		},
	})
	for _, text := range []string{"private quote", "ordinary quote"} {
		questionTestPaint(t, u, 70)
		shell := selectionTestForMain(u, text)
		selectionTestDrag(t, shell, 0, 0, len(text)-1, 0)
		shell.selectionAction('r')
		appServerTestKeys(t, u, "\r")
	}
	var response struct {
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(wire.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	for id, label := range map[string]string{"private": "[Selected message]", "ordinary": "[Selected message 2]"} {
		answers := response.Result.Answers[id].Answers
		if len(answers) != 1 || answers[0] != "user_note: "+label+" " {
			t.Fatalf("question %s answer = %q, want distinct label %s", id, answers, label)
		}
	}
	call := u.questions.calls()[0]
	if call.Questions[0].Editor.Snapshot.Text != "" || len(call.Questions[0].Answer) != 0 || len(call.View().Questions[0].Answer) != 0 {
		t.Fatal("secret draft entered neutral model")
	}
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "mixed-selection"})
	input := conversationbinding.Input(u.attached.conversation.Submitted[u.Submission().ID].Draft)
	frames, ok := attachments.Decode(input[len(input)-1]["text"].(string))
	if !ok || len(frames) != 2 || !strings.Contains(frames[0], `"[Selected message]"`) || !strings.HasSuffix(frames[0], "\nprivate quote") || !strings.Contains(frames[1], `"[Selected message 2]"`) || !strings.HasSuffix(frames[1], "\nordinary quote") {
		t.Fatalf("companion selection frames = %q", frames)
	}
}
