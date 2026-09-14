package router

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

func liveDiffHighlightChunk(key, path, hunk string, applied bool) liveDiffChunk {
	diff := "--- " + strconv.Quote(path) + "\n+++ " + strconv.Quote(path) + "\n" + hunk
	status := key + " prepared (application unconfirmed)"
	if applied {
		status = key + " applied"
	}
	return liveDiffChunk{
		key: key, diff: diff, status: status, applied: applied,
		review: mekugi.ReviewFile{BeforePath: path, AfterPath: path, Diff: diff},
	}
}

func TestLiveDiffHighlightBatchAndReceipts(t *testing.T) {
	first := liveDiffHighlightChunk("first", "a", "@@ -1 +1 @@\n-a\n+A\n", true)
	second := liveDiffHighlightChunk("second", "a", "@@ -20 +20 @@\n-b\n+B\n", false)
	other := liveDiffHighlightChunk("other", "b", "@@ -1 +1 @@\n-c\n+C\n", true)
	v := liveDiffView{following: true, scroll: make(map[string]int)}
	snapshot := []liveDiffFile{{path: "a", chunks: []liveDiffChunk{first}}}
	v.merge(snapshot)
	v.refreshVisible()
	if v.visible[v.files[0].key()].highlighted || v.unseenUpdate {
		t.Fatal("initial history was labeled as a new update")
	}
	v.following = false
	snapshot[0].chunks = append(snapshot[0].chunks, second)
	snapshot = append(snapshot, liveDiffFile{path: "b", chunks: []liveDiffChunk{other}})
	v.merge(snapshot)
	v.refreshVisible()
	for _, file := range v.files {
		if !v.visible[file.key()].highlighted {
			t.Fatalf("multi-file update omitted %s", file.path)
		}
	}
	if !v.unseenUpdate || v.selected != 0 {
		t.Fatal("paused update lost its notice or stole selection")
	}
	visible := v.visible[v.files[0].key()]
	if len(visible.chunks) != 2 || visible.chunks[0].highlighted || !visible.chunks[1].highlighted ||
		!strings.Contains(visible.chunks[1].status, "unconfirmed") {
		t.Fatalf("prepared capture highlight = %#v", visible)
	}
	snapshot[0].chunks[1].applied = true
	snapshot[0].chunks[1].status = "second applied"
	v.merge(snapshot)
	v.refreshVisible()
	for _, file := range v.files {
		if !v.visible[file.key()].highlighted {
			t.Fatal("receipt-only update cleared the previous batch")
		}
	}
	visible = v.visible[v.files[0].key()]
	if len(visible.chunks) != 2 || visible.chunks[0].highlighted || !visible.chunks[1].highlighted {
		t.Fatalf("receipt changed the highlighted capture: %#v", visible)
	}
	third := liveDiffHighlightChunk("third", "c", "@@ -1 +1 @@\n-d\n+D\n", true)
	snapshot = append(snapshot, liveDiffFile{path: "c", chunks: []liveDiffChunk{third}})
	v.merge(snapshot)
	v.refreshVisible()
	for _, file := range v.files {
		visible := v.visible[file.key()]
		if visible.highlighted != (file.path == "c") {
			t.Fatalf("latest batch left stale cached marks on %s", file.path)
		}
		for _, chunk := range visible.chunks {
			if chunk.highlighted != (file.path == "c") {
				t.Fatalf("latest batch left stale hunk marks on %s", file.path)
			}
		}
	}
	v.followLatest()
	if v.unseenUpdate {
		t.Fatal("resume retained the unseen-update notice")
	}
	v.flush(false)
	if v.visible[v.files[v.selected].key()].highlighted {
		t.Fatal("flush retained the latest file marker")
	}
	restarted := liveDiffView{}
	restarted.merge(snapshot)
	restarted.refreshVisible()
	for _, file := range restarted.files {
		if restarted.visible[file.key()].highlighted {
			t.Fatal("fresh viewer revived old recency state")
		}
	}
}

