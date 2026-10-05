package router

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
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
					raw := p.Markdown(tc.source, 40)
					rows := make([]string, len(raw))
					meta := make([][]activityui.CopySpan, len(raw))
					x, y := -1, -1
					for i, row := range raw {
						rows[i], meta[i] = activityui.ExtractCopy(row)
						for _, s := range meta[i] {
							if s.Text == tc.visible {
								x, y = s.Column, i
							}
						}
					}
					u := selectionTestUI(rows...)
					u.paintedCopy = append(meta, nil)
					if activity {
						u.agents = newLiveActivityView()
						u.agents.feedLeft, u.agents.feedTop = 1, 1
						u.agents.feedRight, u.agents.feedRows = 40, len(rows)
						u.layout.agents = u.layout.codex
						u.layout.codex = terminalRect{50, 0, 40, len(rows)}
					}
					if x < 0 {
						t.Fatal("no cell")
					}
					a, b := x+tc.left, x+tc.right
					if reverse {
						a, b = b, a
					}
					selectionTestDrag(t, u, a, y, b, y)
					if got := u.selection.text(); got != tc.want {
						t.Fatalf("selected=%q", got)
					}
					u.selectionAction(action)
					if action == 'c' {
						want := base64.StdEncoding.EncodeToString([]byte(tc.want))
						if !strings.Contains(u.clipboard, want) {
							t.Fatal(u.clipboard)
						}
					} else if len(u.main.selections) != 1 || u.main.selections[0].text != tc.want {
						t.Fatalf("reference=%+v", u.main.selections)
					}
				}
			}
		}
	}
}

func TestTerminalSourceSelectionCodeLiteralDecoration(t *testing.T) {
	p := activityui.Painter{CopySource: true}
	raw := p.Markdown("```\n│ literal  \n\n\n  tail\n```", 40)
	rows := make([]string, len(raw))
	meta := make([][]activityui.CopySpan, len(raw))
	for i, row := range raw {
		rows[i], meta[i] = activityui.ExtractCopy(row)
	}
	u := selectionTestUI(rows...)
	u.paintedCopy = append(meta, nil)
	selectionTestDrag(t, u, 0, 0, 39, len(rows)-1)
	if got := u.selection.text(); got != "│ literal  \n\n\n  tail" {
		t.Fatalf("copy=%q", got)
	}
	left, _ := u.selection.bounds(0)
	if left != 1 {
		t.Fatalf("literal code gutter stripped: %d", left)
	}
}

func TestTerminalBTWSourceCopyAndEscape(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.btw = &appServerBTW{question: "side question", status: "completed", answer: []btwAnswer{{text: "**side answer**"}}}
	var wire bytes.Buffer
	if err := u.paint(&wire, 80, 30); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "mekugi-copy") {
		t.Fatal("private source transport escaped")
	}
	rect := u.btw.rect
	rect.x += u.shell.layout.codex.x
	rect.y += u.shell.layout.codex.y
	y := -1
	x := -1
	for row := rect.y; row < rect.y+rect.h; row++ {
		for _, span := range u.shell.paintedCopy[row] {
			if span.Text == "side answer" {
				x, y = span.Column, row
				break
			}
		}
	}

	if y < 0 {
		t.Fatalf("no dock: %+v", rect)
	}
	selectionTestDrag(t, u.shell, x, y, x+10, y)
	if got := u.shell.selection.text(); got != "**side answer**" {
		t.Fatalf("btw copied=%q", got)
	}
	if err := u.shell.send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.btw == nil || u.shell.selection != nil {
		t.Fatal("Escape closed dock before selection")
	}
	selectionTestDrag(t, u.shell, x, y, x+10, y)
	u.shell.selectionAction('c')
	if !strings.Contains(u.shell.clipboard, base64.StdEncoding.EncodeToString([]byte("**side answer**"))) {
		t.Fatal("btw clipboard missing")
	}
}

