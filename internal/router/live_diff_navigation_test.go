package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

func navigationFile(path string) liveDiffFile {
	return liveDiffFile{
		Path: path,
		Chunks: []liveDiffChunk{{
			Key:    path,
			Review: mekugi.ReviewFile{BeforePath: path, AfterPath: path},
		}},
	}
}

func navigationEntryIndex(entries []liveDiffNavEntry, id string) int {
	for i, entry := range entries {
		if entry.id == id {
			return i
		}
	}
	return -1
}

func TestLiveDiffNavigationTreeAndFlatOrdering(t *testing.T) {
	files := []liveDiffFile{
		navigationFile("z.go"),
		navigationFile("src/pkg/only/b.go"),
		navigationFile("docs/readme.md"),
		navigationFile("src/pkg/only/a.go"),
	}

	var nav liveDiffNavigation
	nav.rebuild(files, "")
	if len(nav.entries) != 6 {
		t.Fatalf("tree should contain two folders and four files, got %#v", nav.entries)
	}
	if first := nav.entries[0]; first.folder != "docs" || first.count != 1 {
		t.Fatalf("folder rows should precede files and carry a descendant count: %+v", first)
	}
	if readme := nav.entries[1]; readme.file < 0 || readme.label != "readme.md" {
		t.Fatalf("folder descendants should be nested below their folder: %+v", readme)
	}
	if compact := nav.entries[2]; compact.folder != "src/pkg/only" || compact.label != "src/pkg/only" || compact.count != 2 {
		t.Fatalf("single-child folder chain was not compacted with its count: %+v", compact)
	}
	if got := []string{nav.entries[3].label, nav.entries[4].label, nav.entries[5].label}; got[0] != "a.go" || got[1] != "b.go" || got[2] != "z.go" {
		t.Fatalf("files under a tree branch should be path sorted: %q", got)
	}

	nav.flat = true
	nav.rebuild(files, "")
	var got []string
	for _, entry := range nav.entries {
		if entry.file < 0 {
			t.Fatalf("flat view unexpectedly contains folder rows: %+v", entry)
		}
		got = append(got, entry.label)
	}
	want := []string{"docs/readme.md", "src/pkg/only/a.go", "src/pkg/only/b.go", "z.go"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("flat paths are not sorted: got %q, want %q", got, want)
	}
}

func TestLiveDiffNavigationFilterAndStableRows(t *testing.T) {
	files := []liveDiffFile{
		navigationFile("pkg/a.go"),
		navigationFile("pkg/b.go"),
		navigationFile("pkg/c.go"),
	}
	c := &liveDiffTerminalController{files: files, workspace: "/workspace", rows: 6}
	c.navigation.rebuild(files, c.workspace)
	c.navigation.cursor = navigationEntryIndex(c.navigation.entries, "d:pkg")
	c.navigation.top = navigationEntryIndex(c.navigation.entries, "d:pkg")
	c.navigation.collapsed = map[string]bool{"pkg": true}

	// Flat mode exposes the descendants of a collapsed tree folder, and a
	// filter is cleared back to the original tree position rather than reset.
	c.navigationKey('/')
	c.editFilter('c')
	if len(c.navigation.entries) != 1 || c.navigation.entries[0].label != "pkg/c.go" {
		t.Fatalf("filter did not expose the matching path: %+v", c.navigation.entries)
	}
	c.editFilter(127)
	if c.navigation.query != "" || c.navigation.cursor != 0 || c.navigation.top != 0 {
		t.Fatalf("clearing a filter did not restore the saved tree rows: query=%q cursor=%d top=%d entries=%+v",
			c.navigation.query, c.navigation.cursor, c.navigation.top, c.navigation.entries)
	}
	if !c.navigation.collapsed["pkg"] {
		t.Fatal("filtering discarded the tree's collapsed-folder state")
	}

	// Adding a lexically earlier file must preserve the identities at the
	// cursor and top of a flat viewport, not their old numerical indexes.
	c.navigation.flat = true
	c.navigation.rebuild(files, c.workspace)
	c.navigation.cursor = navigationEntryIndex(c.navigation.entries, "f:pkg/b.go")
	c.navigation.top = navigationEntryIndex(c.navigation.entries, "f:pkg/a.go")
	files = append(files, navigationFile("00.go"))
	c.files = files
	c.navigation.rebuild(files, c.workspace)
	if got := c.navigation.entries[c.navigation.cursor].id; got != "f:pkg/b.go" {
		t.Fatalf("insertion changed cursor identity: got %q", got)
	}
	if got := c.navigation.entries[c.navigation.top].id; got != "f:pkg/a.go" {
		t.Fatalf("insertion changed viewport-top identity: got %q", got)
	}
}

