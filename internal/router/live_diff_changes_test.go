package router

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

// liveDiffChangesController renders captures in diff mode at a fixed size.
func liveDiffChangesController(t *testing.T, width, height int, captures []liveDiffChunk) *liveDiffTerminalController {
	t.Helper()
	c := newLiveDiffTerminalController(nil, "/w", nil)
	t.Cleanup(c.close)
	c.stdout = io.Discard
	c.size = func() (int, int, error) { return width, height, nil }
	c.diffMode = true
	c.view.Merge(livediff.GroupCaptures(captures))
	c.view.RefreshVisible()
	c.view.Following = false
	c.frame(t)
	return c
}

func (c *liveDiffTerminalController) frame(t *testing.T) {
	t.Helper()
	c.dirty = true
	if err := c.renderFrame(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func liveDiffLinesFile(path string, lines int) string {
	var text strings.Builder
	for i := range lines {
		fmt.Fprintf(&text, "%s_line_%02d\n", path, i)
	}
	return text.String()
}

func liveDiffCapture(key, path string, order uint64, before, after string, origin livediff.Origin) liveDiffChunk {
	return liveDiffChunk{
		Key: key, CaptureOrder: order, Origin: origin,
		Review: mekugi.RenderReviewFile("/w/"+path, "/w/"+path, before, after),
	}
}

// Scrolling through a file must not leave a mid-file position for the next open.
func TestLiveDiffOpenAfterScrollThroughLandsOnHeader(t *testing.T) {
	var captures []liveDiffChunk
	for i, path := range []string{"a.go", "b.go", "c.go"} {
		captures = append(captures, liveDiffCapture(path, path, uint64(i+1), "", liveDiffLinesFile(path, 30), livediff.Origin{Change: fmt.Sprintf("amber%d", i+1), Caller: "/root", Source: "apply_patch"}))
		captures[i].Review = mekugi.RenderReviewFile("", "/w/"+path, "", liveDiffLinesFile(path, 30))
	}
	c := liveDiffChangesController(t, 120, 20, captures)
	start := func(file int) int { return c.rendering.Starts[file] }
	c.view.Open(0)
	c.frame(t)
	for c.offset < start(2)+2 {
		c.handleKey('j')
		c.frame(t)
	}
	if c.view.Selected != 2 {
		t.Fatalf("scrolling into c.go selected %d", c.view.Selected)
	}
	c.handleKey('p')
	c.frame(t)
	if c.view.Selected != 1 || c.offset != start(1) {
		t.Fatalf("p after scrolling through b.go opened offset %d, want its header at %d", c.offset, start(1))
	}
	// A position the reader parked at, then jumped away from, is kept.
	for c.offset < start(1)+5 {
		c.handleKey('j')
		c.frame(t)
	}
	c.handleKey('n')
	c.frame(t)
	c.handleKey('p')
	c.frame(t)
	if c.offset != start(1)+5 {
		t.Fatalf("n/p lost the parked position: %d, want %d", c.offset, start(1)+5)
	}
	// Opening from the file list always starts at the header.
	c.handleKey('/')
	for _, key := range []byte("b.go\r") {
		c.handleKey(key)
	}
	c.frame(t)
	if c.view.Selected != 1 || c.offset != start(1) {
		t.Fatalf("Enter in the file list opened offset %d, want header %d", c.offset, start(1))
	}
	// The open file's header stays pinned while its content scrolls.
	for range 3 {
		c.handleKey('j')
	}
	var screen bytes.Buffer
	c.stdout = &screen
	c.frame(t)
	if row := ansi.Strip(liveDiffFrameRow(screen.String(), 1)); c.offset <= start(1) || !strings.Contains(row, "2/3  b.go") {
		t.Fatalf("mid-file row 1 = %q at offset %d", row, c.offset)
	}
	// The pinned heading must not cover the line at the viewport offset.
	if row, want := ansi.Strip(liveDiffFrameRow(screen.String(), 2)), strings.TrimSpace(ansi.Strip(c.lines[c.offset])); !strings.Contains(row, want) {
		t.Fatalf("row 2 = %q, want the offset line %q", row, want)
	}
	// The last line stays reachable below the pinned heading.
	c.handleKey('G')
	screen.Reset()
	c.frame(t)
	if row, want := ansi.Strip(liveDiffFrameRow(screen.String(), c.rows)), strings.TrimSpace(ansi.Strip(c.lines[len(c.lines)-1])); !strings.Contains(row, want) {
		t.Fatalf("bottom row = %q, want the last line %q", row, want)
	}
}

func TestLiveDiffChangesGraphLanesAndJumps(t *testing.T) {
	main := livediff.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}
	tests := livediff.Origin{Change: "apple1", Caller: "/root/script_tests", Source: "apply_patch"}
	pane := livediff.Origin{Change: "arch1", Caller: "/root/pane_trace", Source: "python"}
	later := livediff.Origin{Change: "amber2", Caller: "/root", Source: "sed"}
	captures := []liveDiffChunk{
		liveDiffCapture("k1", "a.go", 1, "one\n", "ONE\n", main),
		liveDiffCapture("k2", "b.go", 2, "two\n", "TWO\n", tests),
		liveDiffCapture("k3", "c.go", 3, "three\n", "THREE\n", pane),
		liveDiffCapture("k4", "b.go", 4, "TWO\n", "Two\n", later),
		liveDiffCapture("k5", "c.go", 5, "THREE\n", "Three\n", later),
	}
	c := liveDiffChangesController(t, 130, 20, captures)
	c.handleKey('\t')
	c.frame(t)
	n := &c.navigation
	rows := n.changes.render(false, false, "", n.width(130), 12, livediff.DarkTheme)
	var plain []string
	for _, row := range rows {
		plain = append(plain, ansi.Strip(row))
	}
	joined := strings.Join(plain, "\n")
	for _, want := range []string{
		"Changes  4 · @all",
		"● amber1 apply_patch",
		"├─╮ script_tests",
		"│ ● apple1 apply_patch",
		"├─┼─╮ pane_trace",
		"│ │ ● arch1 python",
		"● │ │ amber2 sed",
		"2f",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("changes graph lacks %q:\n%s", want, joined)
		}
	}
	// } walks each change's files in capture order and opens their headers,
	// starting from the open file's first change.
	c.view.Open(0)
	var visited []string
	for range 5 {
		c.handleKey('}')
		c.frame(t)
		if c.offset != c.rendering.Starts[c.view.Selected] {
			t.Fatalf("} opened offset %d, not the header of file %d", c.offset, c.view.Selected)
		}
		visited = append(visited, n.changes.target.change+":"+c.view.Files[c.view.Selected].Path)
	}
	if got := strings.Join(visited, " "); got != "amber1:/w/a.go apple1:/w/b.go arch1:/w/c.go amber2:/w/b.go amber2:/w/c.go" {
		t.Fatalf("} order = %s", got)
	}
	c.handleKey('{')
	if n.changes.target.change != "amber2" || c.view.Files[c.view.Selected].Path != "/w/b.go" {
		t.Fatalf("{ went to %+v", n.changes.target)
	}
	// Each file's header names the changes it composes.
	c.frame(t)
	header := ansi.Strip(strings.Join(c.rendering.Lines[c.rendering.Starts[1]:c.rendering.Starts[1]+3], "\n"))
	if !strings.Contains(header, "┄ apple1 script_tests·apply_patch  amber2 main·sed") {
		t.Fatalf("b.go provenance = %q", header)
	}
}

