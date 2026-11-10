package terminalui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotDeferredFileLinks(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			p := terminalui.Painter{Theme: theme, FileLink: func(string) bool { return true }, DeferFileLinks: true}
			rows := p.Markdown("## [existing heading](sample.go) after\n\n> [a long wrapped file label](sample.go:2) after\n\nsample.go @sample.go\n\n`sample.go:2` `@sample.go:2`\n\n@missing.go @agent:main\n\nmissing.go and [web](https://example.com).", 22, false)
			for i, row := range rows {
				rows[i], _ = terminalui.ResolveFileLinks(row, map[string]bool{"sample.go": true, "sample.go:2": true, "missing.go": false}, theme.Accent())
				if ansi.Strip(rows[i]) != ansi.Strip(row) {
					t.Fatalf("link resolution changed visible text: %q -> %q", row, rows[i])
				}
				if strings.Contains(rows[i], "mekugi-file-candidate:") {
					t.Fatal("private destination escaped resolution")
				}
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/deferred-file-links-%d.txt", theme), append(rows, "plain after links"), 22)
		})
	}
}

func TestMarkdownPrefixedFileLinks(t *testing.T) {
	for _, path := range []string{"sample.go", "src/sample.go", "./sample.go", "../sample.go:2", "/workspace/sample.go:2-3", "src/sample.go:2-3", "src/a b.go:2"} {
		for _, code := range []bool{false, true} {
			if !code && strings.Contains(path, " ") {
				continue // Spaces require a code span or explicit Markdown link.
			}
			for _, deferred := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/code=%t/deferred=%t", path, code, deferred), func(t *testing.T) {
					p := terminalui.Painter{Theme: diffrender.DarkTheme, DeferFileLinks: deferred, FileLink: func(target string) bool {
						if target != path {
							t.Fatalf("admission received %q, want underlying path %q", target, path)
						}
						return true
					}}
					var destinations []string
					for _, prefix := range []string{"", "@"} {
						source := prefix + path
						if code {
							source = "`" + source + "`"
						}
						row := p.Inline(source)
						if deferred {
							_, demands := terminalui.ResolveFileLinks(row, nil, p.Theme.Accent())
							if len(demands) != 1 || demands[0] != path {
								t.Fatalf("deferred demand lost underlying path: %q", demands)
							}
							rejected, _ := terminalui.ResolveFileLinks(row, map[string]bool{path: false}, p.Theme.Accent())
							if strings.Contains(rejected, "\x1b]8;") || ansi.Strip(rejected) != prefix+path {
								t.Fatalf("rejected candidate changed text or remained linked: %q", rejected)
							}
							row, _ = terminalui.ResolveFileLinks(row, map[string]bool{path: true}, p.Theme.Accent())
						}
						if ansi.Strip(row) != prefix+path {
							t.Fatalf("visible operand changed: %q", row)
						}
						_, destination, found := strings.Cut(row, "\x1b]8;;")
						destination, _, _ = strings.Cut(destination, "\x1b\\")
						if !found || destination == "" {
							t.Fatalf("operand was not linked: %q", row)
						}
						destinations = append(destinations, destination)
					}
					if destinations[0] != destinations[1] {
						t.Fatalf("prefix changed destination: %q", destinations)
					}
				})
			}
		}
	}
	for _, source := range []string{"@", "@@sample.go", "@agent:main", "@/root/worker", "@/123/worker", "@user", "user@example", "@https://example.com", "@#sample.go", "@?sample.go"} {
		t.Run(source, func(t *testing.T) {
			p := terminalui.Painter{Theme: diffrender.DarkTheme, FileLink: func(target string) bool {
				t.Fatalf("non-path reached file admission: %q", target)
				return false
			}}
			for _, input := range []string{source, "`" + source + "`"} {
				if row := p.Inline(input); strings.Contains(row, "\x1b]8;") || ansi.Strip(row) != source {
					t.Fatalf("non-path changed or became linked: %q", row)
				}
			}
		})
	}
	p := terminalui.Painter{Theme: diffrender.DarkTheme, FileLink: func(string) bool { return true }, DeferFileLinks: true}
	row := p.Inline("@sample.go, @agent:main user@example.com")
	row, _ = terminalui.ResolveFileLinks(row, map[string]bool{"sample.go": true, "user@example.com": false}, p.Theme.Accent())
	if strings.Count(row, "\x1b]8;;") != 2 || ansi.Strip(row) != "@sample.go, @agent:main user@example.com" {
		t.Fatalf("adjacent punctuation or non-paths changed: %q", row)
	}
}