func TestLiveDiffHighlightEmptyStartAndRevert(t *testing.T) {
	v := liveDiffView{following: true}
	v.merge(nil)
	first := liveDiffHighlightChunk("first", "a", "@@ -1 +1 @@\n-a\n+A\n", true)
	snapshot := []liveDiffFile{{path: "a", chunks: []liveDiffChunk{first}}}
	v.merge(snapshot)
	v.refreshVisible()
	if !v.visible[v.files[0].key()].highlighted {
		t.Fatal("first edit after an empty baseline was not highlighted")
	}
	revert := liveDiffHighlightChunk("revert", "a", "@@ -1 +1 @@\n-A\n+a\n", true)
	snapshot[0].chunks = append(snapshot[0].chunks, revert)
	v.following = false
	v.merge(snapshot)
	v.refreshVisible()
	visible := v.visible[v.files[0].key()]
	if !visible.highlighted || len(visible.chunks) != 0 {
		t.Fatalf("full revert should mark the file without inventing a hunk: %#v", visible)
	}
	v.flush(false)
	if v.unseenUpdate || v.visible[v.files[0].key()].highlighted {
		t.Fatal("flushing the last marked file retained the update notice")
	}
}

func TestLiveDiffHighlightCapturesAndFlushedReceipt(t *testing.T) {
	first := liveDiffHighlightChunk("first", "a", "@@ -1 +1 @@\n-a\n+A\n", false)
	v := liveDiffView{}
	v.merge(nil)
	snapshot := []liveDiffFile{{path: "a", chunks: []liveDiffChunk{first}}}
	v.merge(snapshot)
	v.refreshVisible()
	v.flush(true)
	snapshot[0].chunks[0].applied = true
	v.merge(snapshot)
	v.refreshVisible()
	if len(v.visible[v.files[0].key()].chunks) != 0 || v.unseenUpdate {
		t.Fatal("late confirmation revived a flushed capture")
	}
	other := liveDiffHighlightChunk("other", "a", "@@ -20 +20 @@\n-b\n+B\n", true)
	snapshot[0].chunks = append(snapshot[0].chunks, other)
	v.merge(snapshot)
	v.refreshVisible()
	visible := v.visible[v.files[0].key()]
	if len(visible.chunks) != 1 || !strings.Contains(visible.chunks[0].diff, "-b\n+B\n") || !visible.chunks[0].highlighted {
		t.Fatalf("new capture lost its highlight or revived unrelated flushed history: %#v", visible)
	}
}

func TestLiveDiffRenderHighlights(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	old := liveDiffHighlightChunk("old", path, "@@ -1 +1 @@\n-old marker\n+OLDER\n", true)
	recent := liveDiffHighlightChunk("recent", path, "@@ -20 +20 @@\n-before\n+NEW 界 é 👩‍💻\n", true)
	recent.highlighted = true
	file := liveDiffFile{path: path, highlighted: true, chunks: []liveDiffChunk{old, recent}}
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
			for _, width := range []int{36, 90} {
				render, err := renderLiveDiff(t.Context(), []liveDiffFile{file}, delta, workspace, width, 0, recent)
				if err != nil {
					t.Fatal(err)
				}
				sawOld, sawNew, sawBold := false, false, false
				for _, line := range render.lines {
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
					assertLiveDiffNoBackground(t, line)
				}
				if !sawOld || !sawNew || !sawBold {
					t.Fatalf("width %d: missing old/new/label: %t/%t/%t\n%s", width, sawOld, sawNew, sawBold, strings.Join(render.lines, "\n"))
				}
			}
		})
	}
}

