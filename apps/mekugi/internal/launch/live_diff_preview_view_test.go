package launch

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

func previewViewFixture(id string, rows int) previewmodel.Preview {
	var diff strings.Builder
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&diff, "+stream_%04d\n", i)
	}
	return previewmodel.Preview{ID: id, Workspace: "/workspace", Thread: "thread",
		Input: diff.String()}
}

func TestLiveDiffPreviewRepaintReusesSource(t *testing.T) {
	t.Parallel()
	view := previewview.PreviewView{Current: previewViewFixture("one", 500)}
	lines, err := view.Render(t.Context(), "/workspace", diffrender.LightTheme, 80, 10, previewview.PreviewMotion{})
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+stream_0500") {
		t.Fatalf("rendered stale snapshot: %v %q", err, lines)
	}
	source := view.Source
	// Repaints (including captured-diff navigation) do not parse source again.
	_, err = view.Render(t.Context(), "/workspace", diffrender.LightTheme, 80, 10, previewview.PreviewMotion{})
	if err != nil || &source[0] != &view.Source[0] {
		t.Fatal("unchanged preview rebuilt its source")
	}
}

func TestLiveDiffPreviewLayoutAndWrapping(t *testing.T) {
	t.Parallel()
	var view previewview.PreviewView
	preview := previewViewFixture("one", 1)
	preview.Input = strings.Repeat("界", 100) + "TIP\n"
	view.Current = preview
	for _, width := range []int{4, 10, 40, 120} {
		lines, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, width, 8, previewview.PreviewMotion{})
		if err != nil || len(lines) > 8 {
			t.Fatalf("render at width %d: %v", width, err)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > width-1 {
				t.Fatalf("line escaped width %d: %q", width, line)
			}
		}
		if width >= 10 && !strings.Contains(ansi.Strip(strings.Join(lines, "")), "TIP") {
			t.Fatalf("wrapped stream tip lost at width %d: %q", width, lines)
		}
	}
}

func TestLiveDiffPreviewRepeatedSnapshotKeepsFocus(t *testing.T) {
	t.Parallel()
	// A repeated snapshot must not move focus, because prepare reuses it.
	var view previewview.PreviewView
	view.Current = previewViewFixture("one", 30)
	first, _ := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 80, 8, previewview.PreviewMotion{})
	view.Current = previewViewFixture("one", 30)
	second, _ := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 80, 8, previewview.PreviewMotion{})
	if !slices.Equal(first, second) {
		t.Fatal("identical snapshot moved the viewport")
	}
}

func BenchmarkLiveDiffPreviewPaneFrame(b *testing.B) {
	preview := previewViewFixture("one", 2000)
	var view previewview.PreviewView
	base := preview.Input
	sequence := 0
	b.ReportAllocs()
	for b.Loop() {
		// Change the actual source so syntax work cannot hide behind cache hits.
		sequence++
		next := preview
		next.Input = strings.Replace(base, "stream_2000", fmt.Sprintf("stream_tip_%d", sequence), 1)
		view.Current = next
		if _, err := view.Render(b.Context(), "/workspace", diffrender.DarkTheme, 120, 28, previewview.PreviewMotion{}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffPreviewCallerSafeAndBounded(t *testing.T) {
	t.Parallel()
	var view previewview.PreviewView
	preview := previewViewFixture("caller", 1)
	preview.Caller = "/root/\x1b[2J" + strings.Repeat("界", 80)
	view.Current = preview
	for _, width := range []int{1, 8, 40, 80} {
		lines, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, width, 5, previewview.PreviewMotion{})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			if strings.Contains(line, "\x1b[2J") || ansi.StringWidth(line) > width-1 {
				t.Fatalf("unsafe or unbounded caller: %q", line)
			}
		}
	}
}

func TestLiveDiffPreviewCallerUsesAvailableWidth(t *testing.T) {
	t.Parallel()
	var view previewview.PreviewView
	preview := previewViewFixture("caller", 1)
	preview.Caller = "/root/review_stock_preview"
	view.Current = preview
	lines, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 100, 5, previewview.PreviewMotion{})
	if err != nil {
		t.Fatal(err)
	}
	header := ansi.Strip(lines[0])
	if !strings.HasPrefix(header, "  review_stock_preview · ◐") || strings.Contains(header, "…") {
		t.Fatalf("caller truncated despite available width: %q", header)
	}
	preview.Status = previewmodel.PreviewUnavailable + strings.Repeat("reason ", 20)
	view.Current = preview
	lines, err = view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 60, 5, previewview.PreviewMotion{})
	if err != nil {
		t.Fatal(err)
	}
	if header = ansi.Strip(lines[0]); !strings.HasPrefix(header, "  review_stock") {
		t.Fatalf("long status obscured caller: %q", header)
	}
}

