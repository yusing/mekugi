package router

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

func liveDiffHighlightChunk(key, path, hunk string) liveDiffChunk {
	diff := "--- " + strconv.Quote(path) + "\n+++ " + strconv.Quote(path) + "\n" + hunk
	return liveDiffChunk{Key: key, Review: mekugi.ReviewFile{BeforePath: path, AfterPath: path, Diff: diff}}
}

func TestLiveDiffHighlightBatchAndRepeatedSnapshot(t *testing.T) {
	first := liveDiffHighlightChunk("first", "a", "@@ -1 +1 @@\n-a\n+A\n")
	second := liveDiffHighlightChunk("second", "a", "@@ -20 +20 @@\n-b\n+B\n")
	other := liveDiffHighlightChunk("other", "b", "@@ -1 +1 @@\n-c\n+C\n")
	v := liveDiffView{Following: true, Scroll: make(map[string]int)}
	snapshot := []liveDiffFile{{Path: "a", Chunks: []liveDiffChunk{first}}}
	v.Merge(snapshot)
	v.RefreshVisible()
	if v.Visible[v.Files[0].Key()].Highlighted || v.UnseenUpdate {
		t.Fatal("initial history was labeled as a new update")
	}
	v.Following = false
	snapshot[0].Chunks = append(snapshot[0].Chunks, second)
	snapshot = append(snapshot, liveDiffFile{Path: "b", Chunks: []liveDiffChunk{other}})
	v.Merge(snapshot)
	v.RefreshVisible()
	for _, file := range v.Files {
		if !v.Visible[file.Key()].Highlighted {
			t.Fatalf("multi-file update omitted %s", file.Path)
		}
	}
	if !v.UnseenUpdate || v.Selected != 0 {
		t.Fatal("paused update lost its notice or stole selection")
	}
	visible := v.Visible[v.Files[0].Key()]
	if len(visible.Chunks) != 2 || visible.Chunks[0].Highlighted || !visible.Chunks[1].Highlighted {
		t.Fatalf("changes-observed capture highlight = %#v", visible)
	}
	v.Merge(snapshot)
	v.RefreshVisible()
	for _, file := range v.Files {
		if !v.Visible[file.Key()].Highlighted {
			t.Fatal("repeated snapshot cleared the previous batch")
		}
	}
	visible = v.Visible[v.Files[0].Key()]
	if len(visible.Chunks) != 2 || visible.Chunks[0].Highlighted || !visible.Chunks[1].Highlighted {
		t.Fatalf("repeated snapshot changed the highlighted capture: %#v", visible)
	}
	third := liveDiffHighlightChunk("third", "c", "@@ -1 +1 @@\n-d\n+D\n")
	snapshot = append(snapshot, liveDiffFile{Path: "c", Chunks: []liveDiffChunk{third}})
	v.Merge(snapshot)
	v.RefreshVisible()
	for _, file := range v.Files {
		visible := v.Visible[file.Key()]
		if visible.Highlighted != (file.Path == "c") {
			t.Fatalf("latest batch left stale cached marks on %s", file.Path)
		}
		for _, chunk := range visible.Chunks {
			if chunk.Highlighted != (file.Path == "c") {
				t.Fatalf("latest batch left stale hunk marks on %s", file.Path)
			}
		}
	}
	v.FollowLatest()
	if v.UnseenUpdate {
		t.Fatal("resume retained the unseen-update notice")
	}
	restarted := liveDiffView{}
	restarted.Merge(snapshot)
	restarted.RefreshVisible()
	for _, file := range restarted.Files {
		if restarted.Visible[file.Key()].Highlighted {
			t.Fatal("fresh viewer revived old recency state")
		}
	}
}

func TestLiveDiffHighlightEmptyStartAndRevert(t *testing.T) {
	v := liveDiffView{Following: true}
	v.Merge(nil)
	first := liveDiffHighlightChunk("first", "a", "@@ -1 +1 @@\n-a\n+A\n")
	snapshot := []liveDiffFile{{Path: "a", Chunks: []liveDiffChunk{first}}}
	v.Merge(snapshot)
	v.RefreshVisible()
	if !v.Visible[v.Files[0].Key()].Highlighted {
		t.Fatal("first edit after an empty baseline was not highlighted")
	}
	revert := liveDiffHighlightChunk("revert", "a", "@@ -1 +1 @@\n-A\n+a\n")
	snapshot[0].Chunks = append(snapshot[0].Chunks, revert)
	v.Following = false
	v.Merge(snapshot)
	v.RefreshVisible()
	visible := v.Visible[v.Files[0].Key()]
	if !visible.Highlighted || len(visible.Chunks) != 0 {
		t.Fatalf("full revert should mark the file without inventing a hunk: %#v", visible)
	}
}

