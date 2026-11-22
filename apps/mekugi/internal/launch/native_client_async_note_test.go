package launch

import (
	"testing"
)

func TestNativeClientAsyncTabNotePayload(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	questionTestAsync(t, u, "batch", "First?", "Second?")
	c := nativeTestPromptClient(u)
	nativeTestPromptKey(t, c, "\t")
	editor, _ := c.selectedEditor()
	editor.InsertText("because this is safer")
	nativeTestPromptKey(t, c, "\r")
	q := c.prompts.questions.Active.Questions[0]
	if !q.Note || len(q.Answer) != 2 || q.Answer[1] != "because this is safer" {
		t.Fatalf("async note lost: %+v", q)
	}
	c.prompts.questions.Index = 0
	c.submitPrompt(false)
	if len(c.calls) != 1 {
		t.Fatalf("missing submission: %d", len(c.calls))
	}
	for _, call := range c.calls {
		if call.operation.Answers == nil || call.operation.Answers.Answers[0].Note != "because this is safer" {
			t.Fatal("note missing from native payload")
		}
	}
}
