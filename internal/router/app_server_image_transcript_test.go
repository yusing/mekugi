package router

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestAppServerImageTranscriptHighlight(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		for _, width := range []int{16, 80} {
			u, _ := newAppServerTestUI()
			u.view.painter.theme = theme
			appServerTestKeys(t, u, "literal [Image 1] 世界 ")
			u.insertImage("/tmp/one.png")
			appServerTestKeys(t, u, " after ")
			u.insertImage("/tmp/two.png")
			appServerTestKeys(t, u, "\r")
			check := func(v *liveActivityView) {
				t.Helper()
				if len(v.entries) != 1 {
					t.Fatalf("unexpected duplicate or synthetic tool activity: %+v", v.entries)
				}
				var out conversationLines
				v.userItem(&out, v.entries[0], width)
				screen := vt.NewEmulator(width, len(out.lines))
				defer screen.Close()
				if _, err := screen.Write([]byte(strings.Join(out.lines, "\r\n"))); err != nil {
					t.Fatal(err)
				}
				var bold strings.Builder
				for y, line := range out.lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("row exceeds viewport: %q", line)
					}
					for x := range width {
						cell := screen.CellAt(x, y)
						if cell != nil && cell.Style.Attrs&uv.AttrBold != 0 {
							bold.WriteString(cell.Content)
						}
					}
				}
				if bold.String() != "[Image 1][Image 2]" {
					t.Fatalf("attachment emphasis leaked or missing: %q", bold.String())
				}
			}
			check(u.view)
			item := appServerItem{ID: "user", Type: "userMessage", Content: []byte(`[{"type":"text","text":"literal [Image 1] 世界 "},{"type":"localImage","path":"/tmp/one.png"},{"type":"text","text":" after "},{"type":"image","url":"https://example.com/two.png"}]`)}
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
				check(u.view)
			}
			restored := newAppServerSessionTestUI(t, t.TempDir())
			restored.view.painter.theme = theme
			restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
			check(restored.view)
		}
	}
}
