package router

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func previewViewFixture(id string, rows int) diffview.Preview {
	var diff strings.Builder
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&diff, "+stream_%04d\n", i)
	}
	return diffview.Preview{ID: id, Workspace: "/workspace", Thread: "thread",
		Input: diff.String()}
}

func TestLiveDiffPreviewPaneFollowAndLifecycle(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	for _, size := range []int{2, 30, 300, 2000} {
		pane.Update(previewViewFixture("one", size))
		lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
		if err != nil || len(lines) > 12 || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), fmt.Sprintf("+stream_%04d", size)) {
			t.Fatalf("stream tip %d escaped region: %v %q", size, err, lines)
		}
		if pane.Views["one"].Focus != size-1 {
			t.Fatalf("follow anchored to hunk start rather than tip: %d", pane.Views["one"].Focus)
		}
	}
	pane.Update(diffview.Preview{ID: "one"})
	lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	if err != nil || !strings.Contains(ansi.Strip(lines[0]), "○ edit") {
		t.Fatalf("missing completion hold: %v %q", err, lines)
	}
	// Completed input stays visible until a new call replaces it.
	pane.Update(previewViewFixture("two", 10))
	if len(pane.Order) != 1 || pane.Order[0] != "two" {
		t.Fatal("new call did not replace completed input")
	}
	pane.Update(diffview.Preview{ID: "two"})
	lines, err = pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	if err != nil || !strings.Contains(ansi.Strip(lines[0]), "○ edit") || !strings.Contains(strings.Join(lines, "\n"), "stream_0010") {
		t.Fatalf("completed stream did not persist: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewPaneFinishedCardPersistsUntilReplaced(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	pane.Update(previewViewFixture("done", 2))
	pane.Update(previewViewFixture("live", 2))
	pane.Update(diffview.Preview{ID: "done"})
	lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pane.Order, []string{"done", "live"}) || pane.Live() != 1 || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "○ edit") {
		t.Fatalf("finished card did not remain visible: %v %q", pane.Order, lines)
	}
	pane.Update(previewViewFixture("next", 2))
	if !slices.Equal(pane.Order, []string{"next", "live"}) {
		t.Fatalf("new call did not replace finished card: %v", pane.Order)
	}
}

func TestLiveDiffPreviewUpdatePreemptsDistantFrame(t *testing.T) {
	t.Parallel()
	c := newLiveDiffTerminalController(nil, "/workspace", os.Stdout)
	defer c.close()
	c.scope.Workspaces = map[string]map[string]bool{"/workspace": {"thread": true}}
	c.previewPane.Update(previewViewFixture("finished", 1))
	c.previewPane.Update(previewViewFixture("live", 1))
	c.previewPane.Update(diffview.Preview{ID: "finished"})
	c.previewFrame.Reset(time.Minute)
	c.previewFrameC = c.previewFrame.C
	c.previewFrameDue = time.Now().Add(time.Minute)
	preview := previewViewFixture("live", 2)
	if done, err := c.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview}); done || err != nil {
		t.Fatalf("preview event: done=%v err=%v", done, err)
	}
	if !c.dirty || c.previewFrameC != nil {
		t.Fatalf("already-paced input was delayed again: dirty=%v timer=%v", c.dirty, c.previewFrameC != nil)
	}
}

func TestLiveDiffFirstAndCompletedInputRedrawImmediately(t *testing.T) {
	t.Parallel()
	c := newLiveDiffTerminalController(nil, "/workspace", os.Stdout)
	defer c.close()
	c.scope.Workspaces = map[string]map[string]bool{"/workspace": {"thread": true}}
	c.dirty = false
	preview := previewViewFixture("first", 1)
	if _, err := c.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview}); err != nil || !c.dirty || c.previewFrameC != nil {
		t.Fatalf("first input waited for pacing timer: dirty=%v timer=%v err=%v", c.dirty, c.previewFrameC != nil, err)
	}
	c.dirty = false
	if _, err := c.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &diffview.Preview{ID: "first"}}); err != nil || !c.dirty || c.previewFrameC != nil {
		t.Fatalf("completion waited for pacing timer: dirty=%v timer=%v err=%v", c.dirty, c.previewFrameC != nil, err)
	}
}