func TestLiveDiffSafePreservesZeroWidthText(t *testing.T) {
	const source = "e\u0301 界 👩\u200d💻 क\u094d\u200cष ✈\ufe0f"
	for _, colors := range []bool{false, true} {
		got := liveDiffSafe("\x1b]52;c;clipboard\a\x1b[2J"+source+"\x00\x1b[6n\n", colors)
		if got != source+"\n" {
			t.Fatalf("colors=%t: source text damaged or terminal controls retained: %q", colors, got)
		}
	}
}

func TestLiveDiffHighlightPreservesSyntaxColors(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.go")
	chunk := liveDiffHighlightChunk("recent", path, "@@ -1 +1 @@\n-return \"before\"\n+return \"after\"\n", true)
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
			codeLine := func(highlighted bool) string {
				t.Helper()
				chunk.highlighted = highlighted
				file := liveDiffFile{path: path, chunks: []liveDiffChunk{chunk}}
				render, err := renderLiveDiff(t.Context(), []liveDiffFile{file}, delta, workspace, 90, 0, chunk)
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range render.lines {
					if strings.Contains(ansi.Strip(line), `return "after"`) {
						if highlighted {
							return strings.TrimPrefix(line, "\x1b[36m▎\x1b[0m ")
						}
						return strings.TrimPrefix(line, "  ")
					}
				}
				t.Fatalf("missing syntax-colored source line: %q", strings.Join(render.lines, "\n"))
				return ""
			}
			plain, marked := codeLine(false), codeLine(true)
			if !strings.Contains(plain, "\x1b[") || plain != marked {
				t.Fatalf("gutter changed delta's syntax styling: plain=%q marked=%q", plain, marked)
			}
		})
	}
}

func TestLiveDiffHeaders(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.go")
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide)+
				"\nfile-style = bold red\nfile-decoration-style = blue box"+
				"\nhunk-header-style = file line-number syntax\nhunk-header-decoration-style = blue box")
			for _, tc := range []struct {
				name, before, after, hunk, action string
				added, removed                    int
			}{
				{"edit", path, path, "@@ -1 +1 @@\n-before\n+after\n", "", 1, 1},
				{"new", "", path, "@@ -0,0 +1 @@\n+after\n", "New file", 1, 0},
				{"deleted", path, "", "@@ -1 +0,0 @@\n-before\n", "Deleted file", 0, 1},
				{"renamed", filepath.Join(workspace, "old.go"), path, "@@ -1 +1 @@\n-before\n+after\n", "Rename: old.go → file.go", 1, 1},
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
					chunk := liveDiffChunk{diff: diff, review: mekugi.ReviewFile{
						BeforePath: tc.before, AfterPath: tc.after, Diff: diff,
					}}
					file := liveDiffFile{path: path, chunks: []liveDiffChunk{chunk}}
					render, err := renderLiveDiff(t.Context(), []liveDiffFile{file}, delta, workspace, 90, 0, chunk)
					if err != nil {
						t.Fatal(err)
					}
					text := ansi.Strip(strings.Join(render.lines, "\n"))
					pathCount := 1
					if tc.name == "renamed" {
						pathCount++ // The action also names the destination.
					}
					if strings.Count(text, "file.go") != pathCount || strings.Contains(text, "/dev/null") ||
						strings.Contains(text, "Applied") || strings.Contains(text, "┐") ||
						strings.Contains(text, "└") || strings.Contains(text, "⟶") {
						t.Fatalf("duplicate or decorative delta header remains:\n%s", text)
					}
					if !strings.Contains(render.lines[0], "\x1b[1m1/1  file.go") ||
						!strings.Contains(render.lines[0], "─") || ansi.StringWidth(render.lines[0]) != 89 {
						t.Fatalf("file heading lacks bounded emphasis: %q", render.lines[0])
					}
					if !strings.Contains(render.lines[0], "\x1b[32m+"+strconv.Itoa(tc.added)+"\x1b[39m") ||
						!strings.Contains(render.lines[0], "\x1b[31m-"+strconv.Itoa(tc.removed)+"\x1b[39m") {
						t.Fatalf("missing green/red source-line counts: %q", render.lines[0])
					}

					if tc.action != "" && !strings.Contains(text, tc.action) {
						t.Fatalf("hidden delta header lost the file action %q:\n%s", tc.action, text)
					}
				})
			}
		})
	}
}

