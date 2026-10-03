package diffview

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestPreviewDiffTailStyles(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.TerminalTheme, livediff.DarkTheme, livediff.LightTheme} {
		for _, input := range []string{
			"--- \"src/main.go\"\n+++ \"src/main.go\"\n@@ -1 +1 @@\n-var x = 1\n+var x = 2\n",
			"+++ \"src/main.go\"\n@@ -1 +1 @@\n-var x = 1\n+var x = 2\n",
			"-old\n+new\n",
		} {
			pane := PreviewPane{}
			pane.Update(Preview{ID: "tail", Workspace: "/workspace", Caller: "/root", Input: input, DiffText: true, Truncated: true})
			rows, err := pane.Render(t.Context(), "/workspace", theme, 60, 10)
			if err != nil {
				t.Fatal(err)
			}
			rendered := strings.Join(rows, "\n")
			for _, kind := range []byte{'-', '+'} {
				if !strings.Contains(rendered, theme.RowBackground(kind)) {
					t.Fatalf("missing %c fill: %q", kind, rendered)
				}
			}
			if strings.Contains(input, "@@") && !strings.Contains(rendered, theme.Foreground(chroma.KeywordDeclaration)) {
				t.Fatalf("missing Go syntax: %q", rendered)
			}
			for _, row := range pane.Views["tail"].Source {
				if row.Number != 0 {
					t.Fatal("tail has fabricated coordinates")
				}
			}
			if strings.Contains(ansi.Strip(rendered), "++var") {
				t.Fatal("duplicated marker")
			}
		}
	}
}

func TestPreviewDiffFlagReplacesPlainRows(t *testing.T) {
	pane := PreviewPane{}
	preview := Preview{ID: "tail", Workspace: "/workspace", Input: "+new\n"}
	pane.Update(preview)
	if _, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 60, 5); err != nil {
		t.Fatal(err)
	}
	if row := pane.Views["tail"].Source[0]; row.Kind != ' ' || row.Number != 1 || row.Text != "+new\n" {
		t.Fatalf("plain input parsed as diff: %+v", row)
	}
	preview.DiffText = true
	pane.Update(preview)
	if _, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 60, 5); err != nil {
		t.Fatal(err)
	}
	row := pane.Views["tail"].Source[0]
	if row.Kind != '+' || row.Number != 0 || row.Text != "new\n" {
		t.Fatalf("stale plain row: %+v", row)
	}
}
