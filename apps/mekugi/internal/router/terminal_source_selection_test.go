package router

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestTerminalSourceSelectionDirectionsAndActions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		source, visible, want string
		left, right           int
	}{
		{"A | B\n--- | ---\n**alpha** | SECRET", "alpha", "**lph**", 1, 3},
		{"**alpha beta**", "alpha beta", " **beta**", 5, 9},
		{"**alpha beta**", "alpha beta", "**alpha** ", 0, 5},
		{"before `` `x` `` after", "before `x` after", "`` `x ``", 7, 8},
	} {
		for _, activity := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				for _, action := range []byte{'r', 'c'} {
					p := activityui.Painter{CopySource: true}
					raw := p.Markdown(tc.source, 40, false)
					rows := make([]string, len(raw))
					meta := make([][]terminalui.CopySpan, len(raw))
					x, y := -1, -1
					for i, row := range raw {
						rows[i], meta[i] = terminalui.ExtractCopy(row)
						for _, s := range meta[i] {
							if s.Text == tc.visible {
								x, y = s.Column, i
							}
						}
					}
					u := selectionTestUI(rows...)
					u.Frame.Copy = append(meta, nil)
					if activity {
						u.Agents = activityui.NewView()
						u.Agents.FeedLeft, u.Agents.FeedTop = 1, 1
						u.Agents.FeedRight, u.Agents.FeedRows = 40, len(rows)
						u.main.agents = u.Agents
						u.main.sessionPresentation()
						u.main.testActivity(u.Agents).setDisplayFilter(true, "/root/worker")
					}
					u.main.publishPresentation(u.main.now())
					if x < 0 {
						t.Fatal("no cell")
					}
					a, b := x+tc.left, x+tc.right
					if reverse {
						a, b = b, a
					}
					selectionTestDrag(t, u, a, y, b, y)
					if got := u.Selection.Text(); got != tc.want {
						t.Fatalf("selected=%q", got)
					}
					u.selectionAction(terminalsession.SelectionIntent(action))
					if action == 'c' {
						want := base64.StdEncoding.EncodeToString([]byte(tc.want))
						if !strings.Contains(u.Frame.Clipboard, want) {
							t.Fatal(u.Frame.Clipboard)
						}
					} else if len(u.main.Selections) != 1 || u.main.Selections[0].Text != tc.want {
						t.Fatalf("reference=%+v", u.main.Selections)
					}
				}
			}
		}
	}
}

func TestTerminalSourceSelectionCodeLiteralDecoration(t *testing.T) {
	p := activityui.Painter{CopySource: true}
	raw := p.Markdown("```\n│ literal  \n\n\n  tail\n```", 40, false)
	rows := make([]string, len(raw))
	meta := make([][]terminalui.CopySpan, len(raw))
	for i, row := range raw {
		rows[i], meta[i] = terminalui.ExtractCopy(row)
	}
	u := selectionTestUI(rows...)
	u.Frame.Copy = append(meta, nil)
	selectionTestDrag(t, u, 0, 0, 39, len(rows)-1)
	if got := u.Selection.Text(); got != "│ literal  \n\n\n  tail" {
		t.Fatalf("copy=%q", got)
	}
	left, _ := u.Selection.Bounds(0)
	if left != 1 {
		t.Fatalf("literal code gutter stripped: %d", left)
	}
}

func TestTerminalBTWSourceCopyAndEscape(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	setSideTest(u, &sessioncontrol.Side{SideAnswer: &conversationmodel.SideAnswer{Question: "side question", Status: "completed", Answer: []conversationmodel.Answer{{Text: "**side answer**"}}}})
	var wire bytes.Buffer
	if err := u.testPaint(&wire, 80, 30); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "mekugi-copy") {
		t.Fatal("private source transport escaped")
	}
	rect := u.mainView.Side.Rect
	rect.X += terminalTest(u).Layout.Main.X
	rect.Y += terminalTest(u).Layout.Main.Y
	y := -1
	x := -1
	for row := rect.Y; row < rect.Y+rect.H; row++ {
		for _, span := range terminalTest(u).Frame.Copy[row] {
			if span.Text == "side answer" {
				x, y = span.Column, row
				break
			}
		}
	}

	if y < 0 {
		t.Fatalf("no dock: %+v", rect)
	}
	selectionTestDrag(t, terminalTest(u), x, y, x+10, y)
	if got := terminalTest(u).Selection.Text(); got != "**side answer**" {
		t.Fatalf("btw copied=%q", got)
	}
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.side == nil || terminalTest(u).Selection != nil {
		t.Fatal("Escape closed dock before selection")
	}
	selectionTestDrag(t, terminalTest(u), x, y, x+10, y)
	if err := terminalTest(u).key(3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminalTest(u).Frame.Clipboard, base64.StdEncoding.EncodeToString([]byte("**side answer**"))) {
		t.Fatal("btw clipboard missing")
	}
	if u.side == nil || terminalTest(u).Selection != nil {
		t.Fatal("copy closed the dock or retained selection")
	}
	u.Text, u.CursorBack = "before after", len("after")
	selectionTestDrag(t, terminalTest(u), x, y, x+10, y)
	if err := terminalTest(u).key('r'); err != nil {
		t.Fatal(err)
	}
	if u.side == nil || terminalTest(u).Selection != nil || terminalTest(u).Focus != 0 || u.Text != "before [Selected side answer] after" || len(u.Selections) != 1 || u.Selections[0].Text != "**side answer**" {
		t.Fatalf("btw reference: draft=%q selections=%+v dock=%v", u.Text, u.Selections, u.side != nil)
	}
}

