package router

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

func previewViewFixture(id string, rows int) liveDiffPreview {
	var diff strings.Builder
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&diff, "+stream_%04d\n", i)
	}
	return liveDiffPreview{ID: id, Workspace: "/workspace", Thread: "thread",
		Input: diff.String()}
}

func TestLiveDiffPreviewPaneFollowAndLifecycle(t *testing.T) {
	var pane liveDiffPreviewPane
	for _, size := range []int{2, 30, 300, 2000} {
		pane.update(previewViewFixture("one", size))
		lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
		if err != nil || len(lines) > 12 || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), fmt.Sprintf("+stream_%04d", size)) {
			t.Fatalf("stream tip %d escaped region: %v %q", size, err, lines)
		}
		if pane.views["one"].focus != size-1 {
			t.Fatalf("follow anchored to hunk start rather than tip: %d", pane.views["one"].focus)
		}
	}
	pane.update(liveDiffPreview{ID: "one"})
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	if err != nil || !strings.Contains(ansi.Strip(lines[0]), "✓ edit") {
		t.Fatalf("missing completion hold: %v %q", err, lines)
	}
	// Completed input stays visible until a new call replaces it.
	pane.update(previewViewFixture("two", 10))
	if len(pane.order) != 1 || pane.order[0] != "two" {
		t.Fatal("new call did not replace completed input")
	}
	pane.update(liveDiffPreview{ID: "two"})
	lines, err = pane.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	if err != nil || !strings.Contains(ansi.Strip(lines[0]), "✓ edit") || !strings.Contains(strings.Join(lines, "\n"), "stream_0010") {
		t.Fatalf("completed stream did not persist: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewPaneFinishedCardPersistsUntilReplaced(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(previewViewFixture("done", 2))
	pane.update(previewViewFixture("live", 2))
	pane.update(liveDiffPreview{ID: "done"})
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pane.order, []string{"done", "live"}) || pane.live() != 1 || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "✓ edit") {
		t.Fatalf("finished card did not remain visible: %v %q", pane.order, lines)
	}
	pane.update(previewViewFixture("next", 2))
	if !slices.Equal(pane.order, []string{"next", "live"}) {
		t.Fatalf("new call did not replace finished card: %v", pane.order)
	}
}

func TestLiveDiffPreviewUpdatePreemptsDistantFrame(t *testing.T) {
	c := newLiveDiffTerminalController(nil, "/workspace", os.Stdout)
	defer c.close()
	c.scope.Workspaces = map[string]map[string]bool{"/workspace": {"thread": true}}
	c.previewPane.update(previewViewFixture("finished", 1))
	c.previewPane.update(previewViewFixture("live", 1))
	c.previewPane.update(liveDiffPreview{ID: "finished"})
	c.previewFrame.Reset(time.Minute)
	c.previewFrameC = c.previewFrame.C
	c.previewFrameDue = time.Now().Add(time.Minute)
	preview := previewViewFixture("live", 2)
	if done, err := c.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview}); done || err != nil {
		t.Fatalf("preview event: done=%v err=%v", done, err)
	}
	if delay := time.Until(c.previewFrameDue); delay <= 0 || delay > time.Second {
		t.Fatalf("fresh input waited for old expiry: %v", delay)
	}
}

func TestLiveDiffFirstAndCompletedInputRedrawImmediately(t *testing.T) {
	c := newLiveDiffTerminalController(nil, "/workspace", os.Stdout)
	defer c.close()
	c.scope.Workspaces = map[string]map[string]bool{"/workspace": {"thread": true}}
	c.dirty = false
	preview := previewViewFixture("first", 1)
	if _, err := c.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview}); err != nil || !c.dirty || c.previewFrameC != nil {
		t.Fatalf("first input waited for pacing timer: dirty=%v timer=%v err=%v", c.dirty, c.previewFrameC != nil, err)
	}
	c.dirty = false
	if _, err := c.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &liveDiffPreview{ID: "first"}}); err != nil || !c.dirty || c.previewFrameC != nil {
		t.Fatalf("completion waited for pacing timer: dirty=%v timer=%v err=%v", c.dirty, c.previewFrameC != nil, err)
	}
}

