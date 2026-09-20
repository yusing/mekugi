package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

func TestLiveDiffTerminalPreWriteFastCompletionAndBoundedTail(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})

	exact := liveDiffPreview{
		ID: "prewrite", Workspace: workspace, Thread: "thread",
		Evaluated: true, Status: "PRE-WRITE DIFF",
		Files: []mekugi.ReviewFile{{
			BeforePath: "formatted.go", AfterPath: "formatted.go",
			Diff: "modify formatted.go\n--- formatted.go\n+++ formatted.go\n@@ -1 +1 @@\n-oldValue\n+formattedValue\n",
		}},
	}
	broker.publishPreview(exact, false)
	exact.Complete = true
	broker.publishPreview(exact, false)
	// Both calls finish before the asynchronously launched viewer subscribes.
	second := exact
	second.ID = "second"
	second.Files = []mekugi.ReviewFile{{
		BeforePath: "second.go", AfterPath: "second.go",
		Diff: "modify second.go\n--- second.go\n+++ second.go\n@@ -1 +1 @@\n-oldValue\n+secondValue\n",
	}}
	broker.publishPreview(second, false)
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 18)
	frame := ui.frame(t, func(frame string) bool {
		text := ansi.Strip(frame)
		return strings.Contains(text, "PRE-WRITE DIFF") && strings.Contains(text, "formattedValue") && strings.Contains(text, "secondValue")
	})
	if strings.Contains(ansi.Strip(frame), "old shell input") {
		t.Fatal("pre-write frame rendered unrelated raw shell input")
	}

	marker := "DISTINCTIVE_PREWRITE_TAIL_MARKER"
	large := boundLiveDiffPreview(liveDiffPreview{
		ID: "large", Workspace: workspace, Thread: "thread",
		Evaluated: true, Status: "PRE-WRITE DIFF",
		Files: []mekugi.ReviewFile{{
			BeforePath: "large.txt", AfterPath: "large.txt",
			Diff: "modify large.txt\n--- large.txt\n+++ large.txt\n@@ -1 +1 @@\n-old\n+" +
				strings.Repeat("wide-row-content\n", 10000) + marker + "\n",
		}},
	})
	if !large.DiffText || !large.Truncated {
		t.Fatalf("large preview was not converted to bounded diff text: %+v", large)
	}
	broker.publishPreview(large, false)
	large.Complete = true
	broker.publishPreview(large, false)
	ui.frame(t, func(frame string) bool {
		text := ansi.Strip(frame)
		return strings.Contains(text, "PRE-WRITE DIFF · tail") && strings.Contains(text, marker)
	})
	ui.quit(t)
}