// Change file rows share the navigator's colored status label, and a narrow
// row drops its source instead of truncating it. A single-file change nests
// its file beside the diff and names it on its own row in a stacked list.
func TestLiveDiffChangesRowFormat(t *testing.T) {
	origin := livediff.Origin{Change: "apple1", Caller: "/root", Source: "apply_patch"}
	single := liveDiffChangesController(t, 130, 20, []liveDiffChunk{liveDiffCapture("k1", "broker.go", 1, "one\n", "ONE\n", origin)})
	s := &single.navigation.changes
	if rows := s.render(false, false, "", 60, 4, livediff.DarkTheme); len(s.rows) != 2 || !strings.Contains(ansi.Strip(rows[2]), "apple1 apply_patch 1f") || !strings.Contains(ansi.Strip(rows[3]), "└ M broker.go +1 -1") {
		t.Fatalf("a side navigator did not nest the single file: %q", rows)
	}
	s.inline = true
	s.rebuild(&single.view, single.workspace)
	if len(s.rows) != 1 {
		t.Fatalf("an inline single-file change expanded into %d rows", len(s.rows))
	}
	if row := ansi.Strip(s.render(false, false, "", 60, 3, livediff.DarkTheme)[2]); !strings.Contains(row, "apple1 apply_patch M broker.go +1 -1") || strings.Contains(row, "1f") {
		t.Fatalf("single-file change row = %q", row)
	}
	// Hover underlines the row's text but not its graph lane.
	s.hover = 1
	if row := s.render(false, false, "", 60, 3, livediff.DarkTheme)[2]; !strings.Contains(row, "\x1b[4m") || strings.Index(row, "\x1b[4m") < strings.Index(row, "●") {
		t.Fatalf("hover underlined the graph: %q", row)
	}
	c := liveDiffChangesController(t, 130, 20, []liveDiffChunk{
		liveDiffCapture("k1", "broker.go", 1, "one\n", "ONE\n", origin),
		liveDiffCapture("k2", "queue.go", 2, "two\n", "TWO\n", origin),
	})
	l := &c.navigation.changes
	l.expanded = map[string]bool{"apple1": true}
	l.rebuild(&c.view, c.workspace)
	rows := l.render(false, false, "", 40, 5, livediff.DarkTheme)
	file := liveDiffFileLabel(liveDiffStatus{before: "/w/broker.go", after: "/w/broker.go", edited: true}, "broker.go", "/w", livediff.DarkTheme)
	if !strings.Contains(rows[3], file) || !strings.Contains(ansi.Strip(rows[3]), "├ M broker.go +1 -1") {
		t.Fatalf("file row = %q, want shared label %q", rows[3], file)
	}
	if row := ansi.Strip(rows[2]); !strings.Contains(row, "apple1 apply_patch 2f") {
		t.Fatalf("wide change row = %q", row)
	}
	if row := ansi.Strip(l.render(false, false, "", 24, 4, livediff.DarkTheme)[2]); strings.Contains(row, "apply") || !strings.Contains(row, "apple1 2f +2 -2") {
		t.Fatalf("narrow change row = %q, want the source omitted", row)
	}
	focused := l.render(true, false, "", 24, 4, livediff.DarkTheme)[2]
	if !strings.HasPrefix(focused, livediff.DarkTheme.SelectionBackground()) ||
		!strings.HasSuffix(focused, "\x1b[49m") || ansi.StringWidth(focused) != 23 ||
		strings.HasPrefix(ansi.Strip(focused), ">") {
		t.Fatalf("focused change row should fill in place without an arrow: %q", focused)
	}
}