func TestLiveDiffRenderHighlights(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	old := liveDiffHighlightChunk("old", path, "@@ -1 +1 @@\n-old marker\n+OLDER\n")
	recent := liveDiffHighlightChunk("recent", path, "@@ -20 +20 @@\n-before\n+NEW 界 é 👩‍💻\n")
	recent.Highlighted = true
	file := liveDiffFile{Path: path, Highlighted: true, Chunks: []liveDiffChunk{old, recent}}
	for _, width := range []int{36, 90} {
		render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, width, 0, recent)
		if err != nil {
			t.Fatal(err)
		}
		sawOld, sawNew, sawBold := false, false, false
		for _, line := range render.Lines {
			plain := ansi.Strip(line)
			if !utf8.ValidString(line) {
				t.Fatal("decoration damaged Unicode")
			}
			if strings.Contains(plain, "OLDER") {
				sawOld = true
				if strings.HasPrefix(plain, "▎ ") {
					t.Fatal("older hunk acquired the latest-update gutter")
				}
			}
			if strings.Contains(plain, "NEW") {
				sawNew = true
				if !strings.HasPrefix(plain, "▎ ") || !strings.Contains(line, "\x1b[36m") {
					t.Fatalf("new hunk lacks its colored gutter: %q", line)
				}
			}
			if strings.Contains(plain, "file.txt") && strings.Contains(line, "\x1b[1m") {
				sawBold = true
			}
		}
		if !sawOld || !sawNew || !sawBold {
			t.Fatalf("width %d: missing old/new/label: %t/%t/%t\n%s", width, sawOld, sawNew, sawBold, strings.Join(render.Lines, "\n"))
		}
	}
}

func TestLiveDiffSafePreservesZeroWidthText(t *testing.T) {
	const source = "e\u0301 界 👩\u200d💻 क\u094d\u200cष ✈\ufe0f"
	for _, colors := range []bool{false, true} {
		got := livediff.Safe("\x1b]52;c;clipboard\a\x1b[2J"+source+"\x00\x1b[6n\n", colors)
		if got != source+"\n" {
			t.Fatalf("colors=%t: source text damaged or terminal controls retained: %q", colors, got)
		}
	}
}

func TestLiveDiffHighlightPreservesSyntaxColors(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.go")
	chunk := liveDiffHighlightChunk("recent", path, "@@ -1 +1 @@\n-return \"before\"\n+return \"after\"\n")
	codeLine := func(highlighted bool) string {
		t.Helper()
		chunk.Highlighted = highlighted
		file := liveDiffFile{Path: path, Chunks: []liveDiffChunk{chunk}}
		render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, 90, 0, chunk)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range render.Lines {
			if strings.Contains(ansi.Strip(line), `return "after"`) {
				if highlighted {
					return strings.TrimPrefix(line, "\x1b[36m▎\x1b[0m ")
				}
				return strings.TrimPrefix(line, "  ")
			}
		}
		t.Fatalf("missing syntax-colored source line: %q", strings.Join(render.Lines, "\n"))
		return ""
	}
	plain, marked := codeLine(false), codeLine(true)
	if !strings.Contains(plain, "\x1b[") || plain != marked {
		t.Fatalf("gutter changed syntax styling: plain=%q marked=%q", plain, marked)
	}
}

