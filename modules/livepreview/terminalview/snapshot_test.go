package terminalview

import (
	"strings"
	"testing"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func assertRowsSnapshot(t *testing.T, name string, rows []string) {
	t.Helper()
	uisnapshot.Assert(t, "testdata/snapshots/"+name+".txt", strings.Join(rows, "\n")+"\n")
}

func TestUISnapshotDiffPreview(t *testing.T) {
	edit := previewmodel.Preview{ID: "edit", Workspace: "/workspace", Caller: "/root/worker", Status: previewmodel.PreviewEdit,
		Files: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/src/main.go", "/workspace/src/main.go", "package main\n\nconst label = \"old\"\n", "package main\n\nconst label = \"a long replacement that wraps inside a narrow preview\"\n")}}
	tail := previewmodel.Preview{ID: "tail", Workspace: "/workspace", Caller: "/root", Status: previewmodel.PreviewRunning,
		Input: "+++ \"src/main.go\"\n@@ -1,3 +1,3 @@\n package main\n-const label = \"old\"\n+const label = \"a long replacement that wraps in the preview\"\n", DiffText: true, Truncated: true}
	pending := previewmodel.Preview{ID: "pending", Workspace: "/workspace", Caller: "/root", Status: previewmodel.PreviewPending,
		Input: "src/main.go\ndocs/guide.md", Footer: "may write · 2 scoped paths"}
	completed := edit
	completed.Complete = true
	observed := edit
	observed.Status = previewmodel.PreviewRunning
	for _, tc := range []struct {
		name          string
		previews      []previewmodel.Preview
		width, height int
	}{
		{"preview_diff_tail", []previewmodel.Preview{tail}, 72, 8},
		{"preview_diff_tail_wrapped", []previewmodel.Preview{tail}, 36, 10},
		{"preview_edit_wrapped", []previewmodel.Preview{edit}, 36, 8},
		{"preview_completed", []previewmodel.Preview{completed}, 72, 7},
		{"preview_observed", []previewmodel.Preview{observed}, 72, 7},
		{"preview_pending_footer", []previewmodel.Preview{pending}, 48, 7},
		{"preview_unavailable", []previewmodel.Preview{{ID: "unavailable", Workspace: "/workspace", Caller: "/root", Status: previewmodel.PreviewUnavailable + "baseline unavailable"}}, 48, 4},
		{"preview_concurrent", []previewmodel.Preview{pending, edit}, 60, 12},
		{"preview_accordion", []previewmodel.Preview{pending, edit}, 42, 7},
		{"preview_hidden_calls", []previewmodel.Preview{pending, edit}, 42, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Prefer a caller explicitly; never choose the open card by wall-clock arrival time.
			pane := PreviewPane{Prefer: "/root/worker"}
			for _, preview := range tc.previews {
				pane.Update(preview)
			}
			rows, err := pane.Render(t.Context(), "/workspace", diffrender.DarkTheme, tc.width, tc.height)
			if err != nil {
				t.Fatal(err)
			}
			assertRowsSnapshot(t, tc.name, rows)
		})
	}
}