func TestLiveDiffHeaderWidthAndControls(t *testing.T) {
	for _, width := range []int{0, 1, 2, 10, 36, 90} {
		header := liveDiffHeader("1/2  界 é.go\x1b]52;c;clipboard\a", width, liveDiffCounts{12, 4})
		if !utf8.ValidString(header) || ansi.StringWidth(header) > width || strings.Contains(header, "clipboard") {
			t.Fatalf("width %d: unsafe or overflowing header: %q", width, header)
		}
	}
}

func TestLiveDiffHeaderCountsUseVisibleComposition(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	first := liveDiffHighlightChunk("first", path, "@@ -1 +1 @@\n-a\n+A\n", true)
	second := liveDiffHighlightChunk("second", path, "@@ -1 +1,2 @@\n-A\n+B\n+C\n", true)
	v := liveDiffView{}
	snapshot := []liveDiffFile{{path: path, chunks: []liveDiffChunk{first}}}
	v.merge(snapshot)
	snapshot[0].chunks = append(snapshot[0].chunks, second)
	v.merge(snapshot)
	v.refreshVisible()
	delta := liveDiffConfiguredDelta(t, "")
	for _, want := range []liveDiffCounts{{2, 1}, {0, 0}} {
		file := v.visible[v.files[0].key()]
		render, err := renderLiveDiff(t.Context(), []liveDiffFile{file}, delta, workspace, 90, 0, second)
		if err != nil {
			t.Fatal(err)
		}
		if len(render.counts) != 1 || render.counts[0] != want {
			t.Fatalf("counts = %v, want visible net counts %v", render.counts, want)
		}
		v.flush(true)
	}
}

