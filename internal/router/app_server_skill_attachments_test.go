package router

import (
	"bytes"
	json "encoding/json/v2"
	"strconv"
	"strings"
	"testing"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func skillAttachmentCatalog(t *testing.T, u *appServerUI, w *appServerTestInput) {
	t.Helper()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "$rev")
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "skills/list" || len(requests[0].Params.Cwds) != 1 || requests[0].Params.Cwds[0] != "/work" {
		t.Fatalf("skill catalog scope: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":"/other","skills":[{"name":"review","path":"/other/review/SKILL.md","enabled":true}]},{"cwd":"/work","skills":[{"name":"review","path":"/work/review/SKILL.md","enabled":true},{"name":"disabled","path":"/work/disabled/SKILL.md","enabled":false},{"name":"duplicate","path":"/work/a/SKILL.md","enabled":true},{"name":"duplicate","path":"/work/b/SKILL.md","enabled":true}]}]}`)
}

func TestSkillAttachmentTypedWhitespaceAndLiteralExceptions(t *testing.T) {
	u, w := newAppServerTestUI()
	skillAttachmentCatalog(t, u, w)
	appServerTestKeys(t, u, "iew ")
	if u.draft != "$review " || len(u.skills) != 1 || u.skills[0].path != "/work/review/SKILL.md" {
		t.Fatalf("typed completion did not bind workspace skill: %q %+v", u.draft, u.skills)
	}
	appServerTestKeys(t, u, "\x1a")
	if len(u.skills) != 0 {
		t.Fatalf("undo retained binding: %+v", u.skills)
	}
	appServerTestKeys(t, u, "\x19")
	if len(u.skills) != 1 {
		t.Fatalf("redo lost binding: %+v", u.skills)
	}
	for _, draft := range []string{"$disabled ", "$duplicate ", "$missing ", "$HOME "} {
		d := composerDraft{text: draft}
		u.bindSkills(&d, true)
		if len(d.skills) != 0 {
			t.Errorf("literal %q acquired binding: %+v", draft, d.skills)
		}
	}
	for _, draft := range []string{"! echo $review ", "!$review "} {
		d := composerDraft{text: draft}
		u.bindSkills(&d, true)
		if len(d.skills) != 0 {
			t.Errorf("shell input %q acquired binding: %+v", draft, d.skills)
		}
	}
}

func TestSkillAttachmentPastedBeforeCatalogResponse(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~")
	if u.picker.open || len(u.skills) != 0 {
		t.Fatalf("paste opened picker or bound before catalog: %+v %+v", u.picker, u.skills)
	}
	appServerTestKeys(t, u, "\r")
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("submission did not request skill catalog: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":"/work","skills":[{"name":"review","path":"/work/review/SKILL.md","enabled":true}]}]}`)
	requests = pickerRequests(t, w)
	if len(requests) != 2 || requests[1].Method != "turn/start" {
		t.Fatalf("submission did not continue after catalog: %+v", requests)
	}
	var sent struct {
		Params struct {
			Input []struct {
				Type string `json:"type"`
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"input"`
		} `json:"params"`
	}
	lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte{'\n'})
	if err := json.Unmarshal(lines[len(lines)-1], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Params.Input) != 2 || sent.Params.Input[1].Type != "skill" || sent.Params.Input[1].Name != "review" || sent.Params.Input[1].Path != "/work/review/SKILL.md" {
		t.Fatalf("pasted skill not sent structurally: %+v", sent.Params.Input)
	}
	appServerTestMessage(t, u, `{"id":2,"error":{"code":-1,"message":"rejected"}}`)
	if u.draft != "use $review please" || len(u.skills) != 1 || u.skills[0].path != "/work/review/SKILL.md" {
		t.Fatalf("rejected submission lost binding: %q %+v", u.draft, u.skills)
	}
}

func TestSkillAttachmentTranscriptAndHistoryMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		wantStart     int
	}{
		{"metadata only", `[{"type":"text","text":"use $review please"},{"type":"skill","name":"review","path":"/work/review/SKILL.md"}]`, 4},
		{"selected second occurrence", `[{"type":"text","text":"$review then $review","textElements":[{"byteRange":{"start":13,"end":20},"placeholder":"$review"}]},{"type":"skill","name":"review","path":"/work/review/SKILL.md"}]`, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, spans, ok := appServerUserText([]byte(tc.content))
			if !ok || len(spans) != 1 || spans[0].Kind != activityui.SkillToken || spans[0].Start != tc.wantStart || text[spans[0].Start:spans[0].End] != "$review" {
				t.Fatalf("skill display: %q %+v, ok=%v", text, spans, ok)
			}
			u, _ := newAppServerTestUI()
			u.session.start("main", "/work")
			u.agents = newLiveActivityView()
			var turns []appServerHistoryTurn
			payload := `[{"id":"turn","status":"completed","items":[{"type":"userMessage","id":"message","content":` + tc.content + `}]}]`
			if err := json.Unmarshal([]byte(payload), &turns); err != nil {
				t.Fatal(err)
			}
			u.restoreHistory(turns)
			if len(u.view.entries) == 0 || u.view.entries[0].native == nil || len(u.view.entries[0].native.spans) != 1 || u.view.entries[0].native.spans[0].Start != tc.wantStart {
				t.Fatalf("restored transcript did not preserve selected skill: %+v", u.view.entries)
			}
			if len(u.inputHistory) != 1 || len(u.inputHistory[0].skills) != 1 || u.inputHistory[0].skills[0].start != tc.wantStart {
				t.Fatalf("history binding disagrees with transcript: %+v", u.inputHistory)
			}
			if !strings.Contains(u.view.entries[0].Text, "$review") {
				t.Fatalf("restored transcript lost skill text: %+v", u.view.entries[0])
			}
		})
	}
}

func TestSkillAttachmentLookupFailureStillSendsText(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~\r")
	requests := pickerRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("unexpected pending input: %+v", requests)
	}
	if len(u.unsent) != 1 || len(u.queued) != 0 {
		t.Fatal("skill lookup reordered a steer into the queued stack")
	}
	appServerTestMessage(t, u, `{"id":`+strconv.Itoa(requests[0].ID)+`,"error":{"code":-1,"message":"catalog unavailable"}}`)
	requests = pickerRequests(t, wire)
	if len(requests) != 2 || requests[1].Method != "turn/start" || len(u.submission.skills) != 0 || u.submission.text != "use $review please" {
		t.Fatalf("lookup failure blocked ordinary delivery: %+v %+v", requests, u.submission)
	}
	if !u.noticeAlert || !strings.Contains(u.notice, "Skill attachment unavailable") {
		t.Fatalf("lookup failure was silent: %q", u.notice)
	}
}

func TestSkillAttachmentFailureIndependentOfForegroundPicker(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~\r@foo")
	requests := pickerRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("unexpected pending requests: %+v", requests)
	}
	appServerTestMessage(t, u, `{"id":`+strconv.Itoa(requests[0].ID)+`,"error":{"code":-1,"message":"catalog unavailable"}}`)
	requests = pickerRequests(t, wire)
	fileID, starts := 0, 0
	for _, request := range requests {
		if request.Method == "fuzzyFileSearch" {
			fileID = request.ID
		}
		if request.Method == "turn/start" {
			starts++
		}
	}
	if starts != 1 || fileID == 0 || u.submission.text != "use $review please" || len(u.submission.skills) != 0 {
		t.Fatalf("foreground picker stranded fallback: %+v %+v", requests, u.submission)
	}
	if !u.noticeAlert || !strings.Contains(u.notice, "catalog unavailable") {
		t.Fatalf("foreground picker hid catalog failure: %q", u.notice)
	}
	pickerReply(t, u, fileID, `{"files":[{"root":"/work","path":"foo.go","match_type":"file"}]}`)
	if u.draft != "@foo" || !u.picker.open || len(u.picker.choices) != 1 {
		t.Fatalf("background failure broke foreground picker: %q %+v", u.draft, u.picker)
	}
}

func TestSkillAttachmentQueuedLookupIndependentOfForegroundPicker(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "/work"
	u.queued = []composerDraft{{text: "use $review please"}}
	appServerTestKeys(t, u, "@foo")
	if err := u.flushInput(); err != nil {
		t.Fatal(err)
	}
	requests := pickerRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "fuzzyFileSearch" {
		t.Fatalf("unexpected file search: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"files":[{"root":"/work","path":"foo.go","match_type":"file"}]}`)
	requests = pickerRequests(t, wire)
	// The first file lookup started at @f; acknowledge its coalesced @foo search.
	if len(requests) == 2 && requests[1].Method == "fuzzyFileSearch" {
		pickerReply(t, u, requests[1].ID, `{"files":[{"root":"/work","path":"foo.go","match_type":"file"}]}`)
		requests = pickerRequests(t, wire)
	}
	catalog := requests[len(requests)-1]
	if catalog.Method != "skills/list" {
		t.Fatalf("active file picker prevented background skill lookup: %+v", requests)
	}
	pickerReply(t, u, catalog.ID, `{"data":[{"cwd":"/work","skills":[{"name":"review","path":"/work/review/SKILL.md","enabled":true}]}]}`)
	if u.submission.text != "use $review please" || len(u.submission.skills) != 1 || u.draft != "@foo" {
		t.Fatalf("queued skill lost catalog or foreground draft: %+v %q", u.submission, u.draft)
	}
}

func TestSkillAttachmentPartialCatalogFailureKeepsAvailableSkillsVisible(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "\x1b[200~use $review and $broken please\x1b[201~\r")
	requests := pickerRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("unexpected catalog request: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":"/work","skills":[{"name":"review","path":"/work/review/SKILL.md","enabled":true}],"errors":[{"message":"broken skill manifest"}]}]}`)
	if u.submission.text != "use $review and $broken please" || len(u.submission.skills) != 1 || u.submission.skills[0].name != "review" {
		t.Fatalf("partial catalog failure lost text or available attachment: %+v", u.submission)
	}
	if !u.noticeAlert || !strings.Contains(u.notice, "Skill catalog incomplete") || !strings.Contains(u.notice, "broken skill manifest") {
		t.Fatalf("partial catalog failure was hidden: %q", u.notice)
	}
}