func TestLiveDiffFinishedInputRemainsOnTerminalWhileWaiting(t *testing.T) {
	t.Parallel()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 12, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	c := newLiveDiffTerminalController(nil, "/workspace", slave)
	defer c.close()
	c.coverage = ""
	c.previewPane.Update(previewViewFixture("done", 2))
	c.previewPane.Update(diffview.Preview{ID: "done"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resizes := make(chan os.Signal, 1)
	frames := make(chan string, 2)
	go func() {
		var pending synchronizedFrameBuffer
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			if err != nil {
				return
			}
			pending.Append(buffer[:n])
			for {
				frame, ok := pending.Next()
				if !ok {
					break
				}
				frames <- string(frame)
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- c.run(ctx, nil, nil, resizes) }()
	for index := range 2 {
		select {
		case frame := <-frames:
			if !strings.Contains(ansi.Strip(frame), "○ edit") || !strings.Contains(frame, "+stream_0002") || strings.Contains(frame, "Waiting for live input") {
				t.Fatalf("waiting frame lost completed input: %q", frame)
			}
			if index == 0 {
				resizes <- os.Interrupt
			}
		case <-time.After(2 * time.Second):
			t.Fatal("terminal did not redraw after resize")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLiveDiffPreviewPaneLatestOnlyAndIndependent(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	for i := 1; i <= 500; i++ {
		pane.Update(previewViewFixture("one", i))
	}
	if pane.Views["one"].Source != nil || pane.Views["one"].Rendered.ID != "" {
		t.Fatal("queued snapshots performed rendering")
	}
	lines, err := pane.Render(t.Context(), "/workspace", livediff.LightTheme, 80, 10)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+stream_0500") {
		t.Fatalf("rendered stale queued snapshot: %v %q", err, lines)
	}
	source := pane.Views["one"].Source
	// Repaints (including captured-diff navigation) do not parse source again.
	_, err = pane.Render(t.Context(), "/workspace", livediff.LightTheme, 80, 10)
	if err != nil || &source[0] != &pane.Views["one"].Source[0] {
		t.Fatal("unchanged preview rebuilt its source")
	}
	pane.Update(previewViewFixture("two", 20))
	pane.Update(diffview.Preview{ID: "two"})
	if pane.Views["one"] == nil || pane.Views["one"].Complete {
		t.Fatal("one completed stream hid another active stream")
	}
}

func TestLiveDiffPreviewLayoutAndWrapping(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	preview := previewViewFixture("one", 1)
	preview.Input = strings.Repeat("界", 100) + "TIP\n"
	pane.Update(preview)
	for _, width := range []int{4, 10, 40, 120} {
		lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, width, 8)
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
	var pane diffview.PreviewPane
	pane.Update(previewViewFixture("one", 30))
	first, _ := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 80, 8)
	pane.Update(previewViewFixture("one", 30))
	second, _ := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 80, 8)
	if !slices.Equal(first, second) {
		t.Fatal("identical snapshot moved the viewport")
	}
}

func BenchmarkLiveDiffPreviewPaneFrame(b *testing.B) {
	preview := previewViewFixture("one", 2000)
	var pane diffview.PreviewPane
	base := preview.Input
	sequence := 0
	b.ReportAllocs()
	for b.Loop() {
		// Change the actual source so syntax work cannot hide behind cache hits.
		sequence++
		next := preview
		next.Input = strings.Replace(base, "stream_2000", fmt.Sprintf("stream_tip_%d", sequence), 1)
		pane.Update(next)
		if _, err := pane.Render(b.Context(), "/workspace", livediff.DarkTheme, 120, 28); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffConcurrentPreviewCards(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	first := previewViewFixture("first", 100)
	first.Caller = "/root/editor"
	second := previewViewFixture("second", 200)
	second.Caller = "/root/reviewer"
	second.Thread = "another-thread"
	pane.Update(first)
	pane.Update(second)
	check := func(height int) string {
		t.Helper()
		lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 100, height)
		if err != nil || len(lines) > height {
			t.Fatalf("render: %v %q", err, lines)
		}
		return ansi.Strip(strings.Join(lines, "\n"))
	}
	frame := check(12)
	for _, want := range []string{"editor · ◐ edit", "reviewer · ◐ edit", "stream_0100", "stream_0200"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing concurrent caller/source %q: %s", want, frame)
		}
	}
	if strings.Contains(frame, " · first") || strings.Contains(frame, " · second") {
		t.Fatalf("internal call identifiers leaked into headings: %s", frame)
	}
	// Bursts cannot change call order or replace another call's source cache.
	otherSource := pane.Views["second"].Source
	for i := 101; i <= 150; i++ {
		first = previewViewFixture("first", i)
		first.Caller = "/root/editor"
		pane.Update(first)
	}
	frame = check(12)
	if strings.Index(frame, "editor · ") > strings.Index(frame, "reviewer · ") ||
		!strings.Contains(frame, "stream_0150") || !strings.Contains(frame, "stream_0200") ||
		&otherSource[0] != &pane.Views["second"].Source[0] {
		t.Fatalf("delta reordered or replaced a concurrent view: %s", frame)
	}
	// Same-thread calls still have separate identities and completion states.
	second.Thread, second.Caller = first.Thread, first.Caller
	pane.Update(second)
	pane.Update(diffview.Preview{ID: "first"})
	frame = check(12)
	if !strings.Contains(frame, "editor · ○ edit") || !strings.Contains(frame, "editor · ◐ edit") {
		t.Fatalf("completion replaced another call: %s", frame)
	}
	if len(pane.Order) != 2 || !pane.Views["first"].Complete || pane.Views["second"].Complete {
		t.Fatal("completion did not retain each call independently")
	}
	third := previewViewFixture("third", 300)
	pane.Update(third)
	if frame := check(3); !strings.Contains(frame, "+1 more calls") {
		t.Fatalf("tiny pane silently hid a concurrent call: %s", frame)
	}
	if frame := check(12); !strings.Contains(frame, "stream_0200") || !strings.Contains(frame, "stream_0300") {
		t.Fatalf("resize did not restore concurrent tips: %s", frame)
	}
}

func TestLiveDiffPreviewCallerSafeAndBounded(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	preview := previewViewFixture("caller", 1)
	preview.Caller = "/root/\x1b[2J" + strings.Repeat("界", 80)
	pane.Update(preview)
	for _, width := range []int{1, 8, 40, 80} {
		lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, width, 5)
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
	var pane diffview.PreviewPane
	preview := previewViewFixture("caller", 1)
	preview.Caller = "/root/review_stock_preview"
	pane.Update(preview)
	lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	header := ansi.Strip(lines[0])
	if !strings.HasPrefix(header, "  review_stock_preview · ◐") || strings.Contains(header, "…") {
		t.Fatalf("caller truncated despite available width: %q", header)
	}
	preview.Status = diffview.PreviewUnavailable + strings.Repeat("reason ", 20)
	pane.Update(preview)
	lines, err = pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 60, 5)
	if err != nil {
		t.Fatal(err)
	}
	if header = ansi.Strip(lines[0]); !strings.HasPrefix(header, "  review_stock") {
		t.Fatalf("long status obscured caller: %q", header)
	}
}

func BenchmarkLiveDiffConcurrentPreviewFrame(b *testing.B) {
	var pane diffview.PreviewPane
	for i := range 16 {
		pane.Update(previewViewFixture(fmt.Sprint(i), 2000))
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := pane.Render(b.Context(), "/workspace", livediff.DarkTheme, 100, 28); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffPreviewTitleShowsFileStatusAndCounts(t *testing.T) {
	t.Parallel()
	modified := mekugi.RenderReviewFile("/workspace/a.go", "/workspace/a.go", "one\ntwo\n", "one\nTWO\nthree\n")
	added := mekugi.RenderReviewFile("", "/workspace/new.txt", "", "x\n")
	for _, tc := range []struct {
		name    string
		preview diffview.Preview
		want    string
	}{
		{"modified", diffview.Preview{Status: diffview.PreviewEdit, Files: []mekugi.ReviewFile{modified}}, "◐ M a.go +2 -1"},
		{"added", diffview.Preview{Status: diffview.PreviewEdit, Files: []mekugi.ReviewFile{added}}, "◐ A new.txt +1 -0"},
		{"several files", diffview.Preview{Status: diffview.PreviewEdit, Files: []mekugi.ReviewFile{modified, added}}, "◐ A new.txt +1 -0 2/2 files"},
		{"running", diffview.Preview{Status: diffview.PreviewRunning, Files: []mekugi.ReviewFile{modified}}, "◐ M a.go +2 -1 · observed so far"},
		{"pending", diffview.Preview{Status: diffview.PreviewPending, Input: "will restore (pending)\n/workspace/a.go"}, "◐ scoped effects"},
		{"unavailable", diffview.Preview{Status: diffview.PreviewUnavailable + "patch cannot be projected", Input: "\n"}, "! patch cannot be projected"},
		{"diff tail", diffview.Preview{Status: diffview.PreviewEdit, Input: "+x\n", DiffText: true, Truncated: true}, "◐ edit · tail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pane diffview.PreviewPane
			tc.preview.ID, tc.preview.Workspace, tc.preview.Thread = "one", "/workspace", "thread"
			pane.Update(tc.preview)
			lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 120, 8)
			if err != nil {
				t.Fatal(err)
			}
			if header := ansi.Strip(lines[0]); !strings.HasSuffix(header, "thread · "+tc.want) {
				t.Fatalf("header = %q, want suffix %q", header, tc.want)
			}
		})
	}
	var pane diffview.PreviewPane
	pane.Update(diffview.Preview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: diffview.PreviewEdit, Files: []mekugi.ReviewFile{modified}})
	pane.Update(diffview.Preview{ID: "one"})
	lines, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 120, 8)
	if err != nil || !strings.Contains(lines[0], activityui.Dim+"○") ||
		!strings.Contains(lines[0], livediff.DarkTheme.Foreground(chroma.GenericInserted)+"+2") {
		t.Fatalf("completed header lost its glyph or count colors: %v %q", err, lines[0])
	}
}