func TestLiveDiffHeaders(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.go")
	for _, tc := range []struct {
		name, before, after, hunk, action string
		added, removed                    int
	}{
		{"edit", path, path, "@@ -1 +1 @@\n-before\n+after\n", "", 1, 1},
		{"new", "", path, "@@ -0,0 +1 @@\n+after\n", "New file", 1, 0},
		{"deleted", path, "", "@@ -1 +0,0 @@\n-before\n", "Deleted file", 0, 1},
		{"renamed", filepath.Join(workspace, "old.go"), path, "@@ -1 +1 @@\n-before\n+after\n", "old.go → file.go", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := strconv.Quote(tc.before), strconv.Quote(tc.after)
			if tc.before == "" {
				before = "/dev/null"
			}
			if tc.after == "" {
				after = "/dev/null"
			}
			diff := "--- " + before + "\n+++ " + after + "\n" + tc.hunk
			chunk := liveDiffChunk{Review: mekugi.ReviewFile{
				BeforePath: tc.before, AfterPath: tc.after, Diff: diff,
			}}
			file := liveDiffFile{Path: path, Chunks: []liveDiffChunk{chunk}}
			render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, 90, 0, chunk)
			if err != nil {
				t.Fatal(err)
			}
			text := ansi.Strip(strings.Join(render.Lines, "\n"))
			pathCount := 1
			if strings.Count(text, "file.go") != pathCount || strings.Contains(text, "/dev/null") ||
				strings.Contains(text, "Applied") || strings.Contains(text, "┐") ||
				strings.Contains(text, "└") || strings.Contains(text, "⟶") {
				t.Fatalf("duplicate or decorative header remains:\n%s", text)
			}
			headingPath := "file.go"
			if tc.name == "renamed" {
				headingPath = "old.go → file.go"
			}
			if !strings.Contains(render.Lines[0], "\x1b[1m1/1  "+headingPath) ||
				!strings.Contains(render.Lines[0], "─") || ansi.StringWidth(render.Lines[0]) != 89 {
				t.Fatalf("file heading lacks bounded emphasis: %q", render.Lines[0])
			}
			if !strings.Contains(render.Lines[0], livediff.TerminalTheme.Foreground(chroma.GenericInserted)+"+"+strconv.Itoa(tc.added)+"\x1b[39m") ||
				!strings.Contains(render.Lines[0], livediff.TerminalTheme.Foreground(chroma.GenericDeleted)+"-"+strconv.Itoa(tc.removed)+"\x1b[39m") {
				t.Fatalf("missing green/red source-line counts: %q", render.Lines[0])
			}

			if tc.action != "" && !strings.Contains(text, tc.action) {
				t.Fatalf("file heading lost the file action %q:\n%s", tc.action, text)
			}
		})
	}
}

func TestLiveDiffHeaderWidthAndControls(t *testing.T) {
	for _, width := range []int{0, 1, 2, 10, 36, 90} {
		header := livediff.Header("1/2  界 é.go\x1b]52;c;clipboard\a", width, livediff.Counts{Added: 12, Removed: 4}, livediff.TerminalTheme)
		if !utf8.ValidString(header) || ansi.StringWidth(header) > width || strings.Contains(header, "clipboard") {
			t.Fatalf("width %d: unsafe or overflowing header: %q", width, header)
		}
	}
}

func TestLiveDiffHeaderCountsUseVisibleComposition(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	first := liveDiffHighlightChunk("first", path, "@@ -1 +1 @@\n-a\n+A\n")
	second := liveDiffHighlightChunk("second", path, "@@ -1 +1,2 @@\n-A\n+B\n+C\n")
	v := liveDiffView{}
	snapshot := []liveDiffFile{{Path: path, Chunks: []liveDiffChunk{first}}}
	v.Merge(snapshot)
	snapshot[0].Chunks = append(snapshot[0].Chunks, second)
	v.Merge(snapshot)
	v.RefreshVisible()
	for _, want := range []livediff.Counts{{Added: 2, Removed: 1}} {
		file := v.Visible[v.Files[0].Key()]
		render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, 90, 0, second)
		if err != nil {
			t.Fatal(err)
		}
		if len(render.Counts) != 1 || render.Counts[0] != want {
			t.Fatalf("counts = %v, want visible net counts %v", render.Counts, want)
		}
	}
}