func TestLiveDiffFinishedInputRemainsOnTerminalWhileWaiting(t *testing.T) {
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
	c.previewPane.update(previewViewFixture("done", 2))
	c.previewPane.update(liveDiffPreview{ID: "done"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resizes := make(chan os.Signal, 1)
	frames := make(chan string, 2)
	go func() {
		var pending []byte
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			if err != nil {
				return
			}
			pending = append(pending, buffer[:n]...)
			for {
				end := bytes.Index(pending, []byte("\x1b[?2026l"))
				if end < 0 {
					break
				}
				end += len("\x1b[?2026l")
				frames <- string(pending[:end])
				pending = pending[end:]
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- c.run(ctx, nil, nil, resizes) }()
	for index := range 2 {
		select {
		case frame := <-frames:
			if !strings.Contains(ansi.Strip(frame), "✓ edit") || !strings.Contains(frame, "+stream_0002") || strings.Contains(frame, "Waiting for live input") {
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
	var pane liveDiffPreviewPane
	for i := 1; i <= 500; i++ {
		pane.update(previewViewFixture("one", i))
	}
	if pane.views["one"].source != nil || pane.views["one"].rendered.ID != "" {
		t.Fatal("queued snapshots performed rendering")
	}
	lines, err := pane.render(t.Context(), "/workspace", livediff.LightTheme, 80, 10)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+stream_0500") {
		t.Fatalf("rendered stale queued snapshot: %v %q", err, lines)
	}
	source := pane.views["one"].source
	// Repaints (including captured-diff navigation) do not parse source again.
	_, err = pane.render(t.Context(), "/workspace", livediff.LightTheme, 80, 10)
	if err != nil || &source[0] != &pane.views["one"].source[0] {
		t.Fatal("unchanged preview rebuilt its source")
	}
	pane.update(previewViewFixture("two", 20))
	pane.update(liveDiffPreview{ID: "two"})
	if pane.views["one"] == nil || pane.views["one"].complete {
		t.Fatal("one completed stream hid another active stream")
	}
}

func TestLiveDiffPreviewLayoutAndWrapping(t *testing.T) {
	var pane liveDiffPreviewPane
	preview := previewViewFixture("one", 1)
	preview.Input = strings.Repeat("界", 100) + "TIP\n"
	pane.update(preview)
	for _, width := range []int{4, 10, 40, 120} {
		lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, width, 8)
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
	// A repeated snapshot must not move focus, because prepare reuses it.
	var pane liveDiffPreviewPane
	pane.update(previewViewFixture("one", 30))
	first, _ := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 80, 8)
	pane.update(previewViewFixture("one", 30))
	second, _ := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 80, 8)
	if !slices.Equal(first, second) {
		t.Fatal("identical snapshot moved the viewport")
	}
}

func BenchmarkLiveDiffPreviewPaneFrame(b *testing.B) {
	preview := previewViewFixture("one", 2000)
	var pane liveDiffPreviewPane
	base := preview.Input
	sequence := 0
	b.ReportAllocs()
	for b.Loop() {
		// Change the actual source so syntax work cannot hide behind cache hits.
		sequence++
		next := preview
		next.Input = strings.Replace(base, "stream_2000", fmt.Sprintf("stream_tip_%d", sequence), 1)
		pane.update(next)
		if _, err := pane.render(b.Context(), "/workspace", livediff.DarkTheme, 120, 28); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffConcurrentPreviewCards(t *testing.T) {
	var pane liveDiffPreviewPane
	first := previewViewFixture("first", 100)
	first.Caller = "/root/editor"
	second := previewViewFixture("second", 200)
	second.Caller = "/root/reviewer"
	second.Thread = "another-thread"
	pane.update(first)
	pane.update(second)
	check := func(height int) string {
		t.Helper()
		lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 100, height)
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
	otherSource := pane.views["second"].source
	for i := 101; i <= 150; i++ {
		first = previewViewFixture("first", i)
		first.Caller = "/root/editor"
		pane.update(first)
	}
	frame = check(12)
	if strings.Index(frame, "editor · ") > strings.Index(frame, "reviewer · ") ||
		!strings.Contains(frame, "stream_0150") || !strings.Contains(frame, "stream_0200") ||
		&otherSource[0] != &pane.views["second"].source[0] {
		t.Fatalf("delta reordered or replaced a concurrent view: %s", frame)
	}
	// Same-thread calls still have separate identities and completion states.
	second.Thread, second.Caller = first.Thread, first.Caller
	pane.update(second)
	pane.update(liveDiffPreview{ID: "first"})
	frame = check(12)
	if !strings.Contains(frame, "editor · ✓ edit") || !strings.Contains(frame, "editor · ◐ edit") {
		t.Fatalf("completion replaced another call: %s", frame)
	}
	if len(pane.order) != 2 || !pane.views["first"].complete || pane.views["second"].complete {
		t.Fatal("completion did not retain each call independently")
	}
	third := previewViewFixture("third", 300)
	pane.update(third)
	if frame := check(3); !strings.Contains(frame, "+1 more calls") {
		t.Fatalf("tiny pane silently hid a concurrent call: %s", frame)
	}
	if frame := check(12); !strings.Contains(frame, "stream_0200") || !strings.Contains(frame, "stream_0300") {
		t.Fatalf("resize did not restore concurrent tips: %s", frame)
	}
}

func TestLiveDiffPreviewCallerSafeAndBounded(t *testing.T) {
	var pane liveDiffPreviewPane
	preview := previewViewFixture("caller", 1)
	preview.Caller = "/root/\x1b[2J" + strings.Repeat("界", 80)
	pane.update(preview)
	for _, width := range []int{1, 8, 40, 80} {
		lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, width, 5)
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
	var pane liveDiffPreviewPane
	preview := previewViewFixture("caller", 1)
	preview.Caller = "/root/review_stock_preview"
	pane.update(preview)
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	header := ansi.Strip(lines[0])
	if !strings.HasPrefix(header, "  review_stock_preview · ◐") || strings.Contains(header, "…") {
		t.Fatalf("caller truncated despite available width: %q", header)
	}
	preview.Status = liveDiffPreviewUnavailable + strings.Repeat("reason ", 20)
	pane.update(preview)
	lines, err = pane.render(t.Context(), "/workspace", livediff.DarkTheme, 60, 5)
	if err != nil {
		t.Fatal(err)
	}
	if header = ansi.Strip(lines[0]); !strings.HasPrefix(header, "  review_stock") {
		t.Fatalf("long status obscured caller: %q", header)
	}
}

func BenchmarkLiveDiffConcurrentPreviewFrame(b *testing.B) {
	var pane liveDiffPreviewPane
	for i := range 16 {
		pane.update(previewViewFixture(fmt.Sprint(i), 2000))
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := pane.render(b.Context(), "/workspace", livediff.DarkTheme, 100, 28); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffCompletedPreviewShowsDoneGlyph(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{
		ID: "one", Workspace: "/workspace", Thread: "thread",
		Status: liveDiffPreviewEdit,
	})
	pane.update(liveDiffPreview{ID: "one"})
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 160, 12)
	if err != nil {
		t.Fatal(err)
	}
	if header := ansi.Strip(lines[0]); !strings.Contains(header, "· ✓ edit") || strings.Contains(header, "STREAMING") || strings.Contains(header, "COMPLETE") {
		t.Fatalf("completed preview header = %s", header)
	}
}

func TestLiveDiffPreviewTitleShowsFileStatusAndCounts(t *testing.T) {
	modified := mekugi.RenderReviewFile("/workspace/a.go", "/workspace/a.go", "one\ntwo\n", "one\nTWO\nthree\n")
	added := mekugi.RenderReviewFile("", "/workspace/new.txt", "", "x\n")
	deleted := mekugi.RenderReviewFile("/workspace/old.txt", "", "a\nb\n", "")
	for _, tc := range []struct {
		name    string
		preview liveDiffPreview
		want    string
	}{
		{"modified", liveDiffPreview{Status: liveDiffPreviewEdit, Files: []mekugi.ReviewFile{modified}}, "◐ M a.go +2 -1"},
		{"added", liveDiffPreview{Status: liveDiffPreviewEdit, Files: []mekugi.ReviewFile{added}}, "◐ A new.txt +1 -0"},
		{"deleted", liveDiffPreview{Status: liveDiffPreviewEdit, Files: []mekugi.ReviewFile{deleted}}, "◐ D old.txt +0 -2"},
		{"several files", liveDiffPreview{Status: liveDiffPreviewEdit, Files: []mekugi.ReviewFile{modified, added}}, "◐ A new.txt +1 -0 2/2 files"},
		{"running", liveDiffPreview{Status: liveDiffPreviewRunning, Files: []mekugi.ReviewFile{modified}}, "◐ M a.go +2 -1 · observed so far"},
		{"pending", liveDiffPreview{Status: liveDiffPreviewPending, Input: "will restore (pending)\n/workspace/a.go"}, "◐ scoped effects"},
		{"unavailable", liveDiffPreview{Status: liveDiffPreviewUnavailable + "patch cannot be projected", Input: "\n"}, "! patch cannot be projected"},
		{"diff tail", liveDiffPreview{Status: liveDiffPreviewEdit, Input: "+x\n", DiffText: true, Truncated: true}, "◐ edit · tail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pane liveDiffPreviewPane
			tc.preview.ID, tc.preview.Workspace, tc.preview.Thread = "one", "/workspace", "thread"
			pane.update(tc.preview)
			lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 120, 8)
			if err != nil {
				t.Fatal(err)
			}
			if header := ansi.Strip(lines[0]); !strings.HasSuffix(header, "thread · "+tc.want) {
				t.Fatalf("header = %q, want suffix %q", header, tc.want)
			}
		})
	}
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: liveDiffPreviewEdit, Files: []mekugi.ReviewFile{modified}})
	pane.update(liveDiffPreview{ID: "one"})
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 120, 8)
	if err != nil || !strings.Contains(lines[0], liveActivityGreen+"✓") ||
		!strings.Contains(lines[0], livediff.DarkTheme.Foreground(chroma.GenericInserted)+"+2") {
		t.Fatalf("completed header lost its glyph or count colors: %v %q", err, lines[0])
	}
}