func BenchmarkLiveDiffConcurrentPreviewFrame(b *testing.B) {
	var pane previewview.PreviewPane
	for i := range 16 {
		pane.Update(previewViewFixture(fmt.Sprint(i), 2000), time.Now())
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := pane.RenderBatch(b.Context(), "", "/workspace", diffrender.DarkTheme, 100, 31, 15); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffPreviewTitleShowsFileStatusAndCounts(t *testing.T) {
	t.Parallel()
	modified := diffmodel.RenderReviewFile("/workspace/a.go", "/workspace/a.go", "one\ntwo\n", "one\nTWO\nthree\n")
	added := diffmodel.RenderReviewFile("", "/workspace/new.txt", "", "x\n")
	for _, tc := range []struct {
		name    string
		preview previewmodel.Preview
		want    string
	}{
		{"modified", previewmodel.Preview{Status: previewmodel.PreviewEdit, Files: []diffmodel.ReviewFile{modified}}, "◐ M a.go +2 -1"},
		{"added", previewmodel.Preview{Status: previewmodel.PreviewEdit, Files: []diffmodel.ReviewFile{added}}, "◐ A new.txt +1 -0"},
		{"running", previewmodel.Preview{Status: previewmodel.PreviewRunning, Files: []diffmodel.ReviewFile{modified}}, "◐ M a.go +2 -1 · observed so far"},
		{"pending", previewmodel.Preview{Status: previewmodel.PreviewPending, Input: "will restore (pending)\n/workspace/a.go"}, "◐ scoped effects"},
		{"unavailable", previewmodel.Preview{Status: previewmodel.PreviewUnavailable + "patch cannot be projected", Input: "\n"}, "! patch cannot be projected"},
		{"diff tail", previewmodel.Preview{Status: previewmodel.PreviewEdit, Input: "+x\n", DiffText: true, Truncated: true}, "◐ edit · tail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var view previewview.PreviewView
			tc.preview.ID, tc.preview.Workspace, tc.preview.Thread = "one", "/workspace", "thread"
			view.Current = tc.preview
			lines, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 120, 8, previewview.PreviewMotion{})
			if err != nil {
				t.Fatal(err)
			}
			if header := ansi.Strip(lines[0]); !strings.HasSuffix(header, "thread · "+tc.want) {
				t.Fatalf("header = %q, want suffix %q", header, tc.want)
			}
		})
	}
	var view previewview.PreviewView
	view.Current = previewmodel.Preview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: previewmodel.PreviewEdit, Files: []diffmodel.ReviewFile{modified}}
	view.Complete = true
	lines, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 120, 8, previewview.PreviewMotion{})
	if err != nil || !strings.Contains(lines[0], terminalui.Dim+"○") ||
		!strings.Contains(lines[0], diffrender.DarkTheme.Foreground(chroma.GenericInserted)+"+2") {
		t.Fatalf("completed header lost its glyph or count colors: %v %q", err, lines[0])
	}
}

func TestLiveDiffPendingEditRemovesEmptyCard(t *testing.T) {
	t.Parallel()
	var pane previewview.PreviewPane
	pane.Update(previewmodel.Preview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: previewmodel.PreviewEdit, Input: "\n"}, time.Now())
	pane.Update(previewmodel.Preview{ID: "one", Workspace: "/workspace", Thread: "thread"}, time.Now())
	if len(pane.Views) != 0 || len(pane.Order) != 0 {
		t.Fatal("pending edit left an empty status card")
	}
	pane.Update(previewmodel.Preview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: previewmodel.PreviewEdit}, time.Now())
	if len(pane.Views) != 1 {
		t.Fatal("later projection did not restore the preview")
	}
}