func TestLiveDiffPendingEditRemovesEmptyCard(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	pane.Update(diffview.Preview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: diffview.PreviewEdit, Input: "\n"})
	pane.Update(diffview.Preview{ID: "one", Workspace: "/workspace", Thread: "thread"})
	if len(pane.Views) != 0 || len(pane.Order) != 0 {
		t.Fatal("pending edit left an empty status card")
	}
	pane.Update(diffview.Preview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: diffview.PreviewEdit})
	if len(pane.Views) != 1 {
		t.Fatal("later projection did not restore the preview")
	}
}

func TestLiveDiffPreviewCardsKeepSlotsAndGutter(t *testing.T) {
	t.Parallel()
	var pane diffview.PreviewPane
	for _, call := range []struct{ id, caller string }{{"a1", "/root/a"}, {"b1", "/root/b"}, {"c1", "/root/c"}} {
		preview := previewViewFixture(call.id, 5)
		preview.Caller = call.caller
		pane.Update(preview)
	}
	render := func() {
		t.Helper()
		if _, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 100, 30); err != nil {
			t.Fatal(err)
		}
	}
	render()
	pane.Update(diffview.Preview{ID: "a1"})
	pane.Update(diffview.Preview{ID: "b1"})
	render()
	// b's next call reuses b's slot; a's finished card and c's live card stay put.
	next := previewViewFixture("b2", 5)
	next.Caller = "/root/b"
	pane.Update(next)
	if !slices.Equal(pane.Order, []string{"a1", "b2", "c1"}) {
		t.Fatalf("new call moved other cards: %v", pane.Order)
	}
	// Another caller takes the finished slot rather than appending.
	other := previewViewFixture("d1", 5)
	other.Caller = "/root/d"
	pane.Update(other)
	if !slices.Equal(pane.Order, []string{"d1", "b2", "c1"}) {
		t.Fatalf("new caller did not reuse a finished slot: %v", pane.Order)
	}
	// Finished cards remain until a new call takes their slot.
	pane.Update(diffview.Preview{ID: "c1"})
	render()
	pane.Update(diffview.Preview{ID: "d1"})
	render()
	pane.Update(previewViewFixture("e1", 5))
	if !slices.Equal(pane.Order, []string{"e1", "b2", "c1"}) {
		t.Fatalf("new call did not reuse the oldest finished slot: %v", pane.Order)
	}

	// Line numbers keep their width when the source shrinks back.
	var single diffview.PreviewPane
	single.Update(previewViewFixture("one", 100))
	if _, err := single.Render(t.Context(), "/workspace", livediff.DarkTheme, 100, 10); err != nil {
		t.Fatal(err)
	}
	single.Update(previewViewFixture("one", 99))
	lines, err := single.Render(t.Context(), "/workspace", livediff.DarkTheme, 100, 10)
	if err != nil || !strings.Contains(ansi.Strip(lines[len(lines)-1]), "  99│") {
		t.Fatalf("line-number gutter narrowed: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewPacerIsSteadyAndBounded(t *testing.T) {
	t.Parallel()
	var pacer liveDiffPreviewPacer
	input := ""
	shown := 0
	// Bursts every fourth frame reveal whole lines on most frames, never all at once.
	steps := 0
	for frame := range 40 {
		if frame%4 == 0 {
			input += strings.Repeat("界x", 6) + "\n" + strings.Repeat("y", 10) + "\n"
		}
		next := pacer.advance(input, 1, false, false)
		if next < shown || next > len(input) || next > 0 && input[next-1] != '\n' {
			t.Fatalf("frame %d revealed %d of %d after %d", frame, next, len(input), shown)
		}
		if frame > 8 && frame%4 == 0 && next == len(input) {
			t.Fatalf("frame %d did not pace the burst: %d -> %d of %d", frame, shown, next, len(input))
		}
		if next > shown {
			steps++
		}
		shown = next
	}
	if steps < 16 {
		t.Fatalf("bursts were not revealed line by line: %d steps", steps)
	}
	// A large backlog skips to the window at the tip.
	input += strings.Repeat("y\n", 32<<10)
	if next := pacer.advance(input, 1, false, false); next < len(input)-liveDiffPreviewMaxLag {
		t.Fatalf("lag exceeded its window: %d of %d", next, len(input))
	}
	// A finished call converges promptly, including an unterminated line.
	input += "tail"
	for range 20 {
		shown = pacer.advance(input, 1, true, false)
	}
	if shown != len(input) {
		t.Fatalf("final input not reached: %d of %d", shown, len(input))
	}
}

func TestLiveDiffPreviewPacerKeepsUpAcrossHeldFrames(t *testing.T) {
	t.Parallel()
	// A reveal held between target units spans several frames. The pacer must
	// cover what arrived meanwhile rather than one unit per call, or the
	// backlog grows until completion releases it at once.
	var pacer liveDiffPreviewPacer
	line := strings.Repeat("x", 39) + "\n"
	input := ""
	for step := range 40 {
		input += strings.Repeat(line, 5)
		shown := pacer.advance(input, 8, false, false)
		if step > 8 && len(input)-shown > 10*len(line) {
			t.Fatalf("step %d fell behind: %d of %d", step, shown, len(input))
		}
	}
	// Idle frames are not arrival time: a burst after a stall is still paced.
	pacer = liveDiffPreviewPacer{}
	burst := strings.Repeat(line, 40) // Within the lag window.
	if shown := pacer.advance(burst, 100, false, false); shown == 0 || shown > len(burst)/4 {
		t.Fatalf("burst after idle was not paced: %d of %d", shown, len(burst))
	}
}

func TestLiveDiffPreviewPacerBuffersUnits(t *testing.T) {
	t.Parallel()
	// The pacer offers candidate boundaries; decoded target syntax decides
	// which candidates may actually be displayed.
	reveal := func(input string, encoded bool, step int) []string {
		var pacer liveDiffPreviewPacer
		var shown []string
		for i := min(step, len(input)); ; i = min(i+step, len(input)) {
			if next := pacer.advance(input[:i], 1, false, encoded); len(shown) == 0 && next > 0 || len(shown) > 0 && input[:next] != shown[len(shown)-1] {
				shown = append(shown, input[:next])
			}
			if i == len(input) {
				return shown
			}
		}
	}
	// Semicolons are candidates even inside a for header. The source gate
	// must reject these, rather than making transport pacing language-aware.
	body := "for (i = 0; i < n; i++) {\n\tx();\n"
	if got := reveal(body, false, 3); !slices.Equal(got, []string{"for (i = 0;", "for (i = 0; i < n;", "for (i = 0; i < n; i++) {\n", strings.TrimSuffix(body, "\n")}) {
		t.Fatalf("lines: %q", got)
	}
	// Nested escapes are also candidates; projection decodes their meaning.
	encoded := `{"cmd":"cat > f <<'EOF'\nsay \\n here\nnext`
	if got := reveal(encoded, true, 3); !slices.Equal(got, []string{
		`{"cmd":"cat > f <<'EOF'\n`, `{"cmd":"cat > f <<'EOF'\nsay \\n`, `{"cmd":"cat > f <<'EOF'\nsay \\n here\n`,
	}) {
		t.Fatalf("encoded: %q", got)
	}
	// A stalled line stays buffered regardless of elapsed frames.
	var pacer liveDiffPreviewPacer
	long := strings.Repeat("z", 64)
	shown := 0
	for range 100 {
		shown = pacer.advance(long, 1, false, false)
	}
	if shown != 0 {
		t.Fatalf("unfinished line was released: %d of %d", shown, len(long))
	}
}

func TestLiveDiffPreviewBirthsCarryAcrossSnapshots(t *testing.T) {
	t.Parallel()
	rows := func(texts ...string) []diffview.PreviewRow {
		var out []diffview.PreviewRow
		for i, text := range texts {
			out = append(out, diffview.PreviewRow{i + 1, ' ', text})
		}
		return out
	}
	start := time.Unix(100, 0)
	before := rows("a\n", "b\n", "par")
	born := diffview.PreviewBirths(nil, before, nil, start)
	// A first display cascades, bounded by the maximum stagger.
	if !born[0].Equal(start) || !born[1].After(born[0]) || born[2].Sub(start) > diffview.PreviewMaxStagger {
		t.Fatalf("first cascade: %v", born)
	}
	later := start.Add(time.Second)
	after := rows("a\n", "b\n", "partial\n", "c\n")
	next := diffview.PreviewBirths(before, after, born, later)
	// Unchanged and grown rows keep their times; only the new row fades.
	if !slices.Equal(next[:3], born) || !next[3].Equal(later) {
		t.Fatalf("carried births: %v from %v", next, born)
	}
	// A clipped tail slides and renumbers without refading its rows.
	slid := rows("b\n", "partial\n", "c\n", "d\n")
	shifted := diffview.PreviewBirths(after, slid, next, later.Add(time.Second))
	if !slices.Equal(shifted[:3], next[1:]) || !shifted[3].Equal(later.Add(time.Second)) {
		t.Fatalf("sliding tail: %v from %v", shifted, next)
	}
	// Context renumbered under an inserted row keeps its time.
	renumbered := slices.Insert(slices.Clone(after), 1, diffview.PreviewRow{2, '+', "new\n"})
	for i := 2; i < len(renumbered); i++ {
		renumbered[i].Number++
	}
	inserted := diffview.PreviewBirths(after, renumbered, next, later.Add(2*time.Second))
	if !inserted[0].Equal(next[0]) || !inserted[1].Equal(later.Add(2*time.Second)) || !slices.Equal(inserted[2:], next[1:]) {
		t.Fatalf("renumbered context: %v from %v", inserted, next)
	}
}

func TestLiveDiffPreviewCompletionDoesNotFadeFinalSnapshot(t *testing.T) {
	t.Parallel()
	start := time.Unix(100, 0)
	view := diffview.PreviewView{Current: previewViewFixture("done", 2)}
	motion := diffview.PreviewMotion{Enabled: true, Canvas: livediff.DarkTheme.Canvas(), Now: start}
	streaming, err := view.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.Enabled = false
	settled, err := view.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil || slices.Equal(streaming, settled) {
		t.Fatalf("active input did not fade: %v %q", err, streaming)
	}

	view.Current = previewViewFixture("done", 3)
	view.Current.Complete = true
	view.Complete = true
	motion.Enabled = true
	motion.Now = start.Add(10 * time.Millisecond)
	completed, err := view.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.Enabled = false
	plain, err := view.Render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil || !slices.Equal(completed, plain) || !view.Fading.IsZero() {
		t.Fatalf("completed preview remained dimmed: %v %q vs %q", err, completed, plain)
	}
}

func TestLiveDiffPreviewPacerKeepsEscapesWhole(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  int
	}{
		{`"a\`, 2}, {`"a\\`, 4}, {`"a\u00`, 2}, {`"a\u003e`, 8}, {`"a\x4`, 2}, {`"a\n`, 4},
	} {
		if got := liveDiffEscapeEnd(test.input, len(test.input)); got != test.want {
			t.Errorf("%q: cut at %d, want %d", test.input, got, test.want)
		}
	}
	var pacer liveDiffPreviewPacer
	input := `"a\`
	// An unfinished encoded line stays buffered.
	shown := 0
	for range 100 {
		shown = pacer.advance(input, 1, false, true)
	}
	if shown != 0 {
		t.Fatalf("streaming reveal split an escape at %d", shown)
	}
	// Finished input is shown whole even when it ends in a backslash.
	for range 3 {
		pacer.advance(input, 1, true, true)
	}
	if pacer.shown != len(input) {
		t.Fatalf("finished input stalled at %d of %d", pacer.shown, len(input))
	}
}

func TestLiveDiffPreviewFadesOnlyChangedText(t *testing.T) {
	t.Parallel()
	path := "/workspace/a.txt"
	view := diffview.PreviewView{Current: diffview.Preview{ID: "fade", Workspace: "/workspace", Thread: "thread", Status: diffview.PreviewEdit,
		Files: []mekugi.ReviewFile{mekugi.RenderReviewFile(path, path, "keep\nold\n", "keep\nnew\n")}}}
	motion := diffview.PreviewMotion{Enabled: true, Canvas: livediff.DarkTheme.Canvas(), Now: time.Now()}
	fading, err := view.Render(t.Context(), "/workspace", livediff.DarkTheme, 60, 6, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.Enabled = false
	settled, err := view.Render(t.Context(), "/workspace", livediff.DarkTheme, 60, 6, motion)
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
			fill := livediff.DarkTheme.RowBackground(kind)
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
