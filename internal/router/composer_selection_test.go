package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestComposerSelectionMentionIsOneToken(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertDraft("see ")
	u.insertSelection("message", "first\nquote", "")
	u.insertSelection("message", "second", "")
	if want := "see [Selected message] [Selected message 2] "; u.draft != want {
		t.Fatalf("draft = %q, want %q", u.draft, want)
	}
	spans := u.draftSnapshot().displaySpans()
	if len(spans) != 2 || spans[0].Kind != activityui.SelectionToken || u.draft[spans[1].Start:spans[1].End] != "[Selected message 2]" {
		t.Fatalf("spans = %+v", spans)
	}
	// Backspace after the token removes it whole, then undo restores it.
	u.cursorBack = len(" ")
	u.deleteDraft(true)
	if u.draft != "see [Selected message]  " || len(u.selections) != 1 {
		t.Fatalf("backspace split token: %q %+v", u.draft, u.selections)
	}
	u.undoDraft(false)
	if len(u.selections) != 2 || u.selections[1].text != "second" {
		t.Fatalf("undo lost token: %+v", u.selections)
	}
	// Queue joining shifts later entries' tokens.
	joined := joinDrafts(composerDraft{text: "x"}, u.draftSnapshot())
	if got := joined.selections[0]; joined.text[got.start:got.end] != "[Selected message]" {
		t.Fatalf("joined token = %q", joined.text[got.start:got.end])
	}
}

func TestComposerSelectionSubmitsQuotedFrames(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertDraft("explain ")
	u.insertSelection("diff hunk @amber1:42-45", "+new\n-old", "a.go")
	d := u.takeDraft()
	input := d.input()
	if len(input) != 2 {
		t.Fatalf("input = %+v", input)
	}
	elements, _ := input[0]["text_elements"].([]composerTextElement)
	if input[0]["text"] != "explain [Selected diff hunk @amber1:42-45] " || len(elements) != 1 || elements[0].Placeholder != "[Selected diff hunk @amber1:42-45]" {
		t.Fatalf("prompt part = %+v", input[0])
	}
	envelope, _ := input[1]["text"].(string)
	frames, ok := decodeFileAttachments(envelope)
	want := "Selected text for \"[Selected diff hunk @amber1:42-45]\" from \"a.go\" (UTF-8 bytes 0:9 of 9; quoted from the user's screen, not a separate request):\n+new\n-old"
	if !ok || !slices.Equal(frames, []string{want}) {
		t.Fatalf("frames = %q, %v", frames, ok)
	}
	// The model receives the quote as its own user message.
	content, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": "explain [Selected diff hunk @amber1:42-45] "}, {"type": "input_text", "text": envelope}})
	items, _ := json.Marshal([]map[string]jsontext.Value{{"type": []byte(`"message"`), "role": []byte(`"user"`), "content": content}})
	request := &parsedResponsesRequest{fields: map[string]jsontext.Value{"input": items}}
	if !projectFileAttachments(request) || !strings.Contains(string(request.fields["input"]), `+new\n-old`) || strings.Contains(string(request.fields["input"]), "mekugi-file-attachments") {
		t.Fatalf("projected input = %s", request.fields["input"])
	}
	// The host echo keeps the concise label as one styled token.
	spans := composerElementSpans(input[0]["text"].(string), elements)
	if len(spans) != 1 || spans[0].Kind != activityui.SelectionToken {
		t.Fatalf("transcript spans = %+v", spans)
	}
}

func TestComposerSelectionOverBudgetIsExplicit(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertSelection("message", strings.Repeat("a", fileAttachmentBudget/2+1), "")
	if len(u.selections) != 0 || u.draft != "" {
		t.Fatalf("oversized selection inserted: %q", u.draft)
	}
	for range 3 {
		u.insertSelection("message", strings.Repeat("b\n", fileAttachmentBudget/5), "")
	}
	d := u.takeDraft()
	frames, ok := decodeFileAttachments(d.attachments[0])
	if !ok || !strings.HasSuffix(frames[len(frames)-1], "CONTENT NOT ATTACHED (attachment budget exceeded). Ask the user to paste it if needed.") || d.attachmentNotice == "" {
		t.Fatalf("omission missing: ok=%v notice=%q last=%q", ok, d.attachmentNotice, frames[len(frames)-1][:40])
	}
}

