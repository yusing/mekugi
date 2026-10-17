package router

import (
	"encoding/json/jsontext"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestComposerSelectionMentionIsOneToken(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertDraft("see ")
	u.insertSelection("message", "first\nquote", "")
	u.insertSelection("message", "second", "")
	if want := "see [Selected message] [Selected message 2] "; u.Text != want {
		t.Fatalf("draft = %q, want %q", u.Text, want)
	}
	spans := sessionpresentation.DraftSpans(u.draftSnapshot().Draft)
	if len(spans) != 2 || spans[0].Kind != activitypresentation.SelectionToken || u.Text[spans[1].Start:spans[1].End] != "[Selected message 2]" {
		t.Fatalf("spans = %+v", spans)
	}
	// Backspace after the token removes it whole, then undo restores it.
	u.CursorBack = len(" ")
	u.deleteDraft(true)
	if u.Text != "see [Selected message]  " || len(u.Selections) != 1 {
		t.Fatalf("backspace split token: %q %+v", u.Text, u.Selections)
	}
	u.undoDraft(false)
	if len(u.Selections) != 2 || u.Selections[1].Text != "second" {
		t.Fatalf("undo lost token: %+v", u.Selections)
	}
	// Queue joining shifts later entries' tokens.
	joined := conversationmodel.Join(conversationmodel.Input{Text: "x"}, u.draftSnapshot())
	if got := joined.Selections[0]; joined.Text[got.Start:got.End] != "[Selected message]" {
		t.Fatalf("joined token = %q", joined.Text[got.Start:got.End])
	}
}

func TestComposerSelectionSubmitsQuotedFrames(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertDraft("explain ")
	u.insertSelection("diff hunk @amber1:42-45", "+new\n-old", "a.go")
	d := u.takeDraft()
	input := conversationbinding.Input(d.Draft)
	if len(input) != 2 {
		t.Fatalf("input = %+v", input)
	}
	elements, _ := input[0]["text_elements"].([]binding.InputTextElement)
	if input[0]["text"] != "explain [Selected diff hunk @amber1:42-45] " || len(elements) != 1 || elements[0].Placeholder != "[Selected diff hunk @amber1:42-45]" {
		t.Fatalf("prompt part = %+v", input[0])
	}
	envelope, _ := input[1]["text"].(string)
	frames, ok := attachments.Decode(envelope)
	want := "Selected text for \"[Selected diff hunk @amber1:42-45]\" from \"a.go\" (UTF-8 bytes 0:9 of 9; quoted from the user's screen, not a separate request):\n+new\n-old"
	if !ok || !slices.Equal(frames, []string{want}) {
		t.Fatalf("frames = %q, %v", frames, ok)
	}
	// The model receives the quote as its own user message.
	content, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": "explain [Selected diff hunk @amber1:42-45] "}, {"type": "input_text", "text": envelope}})
	items, _ := json.Marshal([]map[string]jsontext.Value{{"type": []byte(`"message"`), "role": []byte(`"user"`), "content": content}})
	request := &codexresponses.Request{Fields: map[string]jsontext.Value{"input": items}}
	if !requestexecution.ProjectFileAttachments(request) || !strings.Contains(string(request.Fields["input"]), `+new\n-old`) || strings.Contains(string(request.Fields["input"]), "mekugi-file-attachments") {
		t.Fatalf("projected input = %s", request.Fields["input"])
	}
	// The host echo keeps the concise label as one styled token.
	spans := conversationbinding.ElementSpans(input[0]["text"].(string), elements)
	if len(spans) != 1 || spans[0].Kind != composer.Selection {
		t.Fatalf("transcript spans = %+v", spans)
	}
}

func TestComposerSelectionOverBudgetIsExplicit(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertSelection("message", strings.Repeat("a", attachments.Budget/2+1), "")
	if len(u.Selections) != 0 || u.Text != "" {
		t.Fatalf("oversized selection inserted: %q", u.Text)
	}
	for range 3 {
		u.insertSelection("message", strings.Repeat("b\n", attachments.Budget/5), "")
	}
	d := u.takeDraft()
	frames, ok := attachments.Decode(d.Attachments[0])
	if !ok || !strings.Contains(frames[len(frames)-1], "CONTENT NOT ATTACHED (attachment budget exceeded)") || d.AttachmentNotice == "" {
		t.Fatalf("omission missing: ok=%v notice=%q last=%q", ok, d.AttachmentNotice, frames[len(frames)-1][:40])
	}
}

func TestTerminalUIDiffSelectionMentionsChangeLines(t *testing.T) {
	rows := []string{"  a.go +2 -1", "  42│+new", "  43│-old", "  44│ ctx", "  9│+other"}
	u := selectionTestUI(rows...)
	u.Layout = terminalsession.Layout{Diff: terminalsession.Rect{0, 0, 40, len(rows)}}
	sources := []diffrender.LineSource{
		{Path: "/w/a.go", Content: 2},
		{Change: "amber1", Path: "/w/a.go", Line: 42, Content: 5},
		{Change: "amber1", Path: "/w/a.go", Line: 43, Content: 5},
		{Change: "amber1", Path: "/w/a.go", Line: 44, Content: 5},
		{Change: "amber2", Path: "/w/b.go", Line: 9, Content: 4},
	}
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
		left := make([]int, len(sources))
		for i, source := range sources {
			left[i] = source.Content
		}
		u.Selection = &terminalsession.SourceSelection{Selection: terminalsession.NewSelection(terminalsession.Surface{Rect: u.Layout.Diff, Rows: rows, ContentLeft: left}, 0, tt.y1), Pane: u.Layout.Diff, Diff: sources, Workspace: "/w"}
		u.Selection.Move(39, tt.y2, true)

		if got := u.Selection.Text(); got != tt.text {
			t.Fatalf("diff selection text = %q, want %q", got, tt.text)
		}
		if description, source := u.Selection.MentionDescription(); description != tt.description || source != tt.source {
			t.Fatalf("mention = %q from %q, want %q from %q", description, source, tt.description, tt.source)
		}
	}
	u.selectionAction('r')
	if u.main.Text != "[Selected diff hunks @amber1 +1] " || u.main.Selections[0].Text != "-old\n ctx\n+other" {
		t.Fatalf("diff mention draft = %q", u.main.Text)
	}
}