func TestLiveDiffPreparedRenameKeepsDestination(t *testing.T) {
	workspace := t.TempDir()
	oldPath, newPath := filepath.Join(workspace, "old.go"), filepath.Join(workspace, "new.go")
	diff := "--- " + strconv.Quote(oldPath) + "\n+++ " + strconv.Quote(newPath) +
		"\n@@ -1 +1 @@\n-before\n+after\n@@ -20 +20 @@\n-older\n+newer\n"
	chunk := liveDiffChunk{
		Key:    "rename",
		Review: mekugi.ReviewFile{BeforePath: oldPath, AfterPath: newPath, Diff: diff},
	}
	v := liveDiffView{}
	v.Merge([]liveDiffFile{{Path: oldPath, Chunks: []liveDiffChunk{chunk}}})
	v.RefreshVisible()
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{v.Visible[v.Files[0].Key()]}, workspace, 90, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if strings.Count(text, "old.go") != 1 || strings.Count(text, "new.go") != 1 || strings.Contains(text, "Rename:") {
		t.Fatalf("rename header repeats its paths: %s", text)
	}
	for _, want := range []string{"1/1  old.go", "old.go → new.go", "after"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prepared rename lost %q:\n%s", want, text)
		}
	}
	for _, label := range []string{"old.go → new.go"} {
		if strings.Count(text, label) != 1 {
			t.Fatalf("capture label repeated between hunks: %q\n%s", label, text)
		}
	}
}

func TestLiveDiffNewFileRegionsStayCompact(t *testing.T) {
	workspace := t.TempDir()
	rows := make([]string, 340)
	for i := range rows {
		rows[i] = "line" + strconv.Itoa(i+1)
	}
	for _, i := range []int{0, 312, 335, 339} {
		rows[i] = ""
	}
	// Retained creation captures still render, although stock apply_patch also edits existing files.
	path := filepath.Join(workspace, "review_highlight_test.txt")
	initial := liveDiffChunk{
		Key: "create", Review: mekugi.ReviewFile{
			AfterPath: path,
			Diff: "--- /dev/null\n+++ " + strconv.Quote(path) +
				"\n@@ -0,0 +1,311 @@\n+" + strings.Join(rows[:311], "\n+") + "\n",
		},
	}

	recent := liveDiffHighlightChunk("edit", path,
		"@@ -312 +312 @@\n-line312\n+LATEST312\n@@ -337 +337 @@\n-line337\n+LATEST337\n")
	v := liveDiffView{}
	snapshot := []liveDiffFile{{Path: path, Chunks: []liveDiffChunk{initial}}}
	// Appends preserve the original /dev/null identity while growing the file.
	start := 311
	for _, end := range []int{312, 336, 337, 340} {
		hunk := fmt.Sprintf("@@ -%d,0 +%d,%d @@\n+%s\n", start, start+1, end-start, strings.Join(rows[start:end], "\n+"))
		snapshot[0].Chunks = append(snapshot[0].Chunks,
			liveDiffHighlightChunk("append-"+strconv.Itoa(end), path, hunk))
		start = end
	}
	v.Merge(snapshot)
	snapshot[0].Chunks = append(snapshot[0].Chunks, recent)
	v.Merge(snapshot)
	v.RefreshVisible()
	file := v.Visible[v.Files[0].Key()]
	if len(file.Chunks) < 3 {
		t.Fatalf("fixture has %d composed regions, want at least 3", len(file.Chunks))
	}
	for _, chunk := range file.Chunks {
		if chunk.Status != "" || chunk.Review.BeforePath != "" {
			t.Fatalf("fixture is not a composed new file: %#v", chunk)
		}
	}
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, 100, 0, recent)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	for _, label := range []string{"New file", "review_highlight_test.txt"} {
		if strings.Count(text, label) != 1 || !strings.Contains(ansi.Strip(render.Lines[0]), label) {
			t.Fatalf("file label is missing from its header or repeats between hunks: %q\n%s", label, text)
		}
	}
	if len(render.Lines) != len(rows)+1 || render.Counts[0] != (livediff.Counts{Added: 340, Removed: 0}) {
		t.Fatalf("extra heading rows or lost blank source rows: rows=%d counts=%v\n%s",
			len(render.Lines), render.Counts, text)
	}
	if render.FocusOffset != 337 {
		t.Fatalf("follow offset = %d, want the latest source row 337", render.FocusOffset)
	}
	for i := 1; i <= len(rows); i++ {
		plain := ansi.Strip(render.Lines[i])
		if !strings.Contains(plain, strconv.Itoa(i)) {
			t.Fatalf("source row %d lost its inline number: %q", i, plain)
		}
		if marked := strings.HasPrefix(plain, "▎ "); marked != (i == 312 || i == 337) {
			t.Fatalf("source row %d has incorrect recency: %q", i, plain)
		}
	}
}

