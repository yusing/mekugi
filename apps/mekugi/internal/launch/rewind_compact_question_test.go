package launch

import (
	"fmt"
	"github.com/yusing/mekugi/backends/codex/binding"
	"testing"
)

func TestRewindCompactOptionReplyIncludesLabelAndNote(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	text := binding.EncodeQuestionReplies([]binding.QuestionAnswer{{ID: "q1", Choice: 2, Note: "safer"}})
	rewindTestHistory(t, u, w, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","turns":[{"id":"ask","status":"completed","items":[{"id":"question","type":"agentMessage","delivery":"async","questions":[{"id":"q1","question":"Release?","options":[{"label":"Now"},{"label":"Staged"}]}]}]},{"id":"answer","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":%q}]}]}]}}}`, read.ID, text))
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Display != "Staged safer" {
		t.Fatalf("compact rewind answer unreadable: %+v", u.picker.Choices)
	}
}

func TestRewindResolvesOptionFromOlderPage(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","historyMode":"paginated"}}}`, read.ID))
	page := resumeTestOne(t, w, "thread/turns/list")
	text := binding.EncodeQuestionReplies([]binding.QuestionAnswer{{ID: "q1", Choice: 1, Note: "safer"}})
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"answer","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":%q}]}]}],"nextCursor":"older"}}`, page.ID, text))
	page = resumeTestOne(t, w, "thread/turns/list")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"ask","status":"completed","items":[{"id":"question","type":"agentMessage","questions":[{"id":"q1","question":"Release?","options":[{"label":"Staged"}]}]}]}]}}`, page.ID))
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Display != "Staged safer" {
		t.Fatalf("older page did not resolve reply: %+v", u.picker.Choices)
	}
}
