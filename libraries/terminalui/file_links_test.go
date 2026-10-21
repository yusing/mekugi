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
			rows := p.Markdown("## [existing heading](sample.go) after\n\n> [a long wrapped file label](sample.go:2) after\n\nmissing.go and [web](https://example.com).", 22, false)
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
