package router

import (
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func skillAttachmentCatalog(t *testing.T, u *appServerUI, w *appServerTestInput) {
	t.Helper()
	u.cwd = "/work"
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
	if u.Text != "$review " || len(u.Skills) != 1 || u.Skills[0].Path != "/work/review/SKILL.md" {
		t.Fatalf("typed completion did not bind workspace skill: %q %+v", u.Text, u.Skills)
	}
	appServerTestKeys(t, u, "\x1a")
	if len(u.Skills) != 0 {
		t.Fatalf("undo retained binding: %+v", u.Skills)
	}
	appServerTestKeys(t, u, "\x19")
	if len(u.Skills) != 1 {
		t.Fatalf("redo lost binding: %+v", u.Skills)
	}
	for _, draft := range []string{"$disabled ", "$duplicate ", "$missing ", "$HOME "} {
		d := conversationmodel.Input{Text: draft}
		u.bindSkills(&d, true)
		if len(d.Skills) != 0 {
			t.Errorf("literal %q acquired binding: %+v", draft, d.Skills)
		}
	}
	for _, draft := range []string{"! echo $review ", "!$review "} {
		d := conversationmodel.Input{Text: draft}
		u.bindSkills(&d, true)
		if len(d.Skills) != 0 {
			t.Errorf("shell input %q acquired binding: %+v", draft, d.Skills)
		}
	}
}

func TestSkillAttachmentPastedBeforeCatalogResponse(t *testing.T) {
	u, w := newAppServerTestUI()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "review", "SKILL.md")
	const instructions = "Review the attached source 世界.\n"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(instructions), 0600); err != nil {
		t.Fatal(err)
	}
	u.cwd = workspace
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~")
	if u.picker.Open || len(u.Skills) != 0 {
		t.Fatalf("paste opened picker or bound before catalog: %+v %+v", u.picker, u.Skills)
	}
	appServerTestKeys(t, u, "\r")
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("submission did not request skill catalog: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":`+strconv.Quote(workspace)+`,"skills":[{"name":"review","path":`+strconv.Quote(path)+`,"enabled":true}]}]}`)
	requests = pickerRequests(t, w)
	if len(requests) != 2 || requests[1].Method != "turn/start" {
		t.Fatalf("submission did not continue after catalog: %+v", requests)
	}
	var sent struct {
		Params struct {
			Input []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte{'\n'})
	if err := json.Unmarshal(lines[len(lines)-1], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Params.Input) != 2 || sent.Params.Input[1].Type != "text" {
		t.Fatalf("pasted skill contents not attached: %+v", sent.Params.Input)
	}
	frames, ok := attachments.Decode(sent.Params.Input[1].Text)
	if !ok || len(frames) != 1 || frames[0] != composer.SkillFrames("review", path, instructions)[0] {
		t.Fatalf("pasted skill lost its source path or contents: %+v", frames)
	}
	appServerTestMessage(t, u, `{"id":2,"error":{"code":-1,"message":"rejected"}}`)
	if u.Text != "use $review please" || len(u.Skills) != 1 || u.Skills[0].Path != path {
		t.Fatalf("rejected submission lost binding: %q %+v", u.Text, u.Skills)
	}
}

func TestSkillAttachmentTranscriptAndHistoryMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		wantStart     int
	}{
		{"metadata only", `[{"type":"text","text":"use $review please"},{"type":"skill","name":"review","path":"/work/review/SKILL.md"}]`, 4},
		{"selected second occurrence", `[{"type":"text","text":"$review then $review","text_elements":[{"byteRange":{"start":13,"end":20},"placeholder":"$review"}]},{"type":"skill","name":"review","path":"/work/review/SKILL.md"}]`, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, spans, ok := sessionpresentation.UserText([]byte(tc.content))
			if !ok || len(spans) != 1 || spans[0].Kind != activitypresentation.SkillToken || spans[0].Start != tc.wantStart || text[spans[0].Start:spans[0].End] != "$review" {
				t.Fatalf("skill display: %q %+v, ok=%v", text, spans, ok)
			}
			u, _ := newAppServerTestUI()
			u.startSession("main", "/work")

			var turns []binding.HistoryTurn
			payload := `[{"id":"turn","status":"completed","items":[{"type":"userMessage","id":"message","content":` + tc.content + `}]}]`
			if err := json.Unmarshal([]byte(payload), &turns); err != nil {
				t.Fatal(err)
			}
			u.restoreHistory(turns)
			if len(u.source.Entries) == 0 || u.source.Entries[0].Source.Native == nil || len(u.source.Entries[0].Source.Native.Spans) != 1 || u.source.Entries[0].Source.Native.Spans[0].Start != tc.wantStart {
				t.Fatalf("restored transcript did not preserve selected skill: %+v", u.source.Entries)
			}
			if len(u.InputHistory) != 1 || len(u.InputHistory[0].Skills) != 1 || u.InputHistory[0].Skills[0].Start != tc.wantStart {
				t.Fatalf("history binding disagrees with transcript: %+v", u.InputHistory)
			}
			if !strings.Contains(u.source.Entries[0].Source.Text, "$review") {
				t.Fatalf("restored transcript lost skill text: %+v", u.source.Entries[0])
			}
		})
	}
}

func TestSkillAttachmentLookupFailureStillSendsText(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.cwd = "/work"
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~\r")
	requests := pickerRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("unexpected pending input: %+v", requests)
	}
	if len(u.conversation().Unsent) != 1 || len(u.conversation().Queued) != 0 {
		t.Fatal("skill lookup reordered a steer into the queued stack")
	}
	appServerTestMessage(t, u, `{"id":`+strconv.Itoa(requests[0].ID)+`,"error":{"code":-1,"message":"catalog unavailable"}}`)
	requests = pickerRequests(t, wire)
	if len(requests) != 2 || requests[1].Method != "turn/start" || len(u.conversation().Submitted[u.Submission().ID].Skills) != 0 || u.conversation().Submitted[u.Submission().ID].Text != "use $review please" {
		t.Fatalf("lookup failure blocked ordinary delivery: %+v %+v", requests, u.conversation().Submitted[u.Submission().ID])
	}
	if !u.feedback.Alert || !strings.Contains(u.feedback.Text, "Skill attachment unavailable") {
		t.Fatalf("lookup failure was silent: %q", u.feedback.Text)
	}
}

func TestSkillAttachmentFailureIndependentOfForegroundPicker(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.cwd = "/work"
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
	if starts != 1 || fileID == 0 || u.conversation().Submitted[u.Submission().ID].Text != "use $review please" || len(u.conversation().Submitted[u.Submission().ID].Skills) != 0 {
		t.Fatalf("foreground picker stranded fallback: %+v %+v", requests, u.conversation().Submitted[u.Submission().ID])
	}
	if !u.feedback.Alert || !strings.Contains(u.feedback.Text, "catalog unavailable") {
		t.Fatalf("foreground picker hid catalog failure: %q", u.feedback.Text)
	}
	pickerReply(t, u, fileID, `{"files":[{"root":"/work","path":"foo.go","match_type":"file"}]}`)
	if u.Text != "@foo" || !u.picker.Open || len(u.picker.Choices) != 1 {
		t.Fatalf("background failure broke foreground picker: %q %+v", u.Text, u.picker)
	}
}

func TestSkillAttachmentQueuedLookupIndependentOfForegroundPicker(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.cwd = "/work"
	u.conversation().Queued = []conversationmodel.Input{{Text: "use $review please"}}
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
	if u.conversation().Submitted[u.Submission().ID].Text != "use $review please" || len(u.conversation().Submitted[u.Submission().ID].Skills) != 1 || u.Text != "@foo" {
		t.Fatalf("queued skill lost catalog or foreground draft: %+v %q", u.conversation().Submitted[u.Submission().ID], u.Text)
	}
}

func TestSkillAttachmentPartialCatalogFailureKeepsAvailableSkillsVisible(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.cwd = "/work"
	appServerTestKeys(t, u, "\x1b[200~use $review and $broken please\x1b[201~\r")
	requests := pickerRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("unexpected catalog request: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":"/work","skills":[{"name":"review","path":"/work/review/SKILL.md","enabled":true}],"errors":[{"message":"broken skill manifest"}]}]}`)
	if u.conversation().Submitted[u.Submission().ID].Text != "use $review and $broken please" || len(u.conversation().Submitted[u.Submission().ID].Skills) != 1 || u.conversation().Submitted[u.Submission().ID].Skills[0].Name != "review" {
		t.Fatalf("partial catalog failure lost text or available attachment: %+v", u.conversation().Submitted[u.Submission().ID])
	}
	if !u.feedback.Alert || !strings.Contains(u.feedback.Text, "Skill catalog incomplete") || !strings.Contains(u.feedback.Text, "broken skill manifest") {
		t.Fatalf("partial catalog failure was hidden: %q", u.feedback.Text)
	}
}
