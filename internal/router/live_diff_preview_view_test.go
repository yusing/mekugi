package router

import (
	"bytes"
	"context"
	"fmt"
	"github.com/yusing/mekugi/internal/livediff"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
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
	if err != nil || !strings.Contains(lines[0], "STREAMING COMPLETE") {
		t.Fatalf("missing completion hold: %v %q", err, lines)
	}
	// Completed input stays visible until a new call replaces it.
	pane.update(previewViewFixture("two", 10))
	if len(pane.order) != 1 || pane.order[0] != "two" {
		t.Fatal("new call did not replace completed input")
	}
	pane.update(liveDiffPreview{ID: "two"})
	lines, err = pane.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	if err != nil || !strings.Contains(lines[0], "STREAMING COMPLETE") || !strings.Contains(strings.Join(lines, "\n"), "stream_0010") {
		t.Fatalf("completed stream did not persist: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewPaneFinishedCardExpiresWithoutAnotherCall(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(previewViewFixture("done", 2))
	pane.update(previewViewFixture("live", 2))
	pane.update(liveDiffPreview{ID: "done"})
	if _, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12); err != nil {
		t.Fatal(err)
	}
	now := pane.views["done"].completed.Add(liveDiffPreviewStaleAfter)
	if !pane.expire(now) || !slices.Equal(pane.order, []string{"live"}) || pane.live() != 1 {
		t.Fatalf("finished card did not expire independently: %v", pane.order)
	}
	if pane.nextExpiry(now) != 0 {
		t.Fatal("live card scheduled an expiry")
	}
}

func TestLiveDiffPreviewUpdatePreemptsDistantExpiry(t *testing.T) {
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

func TestLiveDiffFinishedInputClearsOnTerminalWithoutAnotherEvent(t *testing.T) {
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
	c.previewPane.views["done"].completed = time.Now().Add(-liveDiffPreviewStaleAfter)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
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
	go func() { done <- c.run(ctx, nil, nil, nil) }()
	for index := range 2 {
		select {
		case frame := <-frames:
			if index == 0 && !strings.Contains(frame, "STREAMING COMPLETE") {
				t.Fatalf("first frame did not show completion: %q", frame)
			}
			if index == 1 && (strings.Contains(frame, "Live input") || strings.Contains(frame, "STREAMING COMPLETE")) {
				t.Fatalf("finished stream persisted after expiry: %q", frame)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("terminal did not redraw after finished input expired")
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

func TestLiveDiffPreviewSyntaxAndVisibleTip(t *testing.T) {
	preview := previewViewFixture("colored", 50)
	preview.Input = "#!python3\n" + strings.Repeat("# context\n", 49) + "return \"STREAM_TIP\"\n"
	var pane liveDiffPreviewPane
	pane.update(preview)
	for _, theme := range []liveDiffTheme{livediff.DarkTheme, livediff.LightTheme} {
		lines, err := pane.render(t.Context(), "/workspace", theme, 70, 12)
		if err != nil {
			t.Fatal(err)
		}
		center := lines[len(lines)-1]
		if !strings.Contains(ansi.Strip(center), "STREAM_TIP") ||
			!strings.Contains(center, theme.Foreground(chroma.Keyword)+"return") ||
			!strings.Contains(center, theme.Foreground(chroma.LiteralString)) {
			t.Fatalf("tip not visible and colored: %q", center)
		}
		if strings.Contains(lines[0], "validated") || strings.Contains(lines[0], "applied") {
			t.Fatal("preview title contains redundant disclaimer")
		}
	}
}

func TestLiveDiffPreviewRawScript(t *testing.T) {
	var pane liveDiffPreviewPane
	preview := liveDiffPreview{ID: "mixed", Workspace: "/workspace", Thread: "thread",
		Input: "shell printf 'before'\nnew after.go\ntype <<PATCH\npackage main\n"}
	pane.update(preview)
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 70, 12)
	text := ansi.Strip(strings.Join(lines, "\n"))
	if err != nil || !strings.Contains(text, "STREAMING SCRIPT") || !strings.Contains(text, "package main") {
		t.Fatalf("missing mixed-script tail: %q, %v", text, err)
	}
	if strings.Contains(text, "stream.sh") || strings.Contains(text, "PREVIEW UNAVAILABLE") {
		t.Fatalf("raw script pretends to be a projected file: %q", text)
	}
}

func TestLiveDiffPreviewScriptSyntax(t *testing.T) {
	for _, theme := range []liveDiffTheme{livediff.DarkTheme, livediff.LightTheme} {
		for _, tc := range []struct {
			name, input, token string
			kind               chroma.TokenType
		}{
			{"bash", "if true; then\n  printf 'hello'\nfi\n", "if", chroma.Keyword},
			{"python", "#!python3\n" + strings.Repeat("# context\n", 100) + "return 'PYTHON_TIP'\n", "return", chroma.Keyword},
			{"uv_python", "#!uv run python\nreturn 'PYTHON_TIP'\n", "return", chroma.Keyword},
			{"javascript", "#!node\nconst tip = 'JS_TIP';\n", "const", chroma.KeywordDeclaration},
			{"batch", "echo first\n#!python3\nreturn 'BATCH_TIP'\n", "return", chroma.Keyword},
		} {
			t.Run(fmt.Sprintf("%d/%s", theme, tc.name), func(t *testing.T) {
				var pane liveDiffPreviewPane
				pane.update(liveDiffPreview{ID: tc.name, Workspace: "/workspace", Input: tc.input})
				lines, err := pane.render(t.Context(), "/workspace", theme, 100, 20)
				frame := strings.Join(lines, "\n")
				if err != nil || !strings.Contains(frame, theme.Foreground(tc.kind)+tc.token) {
					t.Fatalf("missing %s syntax: %v %q", tc.name, err, frame)
				}
			})
		}
	}
}

func TestLiveDiffScriptLanguageBoundaries(t *testing.T) {
	input := "cat <<EOF\n#!python3\nreturn not_python\nEOF\n#!python3\nreturn True\n"
	paths := liveDiffSourceRows(input, liveDiffScriptSyntax(input))
	if len(paths) != 6 || paths[2].Path != "stream.sh" || paths[5].Path != "stream.py" {
		t.Fatalf("heredoc content changed language: %v", paths)
	}
}

func BenchmarkLiveDiffScriptPreviewFrame(b *testing.B) {
	for _, sourceName := range []string{"python", "shell"} {
		b.Run(sourceName, func(b *testing.B) {
			base := "#!python3\n" + strings.Repeat("# context\n", 2000)
			if sourceName == "shell" {
				base = strings.Repeat("echo context\n", 2000)
			}
			var pane liveDiffPreviewPane
			sequence := 0
			b.ReportAllocs()
			for b.Loop() {
				sequence++
				input := base + fmt.Sprintf("return \"tip_%d\"\n", sequence)
				pane.update(liveDiffPreview{ID: "stream", Workspace: "/workspace", Input: input})
				if _, err := pane.render(b.Context(), "/workspace", livediff.DarkTheme, 120, 28); err != nil {
					b.Fatal(err)
				}
			}
		})
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
	for _, want := range []string{"/root/editor · STREAMING SCRIPT", "/root/reviewer · STREAMING SCRIPT", "stream_0100", "stream_0200"} {
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
	if strings.Index(frame, "/root/editor") > strings.Index(frame, "/root/reviewer") ||
		!strings.Contains(frame, "stream_0150") || !strings.Contains(frame, "stream_0200") ||
		&otherSource[0] != &pane.views["second"].source[0] {
		t.Fatalf("delta reordered or replaced a concurrent view: %s", frame)
	}
	// Same-thread calls still have separate identities and completion states.
	second.Thread, second.Caller = first.Thread, first.Caller
	pane.update(second)
	pane.update(liveDiffPreview{ID: "first"})
	frame = check(12)
	if !strings.Contains(frame, "/root/editor · STREAMING COMPLETE") || !strings.Contains(frame, "/root/editor · STREAMING SCRIPT") {
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
	if !strings.HasPrefix(header, "  "+preview.Caller+" · STREAMING") || strings.Contains(header, "…") {
		t.Fatalf("caller truncated despite available width: %q", header)
	}
	preview.Status = "PREVIEW UNAVAILABLE: " + strings.Repeat("reason ", 20)
	pane.update(preview)
	lines, err = pane.render(t.Context(), "/workspace", livediff.DarkTheme, 60, 5)
	if err != nil {
		t.Fatal(err)
	}
	if header = ansi.Strip(lines[0]); !strings.HasPrefix(header, "  /root/review_stock") {
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

func TestLiveDiffCompletedPreviewKeepsProvisionalStatus(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{
		ID: "one", Workspace: "/workspace", Thread: "thread",
		Status: "STREAMING PREVIEW",
	})
	pane.update(liveDiffPreview{ID: "one"})
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 160, 12)
	if err != nil {
		t.Fatal(err)
	}
	header := ansi.Strip(lines[0])
	for _, want := range []string{"STREAMING COMPLETE"} {
		if !strings.Contains(header, want) {
			t.Fatalf("completed preview lost %q: %s", want, header)
		}
	}
}

func TestLiveDiffPendingEditHidesEarlierScript(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: "STREAMING SCRIPT", Input: "apply_patch "})
	pane.update(liveDiffPreview{ID: "one", Workspace: "/workspace", Thread: "thread"})
	if len(pane.views) != 0 || len(pane.order) != 0 {
		t.Fatal("pending edit left a script or empty status card")
	}
	pane.update(liveDiffPreview{ID: "one", Workspace: "/workspace", Thread: "thread", Status: "STREAMING PREVIEW"})
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
	// A stale finished card leaves when another call starts.
	pane.update(liveDiffPreview{ID: "c1"})
	pane.views["c1"].completed = time.Now().Add(-liveDiffPreviewStaleAfter)
	render()
	pane.update(liveDiffPreview{ID: "d1"})
	render()
	pane.update(previewViewFixture("e1", 5))
	if !slices.Equal(pane.order, []string{"e1", "b2"}) {
		t.Fatalf("stale card was not dropped: %v", pane.order)
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
	// Bursts every fourth frame reveal on every frame, never all at once.
	for frame := range 40 {
		if frame%4 == 0 {
			input += strings.Repeat("界x", 20)
		}
		next := pacer.advance(input, false)
		if next < shown || next > len(input) || next < len(input) && !utf8.RuneStart(input[next]) {
			t.Fatalf("frame %d revealed %d of %d after %d", frame, next, len(input), shown)
		}
		if frame > 8 && (next == shown && next < len(input) || frame%4 == 0 && next == len(input)) {
			t.Fatalf("frame %d did not pace the burst: %d -> %d of %d", frame, shown, next, len(input))
		}
		shown = next
	}
	// A large backlog skips to the window at the tip.
	input += strings.Repeat("y", 64<<10)
	if next := pacer.advance(input, false); next < len(input)-liveDiffPreviewMaxLag {
		t.Fatalf("lag exceeded its window: %d of %d", next, len(input))
	}
	// A finished call converges promptly.
	for range 20 {
		shown = pacer.advance(input, true)
	}
	if shown != len(input) {
		t.Fatalf("final input not reached: %d of %d", shown, len(input))
	}
}