func TestLiveDiffNavigationViewportAndFileSelection(t *testing.T) {
	files := make([]liveDiffFile, 10)
	for i := range files {
		files[i] = navigationFile(string(rune('a'+i)) + ".go")
	}
	c := &liveDiffTerminalController{
		files: files, workspace: "/workspace", rows: 6,
		view: liveDiffView{Files: files, Selected: 0, Following: true},
	}
	c.navigation.flat = true
	c.navigation.rebuild(files, c.workspace)
	c.navigation.cursor = 7
	c.navigation.ensureVisible(c.rows)
	if c.navigation.cursor < c.navigation.top || c.navigation.cursor >= c.navigation.top+c.rows-2 {
		t.Fatalf("cursor is outside the independently scrolled navigator: cursor=%d top=%d", c.navigation.cursor, c.navigation.top)
	}
	if c.navigation.top != 4 {
		t.Fatalf("viewport did not move enough to reveal cursor: top=%d", c.navigation.top)
	}

	c.view.Selected = 0
	c.stepFile(1)
	if c.view.Selected != 1 || c.view.Following {
		t.Fatalf("file navigation did not select the next matched file and leave follow mode: selected=%d following=%t",
			c.view.Selected, c.view.Following)
	}
	if id := c.navigation.entries[c.navigation.cursor].id; id != "f:b.go" {
		t.Fatalf("file navigation did not reveal the selected path: cursor=%q", id)
	}
}

func TestLiveDiffNavigationRenderIsSafeAndWidthBounded(t *testing.T) {
	for _, tc := range []struct{ terminal, want int }{
		{99, 0}, {100, 25}, {120, 30}, {144, 34}, {200, 34},
	} {
		var nav liveDiffNavigation
		if got := nav.width(tc.terminal); got != tc.want {
			t.Errorf("navigator width at terminal width %d: got %d, want %d", tc.terminal, got, tc.want)
		}
	}
	hidden := liveDiffNavigation{hidden: true}
	if got := hidden.width(160); got != 0 {
		t.Fatalf("explicitly hidden navigator still occupies %d columns", got)
	}

	path := "docs/界面/\x1b]0;not-a-terminal-title\x07guide.go"
	files := []liveDiffFile{navigationFile(path)}
	var nav liveDiffNavigation
	nav.flat = true
	nav.rebuild(files, "")
	rendered := nav.render(files, []livediff.Counts{{Added: -1, Removed: -1}}, 0, 32, 6, livediff.DarkTheme)
	if len(rendered) != 6 {
		t.Fatalf("renderer returned %d rows for a six-row viewport", len(rendered))
	}
	for i, row := range rendered {
		if width := ansi.StringWidth(row); width > 32 {
			t.Errorf("rendered row %d exceeds its terminal width: width=%d row=%q", i, width, row)
		}
		if strings.Contains(row, "\x1b]") || strings.ContainsRune(row, '\a') || strings.Contains(row, "not-a-terminal-title") {
			t.Errorf("path content escaped terminal sanitization: %q", row)
		}
	}
	rows := strings.Join(rendered, "\n")
	if !strings.Contains(rows, "?") || strings.Contains(rows, "+0 -0") {
		t.Fatalf("unknown line counts should remain unknown, not appear as zero: %q", rows)
	}
}

func TestLiveDiffNavigationExternalAbsolutePaths(t *testing.T) {
	files := []liveDiffFile{navigationFile("/a.go"), navigationFile("/tmp/a.go"), navigationFile("/var/b.go")}
	c := &liveDiffTerminalController{files: files, workspace: "/workspace", rows: 12, view: liveDiffView{Files: files, Selected: 2}}
	n := &c.navigation
	n.rebuild(files, c.workspace)
	if n.entries[0].file != -1 || n.entries[0].folder != "/" || n.entries[0].label != "/" {
		t.Fatalf("absolute root missing: %+v", n.entries[0])
	}
	n.render(files, nil, 2, 32, 12, livediff.DarkTheme)
	c.openNavEntry()
	if !n.collapsed["/"] || len(n.entries) != 1 {
		t.Fatal("absolute root did not collapse")
	}
	c.revealFile()
	if n.collapsed["/"] || n.entries[n.cursor].file != 2 {
		t.Fatal("absolute destination was not revealed")
	}
}

