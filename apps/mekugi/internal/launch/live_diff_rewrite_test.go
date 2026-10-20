package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/livepreview/projection"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotLiveDiffStreamingRewrite(t *testing.T) {
	for _, count := range []int{100, 4000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "main.go")
			var baseline strings.Builder
			baseline.WriteString("package main\n\nfunc keep() {}\n")
			for i := range count {
				fmt.Fprintf(&baseline, "func old%04d() { println(%d) }\n", i, i)
			}
			before := baseline.String()
			if err := os.WriteFile(path, []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			var pane previewview.PreviewPane
			var frame []string
			for step := 1; step <= 2; step++ {
				// The heredoc is still arriving. Its unchanged suffix has not
				// arrived, so the projection contains a large trailing deletion.
				command := fmt.Sprintf("cat > main.go <<'EOF'\npackage main\n\nconst newest = %d\n\nfunc keep() {}\n", step)
				files, tool, ok, err := projection.Shell(t.Context(), command, workspace, false)
				if err != nil || !ok || len(files) != 1 {
					t.Fatalf("projection: %v, %t, %d files", err, ok, len(files))
				}
				original := files[0]
				preview := boundPreview(previewmodel.Preview{ID: "rewrite", Workspace: workspace, Caller: "/root", Status: previewmodel.PreviewEdit, Tool: tool, Files: files})
				if preview.Truncated != (count == 4000) || len(responses.MustMarshalJSON(preview)) > 48<<10 {
					t.Fatal("incorrect preview bound")
				}
				if files[0] != original || !strings.Contains(original.Diff, "-func old3999") && count == 4000 {
					t.Fatal("display clipping changed the original projection")
				}
				pane.Update(preview, time.Unix(0, 0))
				rows, err := pane.RenderBatch(t.Context(), "/root", workspace, diffrender.DarkTheme, 72, 16, 15)
				if err != nil {
					t.Fatal(err)
				}
				text := strings.Join(rows, "\n")
				if !strings.Contains(ansi.Strip(text), fmt.Sprintf("const newest = %d", step)) || strings.Contains(text, fmt.Sprintf("old%04d", count-1)) {
					t.Fatalf("deleted baseline displaced arriving source:\n%s", ansi.Strip(text))
				}
				if !strings.Contains(text, diffrender.DarkTheme.Foreground(chroma.Keyword)+"const") {
					t.Fatal("source keyword lost its syntax color")
				}
				frame = append(frame, rows...)
				frame = append(frame, "plain after preview")
				if count == 100 {
					pane.ScrollBatch("/root", ' ')
					scrolled, err := pane.RenderBatch(t.Context(), "/root", workspace, diffrender.DarkTheme, 72, 16, 15)
					if err != nil || !strings.Contains(strings.Join(scrolled, "\n"), "old0010") {
						t.Fatal("manual scroll could not inspect the shortened deletion block")
					}
					pane.ScrollBatch("/root", 'r')
				}
			}
			if contents, err := os.ReadFile(path); err != nil || string(contents) != before {
				t.Fatal("preview executed the rewrite")
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/live-diff-rewrite-%d.txt", count), frame, 72)
		})
	}
}

func TestUISnapshotLiveDiffClippedHunkSyntax(t *testing.T) {
	var frame []string
	for _, source := range []string{
		"package main\n" + strings.Repeat("var old = 1\n", 5000) + "const last = 2\n",
		"package main\nconst last = `" + strings.Repeat("界", 20000) + "TAIL`\n",
	} {
		preview := boundPreview(previewmodel.Preview{Workspace: "/workspace", Caller: "/root", Files: []diffmodel.ReviewFile{
			diffmodel.RenderReviewFile("/workspace/main.go", "/workspace/main.go", source, "package main\n"),
		}})
		if !preview.Truncated || len(responses.MustMarshalJSON(preview)) > 48<<10 || !strings.Contains(preview.Input, "+++ \"main.go\"") || !strings.Contains(preview.Input, "@@ ") {
			t.Fatal("clipped hunk lost its language or exceeded its bound")
		}
		view := previewview.PreviewView{Current: preview}
		rows, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, 72, 6, previewview.PreviewMotion{})
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(rows, "\n")
		if strings.Contains(source, "TAIL") {
			if !strings.Contains(ansi.Strip(text), "TAIL") {
				t.Fatal("clipping lost the long source row")
			}
		} else if !strings.Contains(text, diffrender.DarkTheme.Foreground(chroma.Keyword)+"const") {
			t.Fatal("clipped deletion lost syntax color")
		}
		frame = append(frame, rows...)
		frame = append(frame, "plain after preview")
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/live-diff-clipped-hunk.txt", frame, 72)
}
