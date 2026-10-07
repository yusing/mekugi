package livediff

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestColorDiffPreservesPartialAndMultiFileOutput(t *testing.T) {
	for _, source := range []string{
		"id\n--- /dev/null\n+++ \"a path.go\"\n@@ -0,0 +1,8 @@\n+package main\n+var answer = 42",
		"--- a.go\n+++ a.go\n@@ -1 +1 @@ function\n-var x = 1\n+var x = 2\n\\ No newline at end of file\n--- a.py\n+++ b.py\n@@ -1 +1 @@\n-return 1\n+return 2\n",
		"--- a.go\n+++ b.go\n@@ -1 +1 @@\n--- source\n+++ source\n",
	} {
		for _, theme := range []Theme{TerminalTheme, DarkTheme, LightTheme} {
			var r Renderer
			rows, err := r.ColorDiff(t.Context(), theme, source)
			if err != nil {
				t.Fatal(err)
			}
			if got := ansi.Strip(strings.Join(rows, "\n")); got != source {
				t.Fatalf("changed source: %q", got)
			}
			if !strings.Contains(strings.Join(rows, "\n"), theme.RowBackground('+')) {
				t.Fatal("missing addition fill")
			}
		}
	}
}

func TestColorDiffUsesPaneRowForeground(t *testing.T) {
	for _, theme := range []Theme{TerminalTheme, DarkTheme, LightTheme} {
		for _, source := range []string{"+plain", "--- a.unknown\n+++ b.unknown\n@@ -1 +1 @@\n-old\n+plain"} {
			var r Renderer
			rows, err := r.ColorDiff(t.Context(), theme, source)
			if err != nil {
				t.Fatal(err)
			}
			text := "plain"
			if strings.Contains(source, "@@") {
				text = theme.WordBackground('+') + text + theme.RowBackground('+')
			}
			if got, want := rows[len(rows)-1], SourceLine(theme, 9, "", text, '+'); got != want {
				t.Fatalf("row differs from pane: %q != %q", got, want)
			}
		}
	}
}