func TestAppServerBTWShimmerElapsed(t *testing.T) {
	u, _ := newAppServerTestUI()
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	now := start.Add(75 * time.Second)
	u.clock = func() time.Time { return now }
	setSideTest(u, &sessioncontrol.Side{Busy: true, SideAnswer: &conversationmodel.SideAnswer{Status: "Answering", Started: start}})
	a := u.btwRows(80, 8)[0]
	now = now.Add(100 * time.Millisecond)
	b := u.btwRows(80, 8)[0]
	if a == b || ansi.Strip(a) != ansi.Strip(b) || !strings.Contains(ansi.Strip(a), "Answering · 1m15s") || !u.publishPresentation(u.now()).Status.Animating {
		t.Fatalf("status=%q / %q", a, b)
	}
	u.side.Busy = false
	u.side.Status = "completed"
	if strings.Contains(ansi.Strip(u.btwRows(80, 8)[0]), "Answering") {
		t.Fatal("completed answer still shimmers")
	}
}

func TestUISnapshotBTWCopyDock(t *testing.T) {
	u, _ := newAppServerTestUI()
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return start.Add(75 * time.Second) }
	u.view.Painter.Theme = diffrender.DarkTheme
	setSideTest(u, &sessioncontrol.Side{Busy: true, SideAnswer: &conversationmodel.SideAnswer{Status: "Answering", Started: start, Question: "Which option?", Answer: []conversationmodel.Answer{{Text: "Name | Value\n--- | ---\n**alpha** | `x`"}}}})
	uisnapshot.Assert(t, "testdata/snapshots/btw-copy-dock.txt", strings.Join(u.btwRows(60, 12), "\n")+"\n")
}

func TestTerminalSourceScrolledMainReply(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	// Scroll back to the original reply above a long trailing tool run.
	u.Turn = "running"
	u.testActivity(u.view).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "**original reply**", Observed: time.Now()}, {Seq: 2, Agent: "Main", Kind: "tool", Text: "Run: go test\n" + strings.Repeat("output\n", 100), Observed: time.Now()}}})
	u.view.Following, u.view.Offset = false, 0
	screen := vt.NewEmulator(80, 30)
	defer screen.Close()
	if err := u.testPaint(screen, 80, 30); err != nil {
		t.Fatal(err)
	}
	x, y := -1, -1
	for row, spans := range terminalTest(u).Frame.Copy {
		for _, s := range spans {
			if s.Text == "original reply" {
				x, y = s.Column, row
				break
			}
		}
		if y >= 0 {
			break
		}
	}
	if y < 0 {
		t.Fatalf("no original reply source: %s", screen.String())
	}
	selectionTestDrag(t, terminalTest(u), terminalTest(u).Layout.Main.X, y-1, x+13, y)
	if got := terminalTest(u).Selection.Text(); got != "**original reply**" {
		t.Fatalf("original reply copy=%q", got)
	}
}

func TestTerminalSourceRenderedTables(t *testing.T) {
	source := "Name | Value\n--- | ---:\n**alpha beta gamma delta epsilon** | `a|b`\n猫 | "
	want := "| Name | Value |\n| --- | ---: |\n| **alpha beta gamma delta epsilon** | `a\\|b` |\n| 猫 |  |"
	for _, activity := range []bool{false, true} {
		for _, width := range []int{24, 80, 120} {
			if activity && width < 100 {
				continue
			}
			for _, reverse := range []bool{false, true} {
				u, _ := newAppServerTestUI()
				u.ensureShell()
				t.Cleanup(func() { terminalTest(u).Diff.Close() })
				view := u.testActivity(u.view)
				if activity {
					view = u.testActivity(u.agents)
					u.childSource.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/worker", Final: true}}
					terminalTest(u).DiffOpen = false
				}
				applyTranscriptFixture(view, true, "", "main", "main", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: source})
				var wire bytes.Buffer
				if view.View == u.agents {
					u.testActivity(u.agents).setDisplayFilter(true, "Main")
				}
				if err := u.testPaint(&wire, width, 50); err != nil {
					t.Fatal(err)
				}
				first, last, left, right := -1, -1, 0, 0
				for y, spans := range terminalTest(u).Frame.Copy {
					for _, span := range spans {
						if span.Table != 0 {
							if first < 0 {
								first, left = y, span.Column
							}
							last = y
							right = max(right, span.Column+span.Width-1)
						}
					}
				}
				if first < 0 {
					t.Fatalf("no rendered table activity=%v width=%d", activity, width)
				}
				ax, ay, bx, by := left, first, right, last
				if reverse {
					ax, ay, bx, by = bx, by, ax, ay
				}
				selectionTestDrag(t, terminalTest(u), ax, ay, bx, by)
				expected := want
				if got := terminalTest(u).Selection.Text(); got != expected {
					t.Fatalf("activity=%v width=%d reverse=%v: %q want %q", activity, width, reverse, got, expected)
				}
			}
		}
	}
}

func TestTerminalSourceActivityCodeTabs(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	u.childSource.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/worker", Final: true}}
	terminalTest(u).DiffOpen = false
	applyTranscriptFixture(u.testActivity(u.agents), true, "", "main", "main", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "```\n\tx := 1  \n```"})
	var wire bytes.Buffer
	u.testActivity(u.agents).setDisplayFilter(true, "Main")
	if err := u.testPaint(&wire, 120, 40); err != nil {
		t.Fatal(err)
	}
	for y, spans := range terminalTest(u).Frame.Copy {
		for _, span := range spans {
			if span.Code {
				selectionTestDrag(t, terminalTest(u), span.Column, y, span.Column+span.Width-1, y)
				if got := terminalTest(u).Selection.Text(); got != "\tx := 1  " {
					t.Fatalf("activity source code=%q", got)
				}
				return
			}
		}
	}
	t.Fatal("no Activity code source")
}