func TestLiveDiffNavigationFilteredRevealPreservesTree(t *testing.T) {
	files := []liveDiffFile{navigationFile("pkg/a.go"), navigationFile("pkg/b.go"), navigationFile("z.go")}
	c := &liveDiffTerminalController{files: files, rows: 12, view: liveDiffView{Files: files, Selected: 2}}
	n := &c.navigation
	n.collapsed = map[string]bool{"pkg": true}
	n.rebuild(files, "")
	c.navigationKey('/')
	for _, key := range []byte("b.go") {
		c.editFilter(key)
	}
	c.openNavEntry()
	c.revealFile() // renderFrame reveals a newly selected file after Enter.
	if !n.collapsed["pkg"] {
		t.Fatal("filtered selection expanded saved tree")
	}
	if n.filtering {
		t.Fatal("Enter did not end text entry")
	}
	c.navigationKey(21)
	if n.query != "" || !n.collapsed["pkg"] || len(n.entries) != 2 {
		t.Fatal("Ctrl-U outside text entry did not restore collapsed tree")
	}
}

func TestLiveDiffNavigationScrollbarPreservesCounts(t *testing.T) {
	files := []liveDiffFile{navigationFile("a.go"), navigationFile("b.go"), navigationFile("c.go")}
	var nav liveDiffNavigation
	nav.rebuild(files, "")
	rows := nav.render(files, []livediff.Counts{{Added: 123, Removed: 456}}, 0, 28, 3, livediff.DarkTheme)
	row := ansi.Strip(rows[2])
	if !strings.Contains(row, "+123 -456") || !strings.HasSuffix(row, "┃") {
		t.Fatalf("scrollbar consumed counts: %q", row)
	}
}

func TestLiveDiffNavigationColoredInlineStatusAndStats(t *testing.T) {
	files := []liveDiffFile{
		navigationFile("modified.go"),
		navigationFile("added.go"),
		navigationFile("deleted.go"),
		navigationFile("renamed.go"),
	}
	files[1].Chunks[0].Review.BeforePath = ""
	files[1].Highlighted = true
	files[2].Chunks[0].Review.AfterPath = ""
	files[3].Chunks[0].Review = mekugi.RenderReviewFile("was.go", "renamed.go", "a\n", "b\n")
	var nav liveDiffNavigation
	nav.flat = true
	nav.rebuild(files, "")
	counts := []livediff.Counts{{Added: 5, Removed: 2}, {Added: 3, Removed: 0}, {Added: 0, Removed: 7}, {Added: 1, Removed: 1}}
	rows := nav.render(files, counts, 0, 34, 6, livediff.DarkTheme)
	if heading := ansi.Strip(rows[0]); heading != " Files  4/4 · flat" {
		t.Errorf("navigator repeats the Files title or loses its counts: %q", heading)
	}
	for i, want := range []string{"A added.go +3 -0", "D deleted.go +0 -7", "M modified.go +5 -2", "RM was.go → renamed.go +1 -1"} {
		row := rows[i+2]
		if plain := ansi.Strip(row); !strings.Contains(plain, want) {
			t.Errorf("row %d lacks inline status/stats: %q", i, plain)
		}
		if strings.Contains(row, "●") {
			t.Errorf("row %d shows a redundant update dot: %q", i, row)
		}
		if !strings.Contains(row, "\x1b[38;2;") {
			t.Errorf("row %d lacks colored status/stats: %q", i, row)
		}
	}
}

