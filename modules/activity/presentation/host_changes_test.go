package presentation

import (
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestHostEditProjection(t *testing.T) {
	for _, tc := range []struct{ kind, diff, want string }{
		{"update", "--- a/path\n+++ b/path\n@@\n-x\n+y\n", "Edit `path` · +1 −1"},
		{"update", "--- a/path\n+++ b/path\n@@ -0,0 +1 @@\n+++i;\n", "Edit `path` · +1 −0"},
		{"update", "@@ -1 +0,0 @@\n--- counter;\n", "Edit `path` · +0 −1"},
		{"update", "@@ -1 +1 @@\n--- counter;\n+++i;\n", "Edit `path` · +1 −1"},
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

func TestHostEditWithoutChangedLines(t *testing.T) {
	for _, tc := range []struct{ kind, diff, move, want, heading string }{
		{"update", "", "", "Edit `path` · ran", "Edit ran"},
		{"update", "@@ -1 +1 @@\n unchanged\n", "", "Edit `path` · ran", "Edit ran"},
		{"add", "", "", "Create `path` · +0 −0", "Edited"},
		{"delete", "", "", "Delete `path` · +0 −0", "Edited"},
		{"update", "", "/workspace/moved", "Move `path => moved` · +0 −0", "Edited"},
	} {
		change := observation.FileChange{Path: "/workspace/path", Diff: tc.diff}
		change.Kind.Type, change.Kind.MovePath = tc.kind, tc.move
		item := observation.Item{Status: "completed", Changes: []observation.FileChange{change}}
		text := HostEditText(item, "/workspace")
		if text != tc.want+" · apply_patch" {
			t.Fatalf("%s/%s: %q", tc.kind, tc.move, text)
		}
		if got := Operations(text)[0].EditHeading(); got != tc.heading {
			t.Fatalf("%s/%s heading = %q, want %q", tc.kind, tc.move, got, tc.heading)
		}
	}
}