func TestTerminalUIDiffSelectionMentionsChangeLines(t *testing.T) {
	rows := []string{"  a.go +2 -1", "  42│+new", "  43│-old", "  44│ ctx", "  9│+other"}
	u := selectionTestUI(rows...)
	u.layout = terminalLayout{diff: terminalRect{0, 0, 40, len(rows)}}
	u.diff = &liveDiffTerminalController{diffMode: true, workspace: "/w", painted: []livediff.LineSource{
		{Path: "/w/a.go", Content: 2},
		{Change: "amber1", Path: "/w/a.go", Line: 42, Content: 5},
		{Change: "amber1", Path: "/w/a.go", Line: 43, Content: 5},
		{Change: "amber1", Path: "/w/a.go", Line: 44, Content: 5},
		{Change: "amber2", Path: "/w/b.go", Line: 9, Content: 4},
	}}
	for _, tt := range []struct {
		y1, y2              int
		description, source string
		text                string
	}{
		{1, 2, "diff hunk @amber1:42-43", "a.go", "+new\n-old"},
		{3, 3, "diff hunk @amber1:44", "a.go", " ctx"},
		{0, 0, "diff", "a.go", "a.go +2 -1"},
		{2, 4, "diff hunks @amber1 +1", "", "-old\n ctx\n+other"},
	} {
		if u.selectionMouse(0, 0, tt.y1, false) {
			t.Fatal("diff press was not passed through to the diff pane")
		}
		if !u.selectionMouse(32, 39, tt.y2, false) || !u.selectionMouse(0, 39, tt.y2, true) || u.selection == nil {
			t.Fatal("diff drag was not selected")
		}
		if got := u.selection.text(); got != tt.text {
			t.Fatalf("diff selection text = %q, want %q", got, tt.text)
		}
		if description, source := u.selection.mentionDescription(); description != tt.description || source != tt.source {
			t.Fatalf("mention = %q from %q, want %q from %q", description, source, tt.description, tt.source)
		}
	}
	u.selectionAction('r')
	if u.main.draft != "[Selected diff hunks @amber1 +1] " || u.main.selections[0].text != "-old\n ctx\n+other" {
		t.Fatalf("diff mention draft = %q", u.main.draft)
	}
}

func TestTerminalUIDiffSelectionUsesRenderedPane(t *testing.T) {
	for _, width := range []int{80, 130} { // Without and with the side file navigator.
		captures := []livediff.Chunk{
			liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a.go", 8), livediff.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
			liveDiffCapture("b", "b.go", 2, "", liveDiffLinesFile("b.go", 2), livediff.Origin{Change: "amber2", Caller: "/root", Source: "apply_patch"}),
		}
		c := liveDiffChangesController(t, width, 16, captures)
		c.view.Open(0)
		c.native = true
		screen := vt.NewEmulator(width, 16)
		defer screen.Close()
		c.stdout = screen
		c.frame(t)
		rows := strings.Split(screen.Render(), "\n")
		find := func(text string) int {
			for y, row := range rows {
				if strings.Contains(ansi.Strip(row), text) {
					return y
				}
			}
			t.Fatalf("%q not rendered (width %d):\n%s", text, width, screen.String())
			return -1
		}
		first, last := find("a.go_line_02"), find("a.go_line_04")
		if (c.sourceX > 0) != (width > 100) || (c.sourceY > 0) != (width < 100) {
			t.Fatalf("width %d: source column %d row %d", width, c.sourceX, c.sourceY)
		}
		u := selectionTestUI(rows...)
		u.width, u.paintedWidth = width, width
		u.layout = terminalLayout{diff: terminalRect{0, 0, width, 16}}
		u.diff = c
		if c.sourceY > 0 && u.diffSelection(c.sourceX, 0) != nil {
			t.Fatal("stacked navigator was selectable")
		}
		u.selectionMouse(0, c.sourceX, first, false)
		if !u.selectionMouse(32, width-2, last, false) || !u.selectionMouse(0, width-2, last, true) || u.selection == nil {
			t.Fatalf("rendered diff was not selectable (width %d)", width)
		}
		description, source := u.selection.mentionDescription()
		if description != "diff hunk @amber1:3-5" || source != "a.go" {
			t.Fatalf("mention = %q from %q", description, source)
		}
		if got, want := u.selection.text(), "+a.go_line_02\n+a.go_line_03\n+a.go_line_04"; got != want {
			t.Fatalf("selected = %q, want %q", got, want)
		}
	}
}

