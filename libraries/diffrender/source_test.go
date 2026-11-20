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

func TestWrapSourceBreaksAtWords(t *testing.T) {
	for _, test := range []struct {
		text  string
		width int
		want  []string
	}{
		{"Keep each result as known", 12, []string{"Keep each ", "result as ", "known"}},
		{"ab cd ef", 5, []string{"ab ", "cd ef"}},
		{"identifier", 4, []string{"iden", "tifi", "er"}},
		{"    indented word", 6, []string{"    in", "dented", " word"}},
		{"a 界界", 3, []string{"a ", "界", "界"}},
		{"\x1b[31mred words\x1b[39m here", 5, []string{"red ", "words", " here"}},
		{"", 4, []string{""}},
	} {
		rows := WrapSource(test.text, test.width)
		plain := make([]string, len(rows))
		for i, row := range rows {
			plain[i] = ansi.Strip(row)
		}
		if strings.Join(plain, "\x00") != strings.Join(test.want, "\x00") || WrapRows(test.text, test.width) != len(rows) {
			t.Fatalf("WrapSource(%q, %d) = %q (%d counted), want %q", test.text, test.width, plain, WrapRows(test.text, test.width), test.want)
		}
		// Styles carry onto continuation rows; the closing reset does not.
		if strings.HasPrefix(test.text, "\x1b[31m") && (!strings.HasPrefix(rows[1], "\x1b[") || strings.HasPrefix(rows[2], "\x1b[")) {
			t.Fatalf("continuation styles = %q", rows)
		}
	}
}