func TestAppServerBTWShimmerElapsed(t *testing.T) {
	u, _ := newAppServerTestUI()
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	now := start.Add(75 * time.Second)
	u.clock = func() time.Time { return now }
	u.btw = &appServerBTW{busy: true, status: "Answering", started: start}
	a := u.btwRows(80, 8)[0]
	now = now.Add(100 * time.Millisecond)
	b := u.btwRows(80, 8)[0]
	if a == b || ansi.Strip(a) != ansi.Strip(b) || !strings.Contains(ansi.Strip(a), "Answering · 1m15s") || !u.sessionAnimating() {
		t.Fatalf("status=%q / %q", a, b)
	}
	u.btw.busy = false
	u.btw.status = "completed"
	if strings.Contains(ansi.Strip(u.btwRows(80, 8)[0]), "Answering") {
		t.Fatal("completed answer still shimmers")
	}
}

func TestUISnapshotBTWCopyDock(t *testing.T) {
	u, _ := newAppServerTestUI()
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return start.Add(75 * time.Second) }
	u.view.painter.Theme = livediff.DarkTheme
	u.btw = &appServerBTW{busy: true, status: "Answering", started: start, question: "Which option?", answer: []btwAnswer{{text: "Name | Value\n--- | ---\n**alpha** | `x`"}}}
	uisnapshot.Assert(t, "testdata/snapshots/btw-copy-dock.txt", strings.Join(u.btwRows(60, 12), "\n")+"\n")
}

func TestTerminalSourceScrolledMainReply(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	// Scroll back to the original reply above a long trailing tool run.
	u.turn = "running"
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "**original reply**", Observed: time.Now()}, {Seq: 2, Agent: "Main", Kind: "tool", Text: "Run: go test\n" + strings.Repeat("output\n", 100), Observed: time.Now()}}})
	u.view.following, u.view.offset = false, 0
	screen := vt.NewEmulator(80, 30)
	defer screen.Close()
	if err := u.paint(screen, 80, 30); err != nil {
		t.Fatal(err)
	}
	x, y := -1, -1
	for row, spans := range u.shell.paintedCopy {
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
	selectionTestDrag(t, u.shell, u.shell.layout.codex.x, y-1, x+13, y)
	if got := u.shell.selection.text(); got != "**original reply**" {
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
				t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
				view := u.view
				if activity {
					view = u.agents
					u.agents.agents = []activityPaneAgent{{Name: "/root/worker", Final: true}}
					u.shell.diffOpen, u.shell.journalOpen = false, false
				}
				view.applyAppServerItem(true, "", "main", "main", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: source})
				var wire bytes.Buffer
				if err := u.paint(&wire, width, 50); err != nil {
					t.Fatal(err)
				}
				first, last, left, right := -1, -1, 0, 0
				for y, spans := range u.shell.paintedCopy {
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
				selectionTestDrag(t, u.shell, ax, ay, bx, by)
				expected := want
				if activity {
					expected = strings.TrimSuffix(want, "\n| 猫 |  |")
				} // Activity exposes only its five-row excerpt.
				if got := u.shell.selection.text(); got != expected {
					t.Fatalf("activity=%v width=%d reverse=%v: %q want %q", activity, width, reverse, got, expected)
				}
			}
		}
	}
}

func TestTerminalSourceActivityCodeTabs(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.agents.agents = []activityPaneAgent{{Name: "/root/worker", Final: true}}
	u.shell.diffOpen, u.shell.journalOpen = false, false
	u.agents.applyAppServerItem(true, "", "main", "main", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "```\n\tx := 1  \n```"})
	var wire bytes.Buffer
	if err := u.paint(&wire, 120, 40); err != nil {
		t.Fatal(err)
	}
	for y, spans := range u.shell.paintedCopy {
		for _, span := range spans {
			if span.Code {
				selectionTestDrag(t, u.shell, span.Column, y, span.Column+span.Width-1, y)
				if got := u.shell.selection.text(); got != "\tx := 1  " {
					t.Fatalf("activity source code=%q", got)
				}
				return
			}
		}
	}
	t.Fatal("no Activity code source")
}
