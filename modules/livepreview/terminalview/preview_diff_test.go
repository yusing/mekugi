package terminalview

// Source: internal/ui/diffview/preview_diff_test.go@[a42ac9f1]
import (
	"testing"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotDiffTailStyles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme diffrender.Theme
	}{
		{"terminal", diffrender.TerminalTheme},
		{"dark", diffrender.DarkTheme},
		{"light", diffrender.LightTheme},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frame []string
			for _, input := range []string{
				"--- \"src/main.go\"\n+++ \"src/main.go\"\n@@ -1 +1 @@\n-var x = 1\n+var x = 2\n",
				"+++ \"src/main.go\"\n@@ -1 +1 @@\n-var x = 1\n+var x = 2\n",
				"-old\n+new\n",
			} {
				view := PreviewView{Current: previewmodel.Preview{ID: "tail", Workspace: "/workspace", Caller: "/root", Input: input, DiffText: true, Truncated: true}}
				rows, err := view.Render(t.Context(), "/workspace", tc.theme, 60, 10, PreviewMotion{})
				if err != nil {
					t.Fatal(err)
				}
				frame = append(frame, rows...)
				frame = append(frame, "plain after preview")
				for _, row := range view.Source {
					if row.Number != 0 {
						t.Fatal("tail has fabricated coordinates")
					}
				}
			}
			uisnapshot.AssertTerminal(t, "testdata/snapshots/preview_tail_styles_"+tc.name+".txt", frame, 60)
		})
	}
}

func TestPreviewDiffFlagReplacesPlainRows(t *testing.T) {
	preview := previewmodel.Preview{ID: "tail", Workspace: "/workspace", Input: "+new\n"}
	view := PreviewView{Current: preview}
	if _, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 60, 5, PreviewMotion{}); err != nil {
		t.Fatal(err)
	}
	if row := view.Source[0]; row.Kind != ' ' || row.Number != 1 || row.Text != "+new\n" {
		t.Fatalf("plain input parsed as diff: %+v", row)
	}
	preview.DiffText = true
	view.Current = preview
	if _, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 60, 5, PreviewMotion{}); err != nil {
		t.Fatal(err)
	}
	row := view.Source[0]
	if row.Kind != '+' || row.Number != 0 || row.Text != "new\n" {
		t.Fatalf("stale plain row: %+v", row)
	}
}
