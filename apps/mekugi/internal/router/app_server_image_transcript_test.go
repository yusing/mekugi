package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestAppServerImageTranscriptHighlight(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		for _, width := range []int{16, 80} {
			u, _ := newAppServerTestUI()
			u.shell.Transcript.Painter.Theme = theme
			appServerTestKeys(t, u, "literal [Image 1] 世界 ")
			u.insertImage("/tmp/one.png")
			appServerTestKeys(t, u, " after ")
			u.insertImage("/tmp/two.png")
			appServerTestKeys(t, u, "\r")
			check := func(v *activityAdapter) {
				t.Helper()
				if len(v.Entries) != 1 {
					t.Fatalf("unexpected duplicate or synthetic tool activity: %+v", v.Entries)
				}
				out := v.testUserItem(v.Entries[0].Source, width)
				screen := vt.NewEmulator(width, len(out.Lines))
				defer screen.Close()
				if _, err := screen.Write([]byte(strings.Join(out.Lines, "\r\n"))); err != nil {
					t.Fatal(err)
				}
				var bold strings.Builder
				for y, line := range out.Lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("row exceeds viewport: %q", line)
					}
					if strings.Contains(ansi.Strip(line), "[Image") && strings.Contains(line, "\x1b[1;35m") && !strings.Contains(ansi.Strip(line), "]") {
						t.Fatalf("split image placeholder: %q", line)
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
			check(u.testActivity(u.shell.Transcript))
			item := binding.ThreadItem{ID: "user", Type: "userMessage", Content: []byte(`[{"type":"text","text":"literal [Image 1] 世界 "},{"type":"localImage","path":"/tmp/one.png"},{"type":"text","text":" after "},{"type":"image","url":"https://example.com/two.png"}]`)}
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
				check(u.testActivity(u.shell.Transcript))
			}
			restored := newAppServerSessionTestUI(t, t.TempDir())
			restored.shell.Transcript.Painter.Theme = theme
			restored.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{item}}})
			check(restored.testActivity(restored.shell.Transcript))
		}
	}
}