// File status follows git's short status, from the net diff across captures.
func TestLiveDiffFileStatus(t *testing.T) {
	conflicted := mekugi.RenderReviewFile("/w/a.go", "/w/a.go", "a\n", "<<<<<<< workspace\na\n=======\nb\n>>>>>>> mchanges revert amber1\n")
	for _, tc := range []struct {
		name    string
		regions []mekugi.ReviewFile
		want    string
	}{
		{"added", []mekugi.ReviewFile{mekugi.RenderReviewFile("", "/w/a.go", "", "a\n")}, "A a.go"},
		{"deleted", []mekugi.ReviewFile{mekugi.RenderReviewFile("/w/a.go", "", "a\n", "")}, "D a.go"},
		{"modified", []mekugi.ReviewFile{mekugi.RenderReviewFile("/w/a.go", "/w/a.go", "a\n", "b\n")}, "M a.go"},
		{"rename only", []mekugi.ReviewFile{mekugi.RenderReviewFile("/w/old.go", "/w/a.go", "", "")}, "R old.go → a.go"},
		{"rename across folders", []mekugi.ReviewFile{mekugi.RenderReviewFile("/w/x/old.go", "/w/a.go", "", "")}, "R x/old.go → a.go"},
		{"rename then edit", []mekugi.ReviewFile{
			mekugi.RenderReviewFile("/w/old.go", "/w/a.go", "", ""),
			mekugi.RenderReviewFile("/w/a.go", "/w/a.go", "a\n", "b\n"),
		}, "RM old.go → a.go"},
		{"rename with unknown content", []mekugi.ReviewFile{mekugi.RenderIncompleteReviewFile("/w/old.go", "/w/a.go", "unreadable")}, "RM old.go → a.go"},
		{"binary move", []mekugi.ReviewFile{mekugi.RenderBinaryReviewFile("/w/old.bin", "/w/a.go", 3, 3, "abc", "abc")}, "R old.bin → a.go"},
		{"binary rename with edit", []mekugi.ReviewFile{mekugi.RenderBinaryReviewFile("/w/old.bin", "/w/a.go", 3, 4, "abc", "abd")}, "RM old.bin → a.go"},
		{"mchanges conflict", []mekugi.ReviewFile{conflicted}, "UU a.go"},
	} {
		if got := ansi.Strip(liveDiffFileLabel(liveDiffStatusOf(tc.regions...), "a.go", "/w", livediff.DarkTheme)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The tree codes the composed net diff: resolving an mchanges conflict clears UU.
func TestLiveDiffTreeConflictClearsWhenResolved(t *testing.T) {
	base := "a\n"
	markers := "<<<<<<< workspace\na\n=======\nb\n>>>>>>> mchanges revert amber1\n"
	origin := livediff.Origin{Change: "amber2", Caller: "/root", Source: "mchanges"}
	captures := []liveDiffChunk{liveDiffCapture("k1", "a.go", 1, base, markers, origin)}
	row := func() string {
		c := liveDiffChangesController(t, 130, 20, captures)
		for _, line := range c.navigation.render(c.files, c.rendering.Counts, c.view.Selected, 34, 6, livediff.DarkTheme) {
			if line := ansi.Strip(line); strings.Contains(line, "a.go") {
				return line
			}
		}
		return ""
	}
	if got := row(); !strings.Contains(got, "UU a.go") {
		t.Fatalf("conflicted row = %q", got)
	}
	captures = append(captures, liveDiffCapture("k2", "a.go", 2, markers, "b\n", livediff.Origin{Change: "amber3", Caller: "/root", Source: "apply_patch"}))
	if got := row(); !strings.Contains(got, "M a.go") {
		t.Fatalf("resolved row = %q", got)
	}
}

func TestLiveDiffCallerFilterComposesOthersAsBaseline(t *testing.T) {
	main := livediff.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}
	tests := livediff.Origin{Change: "apple1", Caller: "/root/script_tests", Source: "python"}
	captures := []liveDiffChunk{
		liveDiffCapture("k1", "a.go", 1, "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n", "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n", main),
		liveDiffCapture("k2", "a.go", 2, "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n", "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\nEIGHT\n", tests),
		liveDiffCapture("k3", "b.go", 3, "x\n", "X\n", main),
	}
	c := liveDiffChangesController(t, 130, 30, captures)
	if got := liveDiffCallers(&c.view); strings.Join(got, ",") != "/root,/root/script_tests" {
		t.Fatalf("callers = %v", got)
	}
	c.handleKey('a')
	c.handleKey('a')
	c.frame(t)
	if c.view.Caller != "/root/script_tests" {
		t.Fatalf("a cycled to %q", c.view.Caller)
	}
	text := ansi.Strip(strings.Join(c.rendering.Lines, "\n"))
	if !strings.Contains(text, "+EIGHT") || strings.Contains(text, "+ONE") || strings.Contains(text, "b.go") {
		t.Fatalf("filtered diff shows other callers' edits:\n%s", text)
	}
	if !strings.Contains(text, "1 change by other callers as baseline") {
		t.Fatalf("filtered diff hides its baseline:\n%s", text)
	}
	// Former flush keys must not hide captured history or change the filter.
	for _, key := range []byte{'f', 'F'} {
		c.handleKey(key)
		c.frame(t)
		if c.view.Caller != "/root/script_tests" || len(c.navigation.changes.nodes) != 1 {
			t.Fatalf("%q changed captured history or caller filter", key)
		}
	}
	c.handleKey('0')
	c.frame(t)
	if text := ansi.Strip(strings.Join(c.rendering.Lines, "\n")); !strings.Contains(text, "+ONE") || !strings.Contains(text, "+X") {
		t.Fatalf("0 did not restore all callers:\n%s", text)
	}
}

func TestExecObservationSourceLabel(t *testing.T) {
	for _, tc := range []struct {
		observation execObservation
		want        string
	}{
		{execObservation{}, "exec_command"},
		{execObservation{Labels: []string{"sed"}}, "sed"},
		{execObservation{Programs: []execProgram{{Label: "python3", Direct: true}, {Label: "make"}}}, "python3"},
		{execObservation{Labels: []string{"sed", "rm"}, Programs: []execProgram{{Label: "shell", Direct: true}}}, "sed+rm+…"},
	} {
		if got := tc.observation.sourceLabel(); got != tc.want {
			t.Errorf("%+v source = %q, want %q", tc.observation, got, tc.want)
		}
	}
}

func TestAgentDisplayName(t *testing.T) {
	for name, want := range map[string]string{"/root": "main", "/root/worker": "worker", "/root/a/x": "a/x", "": ""} {
		if got := agentDisplayName(name); got != want {
			t.Errorf("agentDisplayName(%q) = %q, want %q", name, got, want)
		}
	}
}

// Keys may arrive between an update and the next paint; the Changes tab
// must neither index removed files nor open the wrong one.
func TestLiveDiffChangesSurviveShrinkingMerge(t *testing.T) {
	origin := func(change string) livediff.Origin {
		return livediff.Origin{Change: change, Caller: "/root", Source: "apply_patch"}
	}
	captures := []liveDiffChunk{
		liveDiffCapture("k1", "a.go", 1, "a\n", "A\n", origin("amber1")),
		liveDiffCapture("k2", "b.go", 2, "b\n", "B\n", origin("amber2")),
		liveDiffCapture("k3", "c.go", 3, "c\n", "C\n", origin("amber3")),
	}
	c := liveDiffChangesController(t, 130, 20, captures)
	c.view.Merge(livediff.GroupCaptures(captures[2:]))
	c.view.RefreshVisible()
	for _, key := range []byte("}}{\t\r") {
		c.handleKey(key)
	}
	c.refreshChanges()
	c.handleKey('}')
	if c.view.Files[c.view.Selected].Path != "/w/c.go" {
		t.Fatalf("} after refresh opened %q", c.view.Files[c.view.Selected].Path)
	}
}

func TestLiveDiffChangesFilterAndQueryDetails(t *testing.T) {
	worker := livediff.Origin{Change: "apple1", Caller: "/root/worker", Source: "sed"}
	other := livediff.Origin{Change: "arch1", Caller: "/root/other", Source: "python3"}
	c := liveDiffChangesController(t, 130, 20, []liveDiffChunk{
		liveDiffCapture("k1", "a.go", 1, "a\n", "A\n", worker),
	})
	// One caller: the filtered view is unchanged, but the heading still says so.
	c.handleKey('a')
	c.frame(t)
	if heading := ansi.Strip(c.navigation.render(c.files, c.rendering.Counts, 0, 34, 4, livediff.DarkTheme)[0]); !strings.Contains(heading, "@worker") {
		t.Fatalf("Files heading = %q", heading)
	}
	// Accepting a Changes query opens the match instead of the caller's branch row.
	c = liveDiffChangesController(t, 130, 20, []liveDiffChunk{
		liveDiffCapture("k1", "a.go", 1, "a\n", "A\n", other),
		liveDiffCapture("k2", "b.go", 2, "b\n", "B\n", worker),
	})
	c.filterCaller("/root/worker")
	for _, key := range []byte("\t/apple\r") {
		c.handleKey(key)
	}
	if c.view.Caller != "/root/worker" || c.view.Files[c.view.Selected].Path != "/w/b.go" {
		t.Fatalf("Enter in the Changes filter: caller %q, file %q", c.view.Caller, c.view.Files[c.view.Selected].Path)
	}
}

// A change's captures of one file share a capture order; they are one change.
func TestLiveDiffBaselineCountsChangesOnce(t *testing.T) {
	main := livediff.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}
	first := liveDiffCapture("k1", "a.go", 1, "one\n", "", main)
	first.Review = mekugi.RenderReviewFile("/w/a.go", "", "one\n", "")
	second := liveDiffCapture("k2", "a.go", 1, "", "two\n", main)
	second.Review = mekugi.RenderReviewFile("", "/w/a.go", "", "two\n")
	third := liveDiffCapture("k3", "a.go", 2, "two\n", "TWO\n", livediff.Origin{Change: "apple1", Caller: "/root/worker"})
	var view liveDiffView
	view.Merge(livediff.GroupCaptures([]liveDiffChunk{first, second, third}))
	view.FilterCaller("/root/worker")
	for _, file := range view.Visible {
		if file.Baseline != 1 {
			t.Fatalf("baseline = %d, want 1", file.Baseline)
		}
	}
}

// Captures without a caller filter as their own caller, distinct from all.
func TestLiveDiffUnknownCallerFilters(t *testing.T) {
	c := liveDiffChangesController(t, 130, 20, []liveDiffChunk{
		liveDiffCapture("k1", "a.go", 1, "a\n", "A\n", livediff.Origin{Change: "amber1", Caller: "/root"}),
		liveDiffCapture("k2", "b.go", 2, "b\n", "B\n", livediff.Origin{Change: "old1"}),
		liveDiffCapture("k3", "c.go", 3, "c\n", "C\n", livediff.Origin{Change: "apple1", Caller: "/root/worker"}),
	})
	var cycle []string
	for range 4 {
		c.handleKey('a')
		cycle = append(cycle, c.view.Caller)
	}
	if got := strings.Join(cycle, ","); got != "/root,"+livediff.UnknownCaller+",/root/worker," {
		t.Fatalf("a cycled %q", got)
	}
	c.handleKey('\t')
	l := &c.navigation.changes
	branch := slices.IndexFunc(l.rows, func(row liveDiffChangeRow) bool {
		return row.kind == 'b' && l.nodes[row.node].Caller == ""
	})
	l.cursor = branch
	c.openChangeRow()
	if c.view.Caller != livediff.UnknownCaller || len(l.nodes) != 1 || l.nodes[0].Change != "old1" {
		t.Fatalf("Enter on the unknown branch: caller %q, nodes %+v", c.view.Caller, l.nodes)
	}
	c.filterCaller("")
	l.query = "@unknown"
	l.rebuild(&c.view, c.workspace)
	if len(l.nodes) != 1 || l.nodes[0].Change != "old1" {
		t.Fatalf("@unknown matched %+v", l.nodes)
	}
}

func TestLiveDiffHunkJumpKeepsLeftPosition(t *testing.T) {
	var captures []liveDiffChunk
	for i, path := range []string{"a.go", "b.go"} {
		captures = append(captures, liveDiffCapture(path, path, uint64(i+1), "", liveDiffLinesFile(path, 30), livediff.Origin{Change: fmt.Sprintf("amber%d", i+1), Caller: "/root"}))
		captures[i].Review = mekugi.RenderReviewFile("", "/w/"+path, "", liveDiffLinesFile(path, 30))
	}
	c := liveDiffChangesController(t, 120, 20, captures)
	c.view.Open(0)
	c.frame(t)
	for c.offset < c.rendering.Starts[0]+5 {
		c.handleKey('j')
		c.frame(t)
	}
	parked := c.offset
	c.handleKey(']')
	c.frame(t)
	if c.view.Selected != 1 {
		t.Fatalf("] stayed in file %d", c.view.Selected)
	}
	c.handleKey('p')
	c.frame(t)
	if c.offset != parked {
		t.Fatalf("p after ] reopened offset %d, want parked %d", c.offset, parked)
	}
}