func TestLiveDiffFollowLatestCombinedResult(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	old := liveDiffHighlightChunk("amber1", path, "@@ -20 +20 @@\n-before\n+FIRST20\n")
	recent := liveDiffHighlightChunk("apple1", path,
		"@@ -20 +20 @@\n-FIRST20\n+LATEST20\n@@ -337 +337 @@\n-before\n+LATEST337\n")
	v := liveDiffView{Following: true}
	v.Merge([]liveDiffFile{{Path: path, Chunks: []liveDiffChunk{old}}})
	v.Merge([]liveDiffFile{{Path: path, Chunks: []liveDiffChunk{old, recent}}})
	v.RefreshVisible()
	file := v.Visible[v.Files[0].Key()]
	text := liveDiffVisibleText(file)
	if len(file.Chunks) != 2 || strings.Contains(text, "FIRST20") ||
		!strings.Contains(text, "+LATEST20") || !strings.Contains(text, "+LATEST337") {
		t.Fatalf("result contains intermediate patches instead of final changes: %s", text)
	}
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, 100, 0, v.LatestChunk())
	if err != nil {
		t.Fatal(err)
	}
	focused := ansi.Strip(strings.Join(render.Lines[render.FocusOffset:], "\n"))
	if !strings.Contains(focused, "LATEST337") || strings.Contains(focused, "LATEST20") {
		t.Fatalf("follow selected an older capture or hunk: %q", focused)
	}
	text = ansi.Strip(strings.Join(render.Lines, "\n"))
	if strings.Contains(text, "LATEST UPDATE") || render.Counts[0] != (livediff.Counts{Added: 2, Removed: 2}) {
		t.Fatalf("unexpected update label or incorrect counts: counts=%v\n%s", render.Counts, text)
	}
	for _, line := range render.Lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "LATEST20") || strings.Contains(plain, "LATEST337") {
			if !strings.HasPrefix(plain, "▎ ") {
				t.Fatalf("current capture lost its highlight: %q", line)
			}
		}
	}
}

func TestLiveDiffOnlyShowsFinalComposition(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	first := liveDiffHighlightChunk("amber1", path,
		"@@ -1 +1 @@\n-old1\n+first1\n@@ -20 +20 @@\n-old20\n+first20\n")
	second := liveDiffHighlightChunk("amber2", path,
		"@@ -1 +1 @@\n-first1\n+second1\n@@ -20 +20 @@\n-first20\n+second20\n")
	v := liveDiffView{}
	v.Merge([]liveDiffFile{{Path: path, Chunks: []liveDiffChunk{first, second}}})
	v.RefreshVisible()
	file := v.Visible[v.Files[0].Key()]
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, 90, 0, second)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	for _, want := range []string{"second1", "second20"} {
		if !strings.Contains(text, want) {
			t.Fatalf("final edit missing %q: %s", want, text)
		}
	}
	for _, stale := range []string{"first1", "first20", "Unable to combine", "changes observed"} {
		if strings.Contains(text, stale) {
			t.Fatalf("intermediate content %q visible: %s", stale, text)
		}
	}
}

func TestLiveDiffConflictingSavedEditsRemainReadable(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	first := liveDiffHighlightChunk("amber1", path, "@@ -1 +1 @@\n-old\n+first\n")
	second := liveDiffHighlightChunk("amber2", path, "@@ -1 +1 @@\n-different\n+second\n")
	v := liveDiffView{}
	v.Merge([]liveDiffFile{{Path: path, Chunks: []liveDiffChunk{first, second}}})
	v.RefreshVisible()
	file := v.Visible[v.Files[0].Key()]
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file}, workspace, 90, 0, second)
	if err != nil {
		t.Fatal(err)
	}
	visible := ansi.Strip(strings.Join(render.Lines, "\n"))
	for _, want := range []string{"first", "second", "Separate edit"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("conflicting capture lost %q: %s", want, visible)
		}
	}
	if strings.Contains(visible, "Unable to combine") || strings.Contains(visible, "changes observed") {
		t.Fatalf("conflicting capture exposed obsolete state: %s", visible)
	}
}