func TestLiveDiffPendingEditRemovesEmptyCard(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: liveDiffPreviewEdit, Input: "\n"})
	pane.update(liveDiffPreview{ID: "one", Workspace: "/workspace", Thread: "thread"})
	if len(pane.views) != 0 || len(pane.order) != 0 {
		t.Fatal("pending edit left an empty status card")
	}
	pane.update(liveDiffPreview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: liveDiffPreviewEdit})
	if len(pane.views) != 1 {
		t.Fatal("later projection did not restore the preview")
	}
}

func TestLiveDiffPreviewCardsKeepSlotsAndGutter(t *testing.T) {
	var pane liveDiffPreviewPane
	for _, call := range []struct{ id, caller string }{{"a1", "/root/a"}, {"b1", "/root/b"}, {"c1", "/root/c"}} {
		preview := previewViewFixture(call.id, 5)
		preview.Caller = call.caller
		pane.update(preview)
	}
	render := func() {
		t.Helper()
		if _, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 100, 30); err != nil {
			t.Fatal(err)
		}
	}
	render()
	pane.update(liveDiffPreview{ID: "a1"})
	pane.update(liveDiffPreview{ID: "b1"})
	render()
	// b's next call reuses b's slot; a's finished card and c's live card stay put.
	next := previewViewFixture("b2", 5)
	next.Caller = "/root/b"
	pane.update(next)
	if !slices.Equal(pane.order, []string{"a1", "b2", "c1"}) {
		t.Fatalf("new call moved other cards: %v", pane.order)
	}
	// Another caller takes the finished slot rather than appending.
	other := previewViewFixture("d1", 5)
	other.Caller = "/root/d"
	pane.update(other)
	if !slices.Equal(pane.order, []string{"d1", "b2", "c1"}) {
		t.Fatalf("new caller did not reuse a finished slot: %v", pane.order)
	}
	// Finished cards remain until a new call takes their slot.
	pane.update(liveDiffPreview{ID: "c1"})
	render()
	pane.update(liveDiffPreview{ID: "d1"})
	render()
	pane.update(previewViewFixture("e1", 5))
	if !slices.Equal(pane.order, []string{"e1", "b2", "c1"}) {
		t.Fatalf("new call did not reuse the oldest finished slot: %v", pane.order)
	}

	// Line numbers keep their width when the source shrinks back.
	var single liveDiffPreviewPane
	single.update(previewViewFixture("one", 100))
	if _, err := single.render(t.Context(), "/workspace", livediff.DarkTheme, 100, 10); err != nil {
		t.Fatal(err)
	}
	single.update(previewViewFixture("one", 99))
	lines, err := single.render(t.Context(), "/workspace", livediff.DarkTheme, 100, 10)
	if err != nil || !strings.Contains(ansi.Strip(lines[len(lines)-1]), "  99│") {
		t.Fatalf("line-number gutter narrowed: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewPacerIsSteadyAndBounded(t *testing.T) {
	var pacer liveDiffPreviewPacer
	input := ""
	shown := 0
	// Bursts every fourth frame reveal whole lines on most frames, never all at once.
	steps := 0
	for frame := range 40 {
		if frame%4 == 0 {
			input += strings.Repeat("界x", 6) + "\n" + strings.Repeat("y", 10) + "\n"
		}
		next := pacer.advance(input, false, false)
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
	input += strings.Repeat("y", 64<<10) + "\n"
	if next := pacer.advance(input, false, false); next < len(input)-liveDiffPreviewMaxLag {
		t.Fatalf("lag exceeded its window: %d of %d", next, len(input))
	}
	// A finished call converges promptly, including an unterminated line.
	input += "tail"
	for range 20 {
		shown = pacer.advance(input, true, false)
	}
	if shown != len(input) {
		t.Fatalf("final input not reached: %d of %d", shown, len(input))
	}
}

func TestLiveDiffPreviewPacerBuffersUnits(t *testing.T) {
	// Arrival steps stay within the hold, so only unit ends are revealed.
	reveal := func(input string, encoded bool, step int) []string {
		var pacer liveDiffPreviewPacer
		var shown []string
		for i := min(step, len(input)); ; i = min(i+step, len(input)) {
			if next := pacer.advance(input[:i], false, encoded); len(shown) == 0 && next > 0 || len(shown) > 0 && input[:next] != shown[len(shown)-1] {
				shown = append(shown, input[:next])
			}
			if i == len(input) {
				return shown
			}
		}
	}
	// An edit payload reveals by line, even when its source contains operators.
	body := "for (i = 0; i < n; i++) {\n\tx();\n"
	if got := reveal(body, false, 3); !slices.Equal(got, []string{"for (i = 0; i < n; i++) {\n", body}) {
		t.Fatalf("lines: %q", got)
	}
	// Encoded input breaks at escaped line breaks, not at an escaped backslash.
	encoded := `{"cmd":"cat > f <<'EOF'\nsay \\n here\nnext`
	if got := reveal(encoded, true, 3); !slices.Equal(got, []string{
		`{"cmd":"cat > f <<'EOF'\n`, `{"cmd":"cat > f <<'EOF'\nsay \\n here\n`,
	}) {
		t.Fatalf("encoded: %q", got)
	}
	// A unit that outlives the hold streams rather than stalling the card.
	var pacer liveDiffPreviewPacer
	long := strings.Repeat("z", 64)
	shown := 0
	for range liveDiffPreviewMaxHold + 2 {
		shown = pacer.advance(long, false, false)
	}
	if shown != len(long) {
		t.Fatalf("held unit was not released: %d of %d", shown, len(long))
	}
}

func TestLiveDiffPreviewBirthsCarryAcrossSnapshots(t *testing.T) {
	rows := func(texts ...string) []liveDiffPreviewRow {
		var out []liveDiffPreviewRow
		for i, text := range texts {
			out = append(out, liveDiffPreviewRow{i + 1, ' ', text})
		}
		return out
	}
	start := time.Unix(100, 0)
	before := rows("a\n", "b\n", "par")
	born := liveDiffPreviewBirths(nil, before, nil, start)
	// A first display cascades, bounded by the maximum stagger.
	if !born[0].Equal(start) || !born[1].After(born[0]) || born[2].Sub(start) > liveDiffPreviewMaxStagger {
		t.Fatalf("first cascade: %v", born)
	}
	later := start.Add(time.Second)
	after := rows("a\n", "b\n", "partial\n", "c\n")
	next := liveDiffPreviewBirths(before, after, born, later)
	// Unchanged and grown rows keep their times; only the new row fades.
	if !slices.Equal(next[:3], born) || !next[3].Equal(later) {
		t.Fatalf("carried births: %v from %v", next, born)
	}
	// A clipped tail slides and renumbers without refading its rows.
	slid := rows("b\n", "partial\n", "c\n", "d\n")
	shifted := liveDiffPreviewBirths(after, slid, next, later.Add(time.Second))
	if !slices.Equal(shifted[:3], next[1:]) || !shifted[3].Equal(later.Add(time.Second)) {
		t.Fatalf("sliding tail: %v from %v", shifted, next)
	}
	// Context renumbered under an inserted row keeps its time.
	renumbered := slices.Insert(slices.Clone(after), 1, liveDiffPreviewRow{2, '+', "new\n"})
	for i := 2; i < len(renumbered); i++ {
		renumbered[i].number++
	}
	inserted := liveDiffPreviewBirths(after, renumbered, next, later.Add(2*time.Second))
	if !inserted[0].Equal(next[0]) || !inserted[1].Equal(later.Add(2*time.Second)) || !slices.Equal(inserted[2:], next[1:]) {
		t.Fatalf("renumbered context: %v from %v", inserted, next)
	}
}

func TestLiveDiffPreviewCompletionDoesNotFadeFinalSnapshot(t *testing.T) {
	start := time.Unix(100, 0)
	view := liveDiffPreviewView{current: previewViewFixture("done", 2)}
	motion := liveDiffPreviewMotion{enabled: true, canvas: livediff.DarkTheme.Canvas(), now: start}
	streaming, err := view.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.enabled = false
	settled, err := view.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil || slices.Equal(streaming, settled) {
		t.Fatalf("active input did not fade: %v %q", err, streaming)
	}

	view.current = previewViewFixture("done", 3)
	view.current.Complete = true
	view.complete = true
	motion.enabled = true
	motion.now = start.Add(10 * time.Millisecond)
	completed, err := view.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil {
		t.Fatal(err)
	}
	motion.enabled = false
	plain, err := view.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 8, motion)
	if err != nil || !slices.Equal(completed, plain) || !view.fading.IsZero() {
		t.Fatalf("completed preview remained dimmed: %v %q vs %q", err, completed, plain)
	}
}

func TestLiveDiffPreviewPacerKeepsEscapesWhole(t *testing.T) {
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
	// A held line is released without splitting its trailing escape.
	shown := 0
	for range liveDiffPreviewMaxHold + 1 {
		shown = pacer.advance(input, false, true)
	}
	if shown != 2 {
		t.Fatalf("streaming reveal split an escape at %d", shown)
	}
	// Finished input is shown whole even when it ends in a backslash.
	for range 3 {
		pacer.advance(input, true, true)
	}
	if pacer.shown != len(input) {
		t.Fatalf("finished input stalled at %d of %d", pacer.shown, len(input))
	}
}