func TestComposerSelectionReviewEdges(t *testing.T) {
	t.Run("rejected pending submission retains unique quotes", func(t *testing.T) {
		u, w := newAppServerTestUI()
		u.insertSelection("message", "first quote", "")
		appServerTestKeys(t, u, "\r")
		requests := appServerTurnRequests(t, w)
		if len(requests) != 1 || requests[0].Method != "turn/start" {
			t.Fatalf("start requests = %+v", requests)
		}
		u.insertSelection("message", "second quote", "")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, requests[0].ID))
		appServerTestKeys(t, u, "\r")
		requests = appServerTurnRequests(t, w)
		if len(requests) != 1 || requests[0].Method != "turn/start" {
			t.Fatalf("retry requests = %+v", requests)
		}
		var text string
		var frames []string
		for _, part := range requests[0].Params.Input {
			if attached, ok := decodeFileAttachments(part.Text); ok {
				frames = append(frames, attached...)
			} else if part.Type == "text" {
				text += part.Text
			}
		}
		restored := restoredSelections(text, frames)
		if len(restored) != 2 || restored[0].text != "first quote" || restored[1].text != "second quote" || text[restored[0].start:restored[0].end] == text[restored[1].start:restored[1].end] {
			t.Fatalf("retry lost distinct quotes: text=%q restored=%+v", text, restored)
		}
	})
	t.Run("pending labels stay distinct", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.insertSelection("message", "one", "")
		u.queued = append(u.queued, u.takeDraft())
		u.insertSelection("message", "two", "")
		if u.draft != "[Selected message 2] " {
			t.Fatalf("queued label reused: %q", u.draft)
		}
	})
	t.Run("question answers quote text", func(t *testing.T) {
		u := selectionTestUI("hello world")
		u.main.questions.active = &nativeQuestionCall{questions: []nativeQuestion{{}}}
		selectionTestDrag(t, u, 0, 0, 4, 0)
		u.selectionAction('r')
		if u.main.draft != "> hello\n\n" || len(u.main.selections) != 0 {
			t.Fatalf("question answer draft = %q", u.main.draft)
		}
	})
	t.Run("omissions never overflow the envelope", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.insertSelection("message", strings.Repeat("a", fileAttachmentBudget/2-1024), "")
		u.insertSelection("message", strings.Repeat("b", fileAttachmentBudget/2-3072), "")
		for range 40 {
			u.insertSelection("message", "tiny but over budget", "")
		}
		d := u.takeDraft()
		frames, ok := decodeFileAttachments(d.attachments[0])
		if !ok || len(d.attachments[0]) > fileAttachmentBudget || !strings.Contains(frames[len(frames)-1], "remaining mentions: CONTENT NOT ATTACHED") {
			t.Fatalf("envelope %d bytes decoded=%v", len(d.attachments[0]), ok)
		}
	})
	t.Run("resume recalls quotes", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.insertDraft("why ")
		u.insertSelection("diff hunk @amber1:3", strings.Repeat("x\n", fileAttachmentChunk), "a.go")
		u.insertSelection("message", "short", "")
		d := u.takeDraft()
		frames, _ := decodeFileAttachments(d.attachments[0])
		restored := restoredSelections(d.text, frames)
		if len(restored) != 2 || restored[0].text != strings.Repeat("x\n", fileAttachmentChunk) || restored[0].source != "a.go" || d.text[restored[1].start:restored[1].end] != "[Selected message]" || restored[1].text != "short" {
			t.Fatalf("restored = %+v", restored)
		}
		if len(frames) < 3 {
			t.Fatalf("long selection was not split: %d frames", len(frames))
		}
	})
}

func TestTerminalUIDiffSelectionEdges(t *testing.T) {
	u := selectionTestUI("  1│-a", "  2│-b", "  7│+c", "  8│ d")
	u.layout = terminalLayout{diff: terminalRect{0, 0, 40, 4}}
	u.diff = &liveDiffTerminalController{diffMode: true, sourceY: 1, painted: []livediff.LineSource{
		{},
		{Change: "amber1", Line: 2, Deleted: true, Content: 4},
		{Change: "amber1", Line: 7, Content: 4},
		{Change: "amber1", Line: 8, Content: 4},
	}}
	if u.diffSelection(0, 0) != nil {
		t.Fatal("rows above the diff source were selectable")
	}
	for _, tt := range []struct {
		y1, y2 int
		want   string
	}{{1, 3, "diff hunk @amber1:7-8"}, {1, 1, "diff hunk @amber1:2"}} {
		u.selectionMouse(0, 0, tt.y1, false)
		u.selectionMouse(32, 39, tt.y2, false)
		u.selectionMouse(0, 39, tt.y2, true)
		if got, _ := u.selection.mentionDescription(); got != tt.want {
			t.Fatalf("mention = %q, want %q", got, tt.want)
		}
	}
	u.diffFailure = "boom"
	if u.diffSelection(0, 2) != nil {
		t.Fatal("failed diff body was selectable")
	}
}