func TestTerminalUIDiffSelectionUsesRenderedPane(t *testing.T) {
	for _, width := range []int{80, 130} { // Without and with the side file navigator.
		captures := []presentation.Chunk{
			liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a.go", 8), presentation.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
			liveDiffCapture("b", "b.go", 2, "", liveDiffLinesFile("b.go", 2), presentation.Origin{Change: "amber2", Caller: "/root", Source: "apply_patch"}),
		}
		c := liveDiffChangesPane(t, width, 16, captures)
		c.Key("g")
		screen := vt.NewEmulator(width, 16)
		defer screen.Close()
		c.Invalidate()
		if err := c.Paint(t.Context(), screen, width, 16); err != nil {
			t.Fatal(err)
		}
		sourceView := c.Selection(width-2, 15)
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
		if (sourceView.X > 0) != (width > 100) || (sourceView.Y > 0) != (width < 100) {
			t.Fatalf("width %d: source column %d row %d", width, sourceView.X, sourceView.Y)
		}
		u := selectionTestUI(rows...)
		u.width, u.Frame.Width = width, width
		u.Layout = terminalsession.Layout{Diff: terminalsession.Rect{0, 0, width, 16}}
		u.Auxiliary = c
		if sourceView.Y > 0 && u.Frame.DiffSelection(u.frameInput(), sourceView.X, 0) != nil {
			t.Fatal("stacked navigator was selectable")
		}
		u.selectionMouse(0, sourceView.X, first, false)
		if !u.selectionMouse(32, width-2, last, false) || !u.selectionMouse(0, width-2, last, true) || u.Selection == nil {
			t.Fatalf("rendered diff was not selectable (width %d)", width)
		}
		description, source := u.Selection.MentionDescription()
		if description != "diff hunk @amber1:3-5" || source != "a.go" {
			t.Fatalf("mention = %q from %q", description, source)
		}
		if got, want := u.Selection.Text(), "+a.go_line_02\n+a.go_line_03\n+a.go_line_04"; got != want {
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
			if attached, ok := attachments.Decode(part.Text); ok {
				frames = append(frames, attached...)
			} else if part.Type == "text" {
				text += part.Text
			}
		}
		restored := composer.RestoreSelections(text, frames)
		if len(restored) != 2 || restored[0].Text != "first quote" || restored[1].Text != "second quote" || text[restored[0].Start:restored[0].End] == text[restored[1].Start:restored[1].End] {
			t.Fatalf("retry lost distinct quotes: text=%q restored=%+v", text, restored)
		}
	})
	t.Run("pending labels stay distinct", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.insertSelection("message", "one", "")
		u.conversation().Queued = append(u.conversation().Queued, u.takeDraft())
		u.insertSelection("message", "two", "")
		if u.Text != "[Selected message 2] " {
			t.Fatalf("queued label reused: %q", u.Text)
		}
	})
	t.Run("hidden question releases parked labels", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.insertSelection("message", "main quote", "")
		questionTestAsync(t, u, "parked", "Which scope?")
		u.openQuestions()
		u.hideQuestions()
		u.deleteDraftRange(0, len(u.Text))
		u.insertSelection("message", "replacement quote", "")
		if u.Text != "[Selected message] " {
			t.Fatalf("inactive parked editor reserved a label: %q", u.Text)
		}
	})
	t.Run("omissions never overflow the envelope", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.insertSelection("message", strings.Repeat("a", attachments.Budget/2-1024), "")
		u.insertSelection("message", strings.Repeat("b", attachments.Budget/2-3072), "")
		for range 40 {
			u.insertSelection("message", "tiny but over budget", "")
		}
		d := u.takeDraft()
		frames, ok := attachments.Decode(d.Attachments[0])
		if !ok || len(d.Attachments[0]) > attachments.Budget || !strings.Contains(frames[len(frames)-1], "remaining mentions: CONTENT NOT ATTACHED") {
			t.Fatalf("envelope %d bytes decoded=%v", len(d.Attachments[0]), ok)
		}
	})
	t.Run("resume recalls quotes", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.insertDraft("why ")
		u.insertSelection("diff hunk @amber1:3", strings.Repeat("x\n", attachments.Chunk), "a.go")
		u.insertSelection("message", "short", "")
		d := u.takeDraft()
		frames, _ := attachments.Decode(d.Attachments[0])
		restored := composer.RestoreSelections(d.Text, frames)
		if len(restored) != 2 || restored[0].Text != strings.Repeat("x\n", attachments.Chunk) || restored[0].Source != "a.go" || d.Text[restored[1].Start:restored[1].End] != "[Selected message]" || restored[1].Text != "short" {
			t.Fatalf("restored = %+v", restored)
		}
		if len(frames) < 3 {
			t.Fatalf("long selection was not split: %d frames", len(frames))
		}
	})
}

