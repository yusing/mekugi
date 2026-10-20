package diffrender

// Source: internal/livediff/livediff_test.go@[b95dde39]
import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestShellCommandsAndColorSource(t *testing.T) {
	source := "VALUE=argument rg pattern file | head -10\n"
	commands := shellCommands(source)
	if len(commands) != 2 || commands[strings.Index(source, " rg ")+1] != "rg" || commands[strings.Index(source, "head")] != "head" {
		t.Fatalf("shell commands = %#v", commands)
	}
	lines, err := new(Renderer).ColorSource(t.Context(), DarkTheme, "stream.sh", source)
	if err != nil {
		t.Fatal(err)
	}
	colored := strings.Join(lines, "\n")
	if ansi.Strip(colored) != strings.TrimSuffix(source, "\n") ||
		!strings.Contains(colored, DarkTheme.Foreground(chroma.NameFunction)+"rg") {
		t.Fatalf("colored shell source = %q", colored)
	}
}

func TestRendererCacheIsolation(t *testing.T) {
	const source = "package p\n"
	first, second := new(Renderer), new(Renderer)
	if _, err := first.ColorSource(t.Context(), DarkTheme, "file.go", source); err != nil {
		t.Fatal(err)
	}
	if len(first.syntax) != 1 || len(second.syntax) != 0 {
		t.Fatalf("renderer caches leaked: first=%d second=%d", len(first.syntax), len(second.syntax))
	}
}
