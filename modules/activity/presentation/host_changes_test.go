package presentation

import (
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestHostEditProjection(t *testing.T) {
	for _, tc := range []struct{ kind, diff, want string }{
		{"update", "--- a/path\n+++ b/path\n@@\n-x\n+y\n", "Edit `path` · +1 −1"},
		{"add", "a\nb\n", "Create `path` · +2 −0"},
		{"delete", "a\nb\n", "Delete `path` · +0 −2"},
	} {
		item := observation.Item{Type: "fileChange", Status: "completed", Changes: []observation.FileChange{{Path: "/workspace/path", Diff: tc.diff}}}
		item.Changes[0].Kind.Type = tc.kind
		if got := HostEditText(item, "/workspace"); got != tc.want+" · apply_patch" {
			t.Fatalf("%s: %q", tc.kind, got)
		}
		pages := HostEditPages(item, "/workspace", "item/completed")
		if len(pages) != 1 || pages[0].Path != "path" || pages[0].Code != tc.diff {
			t.Fatalf("host detail: %+v", pages)
		}
		for _, status := range []string{"failed", "declined", "inProgress"} {
			item.Status = status
			if pages := HostEditPages(item, "/workspace", "item/completed"); len(pages) != 0 {
				t.Fatalf("%s exposed completed evidence", status)
			}
			label := status
			if status == "inProgress" {
				label = "pending"
			}
			if got := HostEditText(item, "/workspace"); got != tc.want+" · "+label+" · apply_patch" {
				t.Fatalf("%s outcome: %q", status, got)
			}
		}
	}
}
