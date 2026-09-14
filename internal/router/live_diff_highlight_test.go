package router

import (
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
		key: key, stream: "one", diff: diff, status: status, applied: applied,
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
		t.Fatalf("composition lost exact recent-region attribution: %#v", visible)
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

func TestLiveDiffHighlightUncomposedAndFlushedReceipt(t *testing.T) {
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
	other.stream = "another thread"
	snapshot[0].chunks = append(snapshot[0].chunks, other)
	v.merge(snapshot)
	v.refreshVisible()
	visible := v.visible[v.files[0].key()]
	if len(visible.chunks) != 3 || !strings.HasPrefix(visible.chunks[0].status, "Uncomposed") ||
		visible.chunks[1].highlighted || !visible.chunks[2].highlighted {
		t.Fatalf("ambiguous captures lost their identity or highlighted reviewed history: %#v", visible)
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
					if strings.Contains(plain, "LATEST UPDATE") && strings.Contains(line, "\x1b[1m") {
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