func TestLiveDiffPreparedRenameKeepsDestination(t *testing.T) {
	workspace := t.TempDir()
	oldPath, newPath := filepath.Join(workspace, "old.go"), filepath.Join(workspace, "new.go")
	diff := "--- " + strconv.Quote(oldPath) + "\n+++ " + strconv.Quote(newPath) +
		"\n@@ -1 +1 @@\n-before\n+after\n@@ -20 +20 @@\n-older\n+newer\n"
	chunk := liveDiffChunk{
		key: "rename", status: "hp_a1 prepared (application unconfirmed)", diff: diff,
		review: mekugi.ReviewFile{BeforePath: oldPath, AfterPath: newPath, Diff: diff},
	}
	v := liveDiffView{}
	v.merge([]liveDiffFile{{path: oldPath, chunks: []liveDiffChunk{chunk}}})
	v.refreshVisible()
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
			render, err := renderLiveDiff(t.Context(), []liveDiffFile{v.visible[v.files[0].key()]}, delta, workspace, 90, 0, chunk)
			if err != nil {
				t.Fatal(err)
			}
			text := ansi.Strip(strings.Join(render.lines, "\n"))
			for _, want := range []string{"1/1  old.go", "Rename: old.go → new.go", "application unconfirmed", "after"} {
				if !strings.Contains(text, want) {
					t.Fatalf("prepared rename lost %q:\n%s", want, text)
				}
			}
			for _, label := range []string{"Rename: old.go → new.go", "hp_a1 prepared (application unconfirmed)"} {
				if strings.Count(text, label) != 1 {
					t.Fatalf("capture label repeated between hunks: %q\n%s", label, text)
				}
			}
		})
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
	created, err := mekugi.TranslateForHostAt(t.Context(), workspace,
		"new review_highlight_test.txt\ntype "+strconv.Quote(strings.Join(rows[:311], "\n")+"\n")+"\n", "")
	if err != nil {
		t.Fatal(err)
	}
	path := created.ReviewFiles[0].AfterPath
	initial := liveDiffChunk{
		key: "create", applied: true, review: created.ReviewFiles[0],
		diff: created.ReviewFiles[0].UnifiedDiff(),
	}
	recent := liveDiffHighlightChunk("edit", path,
		"@@ -312 +312 @@\n-line312\n+LATEST312\n@@ -337 +337 @@\n-line337\n+LATEST337\n", true)
	v := liveDiffView{}
	snapshot := []liveDiffFile{{path: path, chunks: []liveDiffChunk{initial}}}
	// Appends preserve the original /dev/null identity while growing the file.
	start := 311
	for _, end := range []int{312, 336, 337, 340} {
		hunk := fmt.Sprintf("@@ -%d,0 +%d,%d @@\n+%s\n", start, start+1, end-start, strings.Join(rows[start:end], "\n+"))
		snapshot[0].chunks = append(snapshot[0].chunks,
			liveDiffHighlightChunk("append-"+strconv.Itoa(end), path, hunk, true))
		start = end
	}
	v.merge(snapshot)
	snapshot[0].chunks = append(snapshot[0].chunks, recent)
	v.merge(snapshot)
	v.refreshVisible()
	file := v.visible[v.files[0].key()]
	if len(file.chunks) < 3 {
		t.Fatalf("fixture has %d composed regions, want at least 3", len(file.chunks))
	}
	for _, chunk := range file.chunks {
		if chunk.status != "" || chunk.review.BeforePath != "" {
			t.Fatalf("fixture is not a composed new file: %#v", chunk)
		}
	}
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide)+
				"\nline-numbers = false\nhunk-header-style = file line-number syntax\nhunk-header-decoration-style = box")
			render, err := renderLiveDiff(t.Context(), []liveDiffFile{file}, delta, workspace, 100, 0, recent)
			if err != nil {
				t.Fatal(err)
			}
			text := ansi.Strip(strings.Join(render.lines, "\n"))
			for _, label := range []string{"New file", "review_highlight_test.txt"} {
				if strings.Count(text, label) != 1 || !strings.Contains(ansi.Strip(render.lines[0]), label) {
					t.Fatalf("file label is missing from its header or repeats between hunks: %q\n%s", label, text)
				}
			}
			if len(render.lines) != len(rows)+1 || render.counts[0] != (liveDiffCounts{340, 0}) {
				t.Fatalf("extra heading rows or lost blank source rows: rows=%d counts=%v\n%s",
					len(render.lines), render.counts, text)
			}
			if render.focusOffset != 337 {
				t.Fatalf("follow offset = %d, want the latest source row 337", render.focusOffset)
			}
			for i := 1; i <= len(rows); i++ {
				plain := ansi.Strip(render.lines[i])
				if !strings.Contains(plain, strconv.Itoa(i)) {
					t.Fatalf("source row %d lost its inline number: %q", i, plain)
				}
				if marked := strings.HasPrefix(plain, "▎ "); marked != (i == 312 || i == 337) {
					t.Fatalf("source row %d has incorrect recency: %q", i, plain)
				}
				assertLiveDiffNoBackground(t, render.lines[i])
			}
		})
	}
}