func TestLiveDiffBlankSourceRows(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	chunk := liveDiffHighlightChunk("edit", path, "@@ -1,4 +1,4 @@\n \n-old\n+new\n-\n+\n tail\n")
	chunk.Status = ""
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: path, Chunks: []liveDiffChunk{chunk}}}, workspace, 90, 0, chunk)

	if err != nil {
		t.Fatal(err)
	}
	wantRows := 7
	if len(render.Lines) != wantRows || render.Counts[0] != (livediff.Counts{Added: 2, Removed: 2}) {
		t.Fatalf("blank context/added/removed rows changed: rows=%d counts=%v\n%s",
			len(render.Lines), render.Counts, strings.Join(render.Lines, "\n"))
	}
	for _, line := range render.Lines[1:] {
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			t.Fatalf("blank source row lacks inline numbers: %q", line)
		}
	}
}

func TestLiveDiffPathOnlyChangesStayCompact(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	for _, tc := range []struct {
		name, before, after, action string
	}{
		{"new", "", path, "New file"},
		{"deleted", path, "", "Deleted file"},
		{"renamed", filepath.Join(workspace, "old.txt"), path, "old.txt → file.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := mekugi.ReviewFile{BeforePath: tc.before, AfterPath: tc.after,
				Diff: tc.name + " " + strconv.Quote(tc.before) + " -> " + strconv.Quote(tc.after) + "\n"}
			for _, status := range []string{"", ""} {
				chunk := liveDiffChunk{Key: "change", Status: status, Review: review}
				render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: path, Chunks: []liveDiffChunk{chunk}}}, workspace, 100, 0, chunk)

				if err != nil {
					t.Fatal(err)
				}
				wantRows := 1
				if status != "" {
					wantRows++
				}
				text := ansi.Strip(strings.Join(render.Lines, "\n"))
				if len(render.Lines) != wantRows || strings.Count(text, tc.action) != 1 ||
					render.FocusOffset >= len(render.Lines) || render.Counts[0] != (livediff.Counts{}) {
					t.Fatalf("path-only action duplicated or lost: %+v\n%s", render, text)
				}
				if status != "" && strings.Count(text, status) != 1 {
					t.Fatalf("path-only action lost application uncertainty:\n%s", text)
				}
			}
		})
	}
}

func TestLiveDiffNarrowFileActions(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "review_highlight_test.txt")
	oldPath := filepath.Join(workspace, "previous_name.txt")
	for _, tc := range []struct {
		name, before, after, action string
	}{
		{"add", "", path, "New file"},
		{"delete", path, "", "Deleted file"},
		{"move", oldPath, path, "→"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := mekugi.ReviewFile{BeforePath: tc.before, AfterPath: tc.after,
				Diff: tc.name + " " + strconv.Quote(tc.before) + " -> " + strconv.Quote(tc.after) + "\n"}
			file := liveDiffFile{Path: path, Highlighted: true, Chunks: []liveDiffChunk{{Review: review}}}
			for _, width := range []int{40, 90} {
				render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{file, {Path: "next.txt", Chunks: []liveDiffChunk{{Status: "prepared"}}}}, workspace, width, 0, file.Chunks[0])

				if err != nil {
					t.Fatal(err)
				}
				lines := render.Lines[:render.Starts[1]]
				text := ansi.Strip(strings.Join(lines, "\n"))
				for _, label := range []string{tc.action, "+0", "-0"} {
					if strings.Count(text, label) != 1 {
						t.Fatalf("width %d: heading dropped or repeated %q:\n%s", width, label, text)
					}
				}
				for _, name := range []string{tc.before, tc.after} {
					if name != "" && !strings.Contains(text, filepath.Base(name)) {
						t.Fatalf("width %d: heading lost path %q:\n%s", width, name, text)
					}
				}
				if render.FocusOffset != 0 || (width == 40 && len(lines) == 1) {
					t.Fatalf("width %d: wrapped heading lost focus or its continuation: %+v", width, render)
				}
				for _, line := range lines {
					if !utf8.ValidString(line) || ansi.StringWidth(line) > width-1 || !strings.Contains(line, "\x1b[1m") {
						t.Fatalf("width %d: invalid wrapped heading: %q", width, line)
					}
				}
				view := liveDiffView{Files: []liveDiffFile{file, {Path: "next.txt", Chunks: []liveDiffChunk{{Status: "prepared"}}}}, Scroll: make(map[string]int)}
				view.ScrollTo(render, render.Starts[1])
				if view.Selected != 1 || view.Scroll["next.txt"] != 0 {
					t.Fatal("wrapped heading broke navigation to the next file")
				}
			}
		})
	}
}
