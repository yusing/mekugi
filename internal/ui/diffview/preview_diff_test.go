package diffview

import (
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotDiffTailStyles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme livediff.Theme
	}{
		{"terminal", livediff.TerminalTheme},
		{"dark", livediff.DarkTheme},
		{"light", livediff.LightTheme},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frame []string
			for _, input := range []string{
				"--- \"src/main.go\"\n+++ \"src/main.go\"\n@@ -1 +1 @@\n-var x = 1\n+var x = 2\n",
				"+++ \"src/main.go\"\n@@ -1 +1 @@\n-var x = 1\n+var x = 2\n",
				"-old\n+new\n",
			} {
				pane := PreviewPane{}
				pane.Update(Preview{ID: "tail", Workspace: "/workspace", Caller: "/root", Input: input, DiffText: true, Truncated: true})
				rows, err := pane.Render(t.Context(), "/workspace", tc.theme, 60, 10)
				if err != nil {
					t.Fatal(err)
				}
				frame = append(frame, rows...)
				frame = append(frame, "plain after preview")
				for _, row := range pane.Views["tail"].Source {
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
	pane := PreviewPane{}
	preview := Preview{ID: "tail", Workspace: "/workspace", Input: "+new\n"}
	pane.Update(preview)
	if _, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 60, 5); err != nil {
		t.Fatal(err)
	}
	if row := pane.Views["tail"].Source[0]; row.Kind != ' ' || row.Number != 1 || row.Text != "+new\n" {
		t.Fatalf("plain input parsed as diff: %+v", row)
	}
	preview.DiffText = true
	pane.Update(preview)
	if _, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, 60, 5); err != nil {
		t.Fatal(err)
	}
	row := pane.Views["tail"].Source[0]
	if row.Kind != '+' || row.Number != 0 || row.Text != "new\n" {
		t.Fatalf("stale plain row: %+v", row)
	}
}

func TestLivePreviewOmitsDeletedFiles(t *testing.T) {
	pane := PreviewPane{}
	preview := batchTestPreview("call", "/root", "src/kept.go")
	preview.Files = append(preview.Files, mekugi.RenderReviewFile("/workspace/src/deleted.go", "", "old\n", ""))
	pane.Update(preview)
	if len(pane.Views["call"].Current.Files) != 1 || len(preview.Files) != 2 {
		t.Fatal("deleted file retained or original evidence changed")
	}
	preview.Files = preview.Files[1:]
	pane.Update(preview)
	if len(pane.Order) != 0 || len(pane.Callers()) != 0 {
		t.Fatal("deletion-only preview not withdrawn")
	}
}