func TestLiveDiffFollowLatestCombinedResult(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	old := liveDiffHighlightChunk("hp_a1", path, "@@ -20 +20 @@\n-before\n+FIRST20\n", true)
	recent := liveDiffHighlightChunk("hp_b1", path,
		"@@ -20 +20 @@\n-FIRST20\n+LATEST20\n@@ -337 +337 @@\n-before\n+LATEST337\n", true)
	v := liveDiffView{following: true}
	v.merge([]liveDiffFile{{path: path, chunks: []liveDiffChunk{old}}})
	v.merge([]liveDiffFile{{path: path, chunks: []liveDiffChunk{old, recent}}})
	v.refreshVisible()
	file := v.visible[v.files[0].key()]
	text := liveDiffVisibleText(file)
	if len(file.chunks) != 2 || strings.Contains(text, "FIRST20") ||
		!strings.Contains(text, "+LATEST20") || !strings.Contains(text, "+LATEST337") {
		t.Fatalf("result contains intermediate patches instead of final changes: %s", text)
	}
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
			render, err := renderLiveDiff(t.Context(), []liveDiffFile{file}, delta, workspace, 100, 0, v.latestChunk())
			if err != nil {
				t.Fatal(err)
			}
			focused := ansi.Strip(strings.Join(render.lines[render.focusOffset:], "\n"))
			if !strings.Contains(focused, "LATEST337") || strings.Contains(focused, "LATEST20") {
				t.Fatalf("follow selected an older capture or hunk: %q", focused)
			}
			text := ansi.Strip(strings.Join(render.lines, "\n"))
			if strings.Contains(text, "LATEST UPDATE") || render.counts[0] != (liveDiffCounts{2, 2}) {
				t.Fatalf("unexpected update label or incorrect counts: counts=%v\n%s", render.counts, text)
			}
			for _, line := range render.lines {
				plain := ansi.Strip(line)
				if strings.Contains(plain, "LATEST20") || strings.Contains(plain, "LATEST337") {
					if !strings.HasPrefix(plain, "▎ ") {
						t.Fatalf("current capture lost its highlight: %q", line)
					}
				}
				assertLiveDiffNoBackground(t, line)
			}
		})
	}
}

func TestLiveDiffCaptureLabelsOnce(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	for _, applied := range []bool{false, true} {
		t.Run(strconv.FormatBool(applied), func(t *testing.T) {
			first := liveDiffHighlightChunk("hp_a1", path,
				"@@ -1 +1 @@\n-old1\n+first1\n@@ -20 +20 @@\n-old20\n+first20\n", applied)
			second := liveDiffHighlightChunk("hp_a2", path,
				"@@ -1 +1 @@\n-first1\n+second1\n@@ -20 +20 @@\n-first20\n+second20\n", applied)
			v := liveDiffView{}
			v.merge(nil)
			v.merge([]liveDiffFile{{path: path, chunks: []liveDiffChunk{first, second}}})
			v.refreshVisible()
			file := v.visible[v.files[0].key()]
			for _, sideBySide := range []bool{false, true} {
				t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
					delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
					render, err := renderLiveDiff(t.Context(), []liveDiffFile{file}, delta, workspace, 90, 0, second)
					if err != nil {
						t.Fatal(err)
					}
					text := ansi.Strip(strings.Join(render.lines, "\n"))
					var labels []string
					if !applied {
						labels = append(labels, first.status, second.status)
					}
					for _, label := range labels {
						if strings.Count(text, label) != 1 {
							t.Fatalf("capture identity or recency label missing or repeated: %q\n%s", label, text)
						}
					}
					if strings.Contains(text, "Unable to combine") ||
						applied && (strings.Contains(text, first.status) || strings.Contains(text, "first1")) {
						t.Fatalf("combined result exposed intermediate patches:\n%s", text)
					}
					contents := []string{"second1", "second20"}
					if !applied {
						contents = append(contents, "first1", "first20")
					}
					for _, content := range contents {
						if !strings.Contains(text, content) {
							t.Fatalf("capture content missing: %q\n%s", content, text)
						}
					}
				})
			}
		})
	}
}