func TestTerminalUIDiffSelectionEdges(t *testing.T) {
	u := selectionTestUI("  1│-a", "  2│-b", "  7│+c", "  8│ d")
	u.Layout = terminalsession.Layout{Diff: terminalsession.Rect{0, 0, 40, 4}}
	sources := []diffrender.LineSource{
		{}, {Change: "amber1", Line: 2, Deleted: true, Content: 4},
		{Change: "amber1", Line: 7, Content: 4}, {Change: "amber1", Line: 8, Content: 4},
	}

	for _, tt := range []struct {
		y1, y2 int
		want   string
	}{{1, 3, "diff hunk @amber1:7-8"}, {1, 1, "diff hunk @amber1:2"}} {
		u.Selection = &terminalsession.SourceSelection{Selection: terminalsession.NewSelection(terminalsession.Surface{Rect: u.Layout.Diff, Rows: u.Frame.Rows, ContentLeft: []int{0, 4, 4, 4}}, 0, tt.y1), Diff: sources}
		u.Selection.Move(39, tt.y2, true)

		if got, _ := u.Selection.MentionDescription(); got != tt.want {
			t.Fatalf("mention = %q, want %q", got, tt.want)
		}
	}
	u.main.review.Failure = "boom"
	if u.Frame.DiffSelection(u.frameInput(), 0, 2) != nil {
		t.Fatal("failed diff body was selectable")
	}
}
