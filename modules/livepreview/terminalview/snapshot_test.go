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
		preview       previewmodel.Preview
		width, height int
	}{
		{"preview_diff_tail", tail, 72, 8},
		{"preview_diff_tail_wrapped", tail, 36, 10},
		{"preview_edit_wrapped", edit, 36, 8},
		{"preview_completed", completed, 72, 7},
		{"preview_observed", observed, 72, 7},
		{"preview_pending_footer", pending, 48, 7},
		{"preview_unavailable", previewmodel.Preview{ID: "unavailable", Workspace: "/workspace", Caller: "/root", Status: previewmodel.PreviewUnavailable + "baseline unavailable"}, 48, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := PreviewView{Current: tc.preview, Complete: tc.preview.Complete}
			rows, err := view.Render(t.Context(), "/workspace", diffrender.DarkTheme, tc.width, tc.height, PreviewMotion{})
			if err != nil {
				t.Fatal(err)
			}
			assertRowsSnapshot(t, tc.name, rows)
		})
	}
}