func TestLiveDiffNavigationDockAndPickerKeys(t *testing.T) {
	files := []liveDiffFile{navigationFile("a.go")}
	c := &liveDiffTerminalController{files: files, lastWidth: 120, view: liveDiffView{Files: files, Following: true}}
	c.navigation.rebuild(files, "")
	c.navigationKey('/')
	c.editFilter('\r')
	if !c.navigation.focused || c.navigation.filtering || c.view.Selected != 0 {
		t.Fatal("empty search Enter did not leave the file list ready for keyboard navigation")
	}
	c.navigationKey('s')
	if !c.navigation.hidden || c.navigation.focused || c.navigation.width(120) != 0 {
		t.Fatal("s did not hide the wide dock")
	}
	c.navigationKey('s')
	if c.navigation.hidden || c.navigation.width(120) == 0 {
		t.Fatal("s did not restore the wide dock")
	}
	c.lastWidth = 70
	c.view.Following = true
	c.navigationKey('s')
	if !c.navigation.focused || c.navigation.hidden || c.navigation.width(70) != 0 || c.view.Following {
		t.Fatal("first s did not open the narrow picker")
	}
	c.navigationKey('j')
	c.view.Merge([]liveDiffFile{files[0], navigationFile("new.go")})
	if c.view.Selected != 0 || c.view.Following {
		t.Fatal("incoming update moved the file under the narrow picker")
	}
	c.navigationKey('s')
	if c.navigation.focused {
		t.Fatal("second s did not close the narrow picker")
	}
}

func TestLiveDiffNavigationFolderArrow(t *testing.T) {
	files := []liveDiffFile{navigationFile("pkg/a.go")}
	var nav liveDiffNavigation
	nav.rebuild(files, "")
	row := nav.render(files, nil, 0, 25, 4, livediff.DarkTheme)[2]
	if !strings.Contains(ansi.Strip(row), "▼ pkg (1)") || !strings.Contains(row, "\x1b[38;2;") {
		t.Fatalf("expanded folder is not prominent: %q", row)
	}
	nav.collapsed = map[string]bool{"pkg": true}
	nav.rebuild(files, "")
	if row = ansi.Strip(nav.render(files, nil, 0, 25, 4, livediff.DarkTheme)[2]); !strings.Contains(row, "▶ pkg (1)") {
		t.Fatalf("collapsed folder arrow missing: %q", row)
	}
}

func TestLiveDiffNavigationSelectionUsesRowFill(t *testing.T) {
	files := []liveDiffFile{navigationFile("a.go"), navigationFile("b.go")}
	var nav liveDiffNavigation
	nav.flat = true
	nav.rebuild(files, "")
	nav.focused = true
	nav.cursor = 1
	rows := nav.render(files, nil, 0, 16, 4, livediff.DarkTheme)
	for i, row := range rows[2:] {
		if i == 0 {
			if strings.Contains(row, livediff.DarkTheme.SelectionBackground()) {
				t.Fatalf("open file should not compete with focused cursor: %q", row)
			}
			continue
		}
		if !strings.HasPrefix(row, livediff.DarkTheme.SelectionBackground()) ||
			!strings.HasSuffix(row, "\x1b[49m") || ansi.StringWidth(row) != 15 {
			t.Fatalf("selected and focused rows should fill the available width: %q", row)
		}
		if strings.HasPrefix(ansi.Strip(row), ">") || strings.HasPrefix(ansi.Strip(row), "▎") {
			t.Fatalf("row retained a marker column: %q", row)
		}
	}
	nav.focused = false
	rows = nav.render(files, nil, 0, 16, 4, livediff.DarkTheme)
	if !strings.HasPrefix(rows[2], livediff.DarkTheme.SelectionBackground()) ||
		strings.Contains(rows[3], livediff.DarkTheme.SelectionBackground()) {
		t.Fatalf("unfocused file selection = %q", rows)
	}
}

// A compacted folder chain that splits keeps its new ancestor in view, and
// rows are never scrolled off while space remains below.
func TestLiveDiffNavigationChainSplitKeepsAncestor(t *testing.T) {
	var nav liveDiffNavigation
	files := []liveDiffFile{navigationFile("/w/internal/broker/a.go"), navigationFile("/w/internal/broker/b.go")}
	nav.rebuild(files, "/w")
	if got := nav.entries[0].label; got != "internal/broker" {
		t.Fatalf("compacted chain = %q", got)
	}
	files = append(files, navigationFile("/w/internal/pane/c.go"))
	nav.rebuild(files, "/w")
	if nav.top != 0 || nav.entries[0].label != "internal" {
		t.Fatalf("after split top = %d (%q)", nav.top, nav.entries[nav.top].label)
	}
	nav.top = 3
	if row := ansi.Strip(nav.render(files, nil, 0, 34, 10, livediff.DarkTheme)[2]); nav.top != 0 || !strings.Contains(row, "internal") {
		t.Fatalf("fitting tree scrolled to %d, first row %q", nav.top, row)
	}
}
