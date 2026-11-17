package launch

import (
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/libraries/diffrender"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestComposerSelectionMentionIsOneToken(t *testing.T) {
	u, _ := newAppServerTestUI()
	insertDraft(&u.attached, u.Editor, u.clock, u.ownedImages, &u.questions, &u.approvals, u.side, "see ")
	insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "first\nquote", "")
	insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "second", "")
	if want := "see [Selected message] [Selected message 2] "; u.Text != want {
		t.Fatalf("draft = %q, want %q", u.Text, want)
	}
	spans := sessionpresentation.DraftSpans(draftSnapshot(u.Editor).Draft)
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
	joined := conversationmodel.Join(conversationmodel.Input{Text: "x"}, draftSnapshot(u.Editor))
	if got := joined.Selections[0]; joined.Text[got.Start:got.End] != "[Selected message]" {
		t.Fatalf("joined token = %q", joined.Text[got.Start:got.End])
	}
}

func TestComposerSelectionOverBudgetIsExplicit(t *testing.T) {
	u, _ := newAppServerTestUI()
	insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", strings.Repeat("a", attachments.Budget/2+1), "")
	if len(u.Selections) != 0 || u.Text != "" {
		t.Fatalf("oversized selection inserted: %q", u.Text)
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
		u.selectionAction('r')
		selection := u.main.Selections[len(u.main.Selections)-1]
		if u.main.Text[selection.Start:selection.End] != "[Selected "+tt.description+"]" || selection.Text != tt.text || selection.Source != tt.source {
			t.Fatalf("reference lost diff attribution: %q %+v", u.main.Text, selection)
		}
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
		u.Width, u.Frame.Width = width, width
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
		u.selectionAction('r')
		if u.main.Text != "[Selected diff hunk @amber1:3-5] " || u.main.Selections[0].Source != "a.go" {
			t.Fatalf("painted diff reference = %q %+v", u.main.Text, u.main.Selections)
		}
	}
}

func TestComposerSelectionReviewEdges(t *testing.T) {
	t.Run("rejected pending submission retains unique quotes", func(t *testing.T) {
		u, w := newAppServerTestUI()
		insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "first quote", "")
		appServerTestKeys(t, u, "\r")
		requests := appServerTurnRequests(t, w)
		if len(requests) != 1 || requests[0].Method != "turn/start" {
			t.Fatalf("start requests = %+v", requests)
		}
		insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "second quote", "")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, requests[0].ID))
		appServerTestKeys(t, u, "\r")
		requests = appServerTurnRequests(t, w)
		if len(requests) != 1 || requests[0].Method != "turn/start" {
			t.Fatalf("retry requests = %+v", requests)
		}
		if text := requests[0].text(); text != "[Selected message]\n[Selected message 2]\n\n---\nUser selections:\n- message: first quote\n- message 2: second quote" {
			t.Fatalf("retry lost distinct quotes: %q", text)
		}
	})
	t.Run("pending labels stay distinct", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "one", "")
		u.attached.conversation.Queued = append(u.attached.conversation.Queued, prepareDraft(&u.picker, u.attached.cwd, u.skillEnvironment, u.Editor.Take()))
		insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "two", "")
		if u.Text != "[Selected message 2] " {
			t.Fatalf("queued label reused: %q", u.Text)
		}
	})
	t.Run("hidden question releases parked labels", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "main quote", "")
		questionTestAsync(t, u, "parked", "Which scope?")
		openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
		u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.Editor, u.Thread)
		u.nativeSession.deleteDraftRange(&u.attached, &u.picker, &u.Editor, u.side, u.clock, u.statusPanel != nil || u.resumePicker != nil, 0, len(u.Text))
		insertSelection(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, "message", "replacement quote", "")
		if u.Text != "[Selected message] " {
			t.Fatalf("inactive parked editor reserved a label: %q", u.Text)
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
