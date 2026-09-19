package router

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

func TestLiveDiffShellCommandsThroughPreview(t *testing.T) {
	var pane liveDiffPreviewPane
	input := "rg -n 'preview' internal/router | head -65\nsed -n '1,5p' file.go\n"
	pane.update(liveDiffPreview{ID: "shell", Workspace: "/workspace", Input: input})
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"rg", "head", "sed"} {
		if !strings.Contains(strings.Join(lines, "\n"), liveDiffDarkTheme.Foreground(chroma.NameFunction)+command) {
			t.Fatalf("preview lost command highlighting for %s: %q", command, lines)
		}
	}
}
