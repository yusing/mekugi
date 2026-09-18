package router

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLiveDiffShellCommandNames(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		commands     []string
	}{
		{"screenshot", "rg -n 'func .*commentaryAuthor' internal/router | head -65\nsed -n '535,600p' internal/router/mekugi_proxy.go\n", []string{"rg", "head", "sed"}},
		{"argument", "printf rg\n", []string{"printf"}},
		{"assignments", "VALUE=rg command-name --flag sed\n", []string{"command-name"}},
		{"heredoc", "cat <<'END'\nrg plain body\nEND\nsed file\n", []string{"cat", "sed"}},
		{"substitution", "printf '%s' \"$(rg pattern file)\"\n", []string{"printf", "rg"}},
		{"branch", "if rg pattern file; then sed file; fi\n", []string{"rg", "sed"}},
		{"comment", "# rg not_a_command\nsed file\n", []string{"sed"}},
		{"assignment_only", "VALUE=rg\n", nil},
		{"quoted_argument", "printf 'rg argument'\n", []string{"printf"}},
		{"unicode", "rg '界' file\nsed file\n", []string{"rg", "sed"}},
		{"incomplete_quote", "rg 'unfinished", []string{"rg"}},
		{"incomplete_pipeline", "rg pattern |", []string{"rg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := liveDiffShellCommands(tc.source)
			for _, want := range tc.commands {
				found := false
				for _, command := range commands {
					found = found || command == want
				}
				if !found {
					t.Fatalf("missing command %q: %v", want, commands)
				}
			}
			if len(commands) != len(tc.commands) {
				t.Fatalf("arguments or content became commands: %v", commands)
			}
			for _, theme := range []liveDiffTheme{liveDiffDarkTheme, liveDiffLightTheme} {
				lines, err := liveDiffColorSource(t.Context(), theme, "stream.sh", tc.source)
				if err != nil || ansi.Strip(strings.Join(lines, "\n")) != strings.TrimSuffix(tc.source, "\n") {
					t.Fatalf("decoration changed source: %v %q", err, lines)
				}
				for offset, command := range commands {
					// Built-ins retain their original category.
					if command == "printf" {
						continue
					}
					if !strings.Contains(strings.Join(lines, "\n"), theme.foreground(chroma.NameFunction)+command+"\x1b[39m") {
						t.Fatalf("command at %d remains plain: %q", offset, lines)
					}
				}
			}
		})
	}
}

func TestLiveDiffShellCommandsThroughPreview(t *testing.T) {
	var pane liveDiffPreviewPane
	input := "rg -n 'preview' internal/router | head -65\nsed -n '1,5p' file.go\n"
	pane.update(liveDiffPreview{ID: "shell", Workspace: "/workspace", Input: input}, time.Time{})
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"rg", "head", "sed"} {
		if !strings.Contains(strings.Join(lines, "\n"), liveDiffDarkTheme.foreground(chroma.NameFunction)+command) {
			t.Fatalf("preview lost command highlighting for %s: %q", command, lines)
		}
	}
}

func BenchmarkLiveDiffShellCommandSyntax(b *testing.B) {
	source := strings.Repeat("rg -n 'pattern' file.go | head -10\n", 40)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := liveDiffColorSource(b.Context(), liveDiffDarkTheme, "stream.sh", source); err != nil {
			b.Fatal(err)
		}
	}
}