func TestLiveDiffBlankSourceRows(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	chunk := liveDiffHighlightChunk("edit", path, "@@ -1,4 +1,4 @@\n \n-old\n+new\n-\n+\n tail\n", true)
	chunk.status = ""
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide)+
				"\nline-numbers-left-format = \"\"\nline-numbers-right-format = \"\""+
				"\nline-numbers-left-style = normal\nline-numbers-right-style = normal\nzero-style = normal normal")
			render, err := renderLiveDiff(t.Context(), []liveDiffFile{{path: path, chunks: []liveDiffChunk{chunk}}},
				delta, workspace, 90, 0, chunk)
			if err != nil {
				t.Fatal(err)
			}
			wantRows := 7
			if sideBySide {
				wantRows = 6 // Delta puts dissimilar old/new text on separate rows.
			}
			if len(render.lines) != wantRows || render.counts[0] != (liveDiffCounts{2, 2}) {
				t.Fatalf("blank context/added/removed rows changed: rows=%d counts=%v\n%s",
					len(render.lines), render.counts, strings.Join(render.lines, "\n"))
			}
			for _, line := range render.lines[1:] {
				if strings.TrimSpace(ansi.Strip(line)) == "" {
					t.Fatalf("blank source row lacks inline numbers: %q", line)
				}
				assertLiveDiffNoBackground(t, line)
			}
		})
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
		{"renamed", filepath.Join(workspace, "old.txt"), path, "Rename: old.txt → file.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := mekugi.ReviewFile{BeforePath: tc.before, AfterPath: tc.after,
				Diff: tc.name + " " + strconv.Quote(tc.before) + " -> " + strconv.Quote(tc.after) + "\n"}
			for _, sideBySide := range []bool{false, true} {
				t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
					delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
					for _, status := range []string{"", "hp_a1 prepared (application unconfirmed)"} {
						chunk := liveDiffChunk{key: "change", status: status, review: review, diff: review.Diff}
						render, err := renderLiveDiff(t.Context(), []liveDiffFile{{path: path, chunks: []liveDiffChunk{chunk}}},
							delta, workspace, 100, 0, chunk)
						if err != nil {
							t.Fatal(err)
						}
						wantRows := 1
						if status != "" {
							wantRows++
						}
						text := ansi.Strip(strings.Join(render.lines, "\n"))
						if len(render.lines) != wantRows || strings.Count(text, tc.action) != 1 ||
							render.focusOffset >= len(render.lines) || render.counts[0] != (liveDiffCounts{}) {
							t.Fatalf("path-only action duplicated or lost: %+v\n%s", render, text)
						}
						if status != "" && strings.Count(text, status) != 1 {
							t.Fatalf("path-only action lost application uncertainty:\n%s", text)
						}
					}
				})
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
		{"move", oldPath, path, "Rename:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := mekugi.ReviewFile{BeforePath: tc.before, AfterPath: tc.after,
				Diff: tc.name + " " + strconv.Quote(tc.before) + " -> " + strconv.Quote(tc.after) + "\n"}
			file := liveDiffFile{path: path, highlighted: true, chunks: []liveDiffChunk{{review: review, diff: review.Diff}}}
			for _, sideBySide := range []bool{false, true} {
				t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
					delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
					for _, width := range []int{40, 90} {
						render, err := renderLiveDiff(t.Context(), []liveDiffFile{file, {path: "next.txt"}},
							delta, workspace, width, 0, file.chunks[0])
						if err != nil {
							t.Fatal(err)
						}
						lines := render.lines[:render.starts[1]]
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
						if render.focusOffset != 0 || (width == 40 && len(lines) == 1) {
							t.Fatalf("width %d: wrapped heading lost focus or its continuation: %+v", width, render)
						}
						for _, line := range lines {
							if !utf8.ValidString(line) || ansi.StringWidth(line) > width-1 || !strings.Contains(line, "\x1b[1m") {
								t.Fatalf("width %d: invalid wrapped heading: %q", width, line)
							}
						}
						view := liveDiffView{files: []liveDiffFile{file, {path: "next.txt"}}, scroll: make(map[string]int)}
						view.scrollTo(render, render.starts[1])
						if view.selected != 1 || view.scroll["next.txt"] != 0 {
							t.Fatal("wrapped heading broke navigation to the next file")
						}
					}
				})
			}
		})
	}
}