func TestLiveDiffPreviewKeepsGutterWidth(t *testing.T) {
	t.Parallel()
	view := previewview.PreviewView{Current: previewViewFixture("one", 100)}
	if _, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 100, 10, previewview.PreviewMotion{}); err != nil {
		t.Fatal(err)
	}
	view.Current = previewViewFixture("one", 99)
	lines, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 100, 10, previewview.PreviewMotion{})
	if err != nil || !strings.Contains(ansi.Strip(lines[len(lines)-1]), "  99│") {
		t.Fatalf("line-number gutter narrowed: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewBirthsCarryAcrossSnapshots(t *testing.T) {
	t.Parallel()
	rows := func(texts ...string) []previewview.PreviewRow {
		var out []previewview.PreviewRow
		for i, text := range texts {
			out = append(out, previewview.PreviewRow{Number: i + 1, Kind: ' ', Text: text})
		}
		return out
	}
	start := time.Unix(100, 0)
	before := rows("a\n", "b\n", "par")
	born := previewview.PreviewBirths(nil, before, nil, start)
	// A first display cascades, bounded by the maximum stagger.
	if !born[0].Equal(start) || !born[1].After(born[0]) || born[2].Sub(start) > previewview.PreviewMaxStagger {
		t.Fatalf("first cascade: %v", born)
	}
	later := start.Add(time.Second)
	after := rows("a\n", "b\n", "partial\n", "c\n")
	next := previewview.PreviewBirths(before, after, born, later)
	// Unchanged and grown rows keep their times; only the new row fades.
	if !slices.Equal(next[:3], born) || !next[3].Equal(later) {
		t.Fatalf("carried births: %v from %v", next, born)
	}
	// A clipped tail slides and renumbers without refading its rows.
	slid := rows("b\n", "partial\n", "c\n", "d\n")
	shifted := previewview.PreviewBirths(after, slid, next, later.Add(time.Second))
	if !slices.Equal(shifted[:3], next[1:]) || !shifted[3].Equal(later.Add(time.Second)) {
		t.Fatalf("sliding tail: %v from %v", shifted, next)
	}
	// Context renumbered under an inserted row keeps its time.
	renumbered := slices.Insert(slices.Clone(after), 1, previewview.PreviewRow{Number: 2, Kind: '+', Text: "new\n"})
	for i := 2; i < len(renumbered); i++ {
		renumbered[i].Number++
	}
	inserted := previewview.PreviewBirths(after, renumbered, next, later.Add(2*time.Second))
	if !inserted[0].Equal(next[0]) || !inserted[1].Equal(later.Add(2*time.Second)) || !slices.Equal(inserted[2:], next[1:]) {
		t.Fatalf("renumbered context: %v from %v", inserted, next)
	}
}

func TestLiveDiffPreviewCompletionDoesNotFadeFinalSnapshot(t *testing.T) {
	t.Parallel()
	start := time.Unix(100, 0)
	view := previewview.PreviewView{Current: previewViewFixture("done", 2)}
	motion := previewview.PreviewMotion{Enabled: true, Canvas: diffrender.DarkTheme.Canvas(), Now: start}
	streaming, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 70, 8, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.Enabled = false
	settled, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 70, 8, motion)
	if err != nil || slices.Equal(streaming, settled) {
		t.Fatalf("active input did not fade: %v %q", err, streaming)
	}

	view.Current = previewViewFixture("done", 3)
	view.Current.Complete = true
	view.Complete = true
	motion.Enabled = true
	motion.Now = start.Add(10 * time.Millisecond)
	completed, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 70, 8, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.Enabled = false
	plain, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 70, 8, motion)
	if err != nil || !slices.Equal(completed, plain) {
		t.Fatalf("completed preview remained dimmed: %v %q vs %q", err, completed, plain)
	}
}

func TestLiveDiffPreviewFadesOnlyChangedText(t *testing.T) {
	t.Parallel()
	path := "/workspace/a.txt"
	view := previewview.PreviewView{Current: previewmodel.Preview{ID: "fade", Workspace: "/workspace", Thread: "thread", Status: previewmodel.PreviewEdit,
		Files: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "keep\nold\n", "keep\nnew\n")}}}
	motion := previewview.PreviewMotion{Enabled: true, Canvas: diffrender.DarkTheme.Canvas(), Now: time.Now()}
	fading, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 60, 6, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.Enabled = false
	settled, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 60, 6, motion)
	if err != nil || len(fading) != len(settled) {
		t.Fatalf("renders: %v %q %q", err, fading, settled)
	}
	changed := 0
	for i := range settled {
		plain := ansi.Strip(settled[i])
		switch {
		case strings.Contains(plain, "keep"):
			if fading[i] != settled[i] {
				t.Fatalf("context row faded: %q", fading[i])
			}
		case strings.Contains(plain, "+new"), strings.Contains(plain, "-old"):
			changed++
			kind := byte('+')
			if strings.Contains(plain, "-old") {
				kind = '-'
			}
			fill := diffrender.DarkTheme.RowBackground(kind)
			if fading[i] == settled[i] || ansi.Strip(fading[i]) != plain || strings.Count(fading[i], fill) != strings.Count(settled[i], fill) ||
				strings.Count(fading[i], "\x1b[48;2;") != strings.Count(settled[i], "\x1b[48;2;") {
				t.Fatalf("changed row did not fade its text only:\n%q\n%q", fading[i], settled[i])
			}
			// The line number and diff marker appear settled, before the text.
			marker := strings.Index(settled[i], string(kind))
			if marker < 0 || fading[i][:marker+1] != settled[i][:marker+1] {
				t.Fatalf("changed row chrome faded:\n%q\n%q", fading[i], settled[i])
			}
		}
	}
	if changed != 2 {
		t.Fatalf("missing changed rows: %q", settled)
	}
}
