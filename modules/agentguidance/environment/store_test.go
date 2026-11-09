package environment

import (
	"reflect"
	"testing"
)

func TestStoreForkSnapshotLifecycle(t *testing.T) {
	var store Store
	workspace := t.TempDir()
	parent := Context{Session: "main-session", Thread: "main", Workspace: workspace, Revision: 1}
	original := store.Snapshot(t.Context(), parent, 0)
	child := Context{Thread: "side", Workspace: workspace}
	store.Inherit(parent.Thread, child)
	// Freeze at admission, not at the child's first request after Main refreshes.
	parent.Revision++
	refreshed := store.Snapshot(t.Context(), parent, 1)
	child.Session, child.Revision = "native-side-session", 1
	if got := store.Snapshot(t.Context(), child, 0); !reflect.DeepEqual(got, original) {
		t.Fatal("first native context window changed the inherited snapshot")
	}
	if got := store.Snapshot(t.Context(), child, 1); !reflect.DeepEqual(got, original) {
		t.Fatal("ordinary side request refreshed the snapshot")
	}
	child.Revision++
	if got := store.Snapshot(t.Context(), child, 1); got.ObservedAt == original.ObservedAt {
		t.Fatal("side context refresh retained the inherited snapshot")
	}
	if got := store.Snapshot(t.Context(), parent, 2); !reflect.DeepEqual(got, refreshed) {
		t.Fatal("side refresh changed Main's snapshot")
	}

	for _, scenario := range []string{"reset-before-first", "different-workspace", "unknown-source", "resume"} {
		t.Run(scenario, func(t *testing.T) {
			selected := Context{Thread: scenario, Workspace: workspace}
			source := parent.Thread
			if scenario == "different-workspace" {
				selected.Workspace = t.TempDir()
			}
			if scenario == "unknown-source" {
				source = "unadmitted"
			}
			store.Inherit(source, selected)
			selected.Session = "child-session"
			previous := uint64(0)
			if scenario == "reset-before-first" {
				selected.Revision, previous = 1, 1
			}
			got := store.Snapshot(t.Context(), selected, previous)
			if scenario == "resume" {
				if !reflect.DeepEqual(got, refreshed) {
					t.Fatal("same-workspace fork failed to inherit")
				}
				selected.Session = "resumed-session"
				got = store.Snapshot(t.Context(), selected, previous)
			}
			if got.ObservedAt == refreshed.ObservedAt {
				t.Fatal("reset, workspace, authority or session boundary reused source facts")
			}
		})
	}
}

func TestStoreRewindSnapshotLifecycle(t *testing.T) {
	for _, scenario := range []string{"same-workspace", "repeated-rewind", "different-workspace", "missing-workspace", "unknown-thread", "reset-before-rewind", "reset-before-request", "resume"} {
		t.Run(scenario, func(t *testing.T) {
			var store Store
			originalContext := Context{Session: "session", Thread: "main", Workspace: t.TempDir(), Revision: 1}
			original := store.Snapshot(t.Context(), originalContext, 0)
			selected := originalContext
			selected.Revision++
			switch scenario {
			case "different-workspace":
				selected.Workspace = t.TempDir()
			case "missing-workspace":
				selected.Workspace = ""
			case "unknown-thread":
				selected.Thread = "unknown"
			case "reset-before-rewind":
				selected.Revision++
			}
			store.Rewind(selected)
			if scenario == "repeated-rewind" {
				selected.Revision++
				store.Rewind(selected)
			}
			if scenario == "reset-before-request" || scenario == "resume" {
				selected.Revision++
			}
			previous := selected.Revision
			selected.Revision++ // The reloaded runtime's first native window.
			if scenario == "resume" {
				selected.Session = "resumed"
			}
			got := store.Snapshot(t.Context(), selected, previous)
			reused := scenario == "same-workspace" || scenario == "repeated-rewind"
			if reflect.DeepEqual(got, original) != reused {
				t.Fatalf("snapshot reused=%t, want %t", reflect.DeepEqual(got, original), reused)
			}
			if reused {
				selected.Revision++
				if later := store.Snapshot(t.Context(), selected, previous+1); later.ObservedAt == original.ObservedAt {
					t.Fatal("later native reset did not refresh")
				}
			}
		})
	}
}

func TestStoreCancelPendingReuse(t *testing.T) {
	for _, pending := range []bool{false, true} {
		var store Store
		selected := Context{Session: "session", Thread: "main", Workspace: t.TempDir(), Revision: 1}
		original := store.Snapshot(t.Context(), selected, 0)
		if pending {
			selected.Revision++
			store.Rewind(selected)
		}
		store.CancelReuse(selected.Thread)
		got := store.Snapshot(t.Context(), selected, selected.Revision)
		if reflect.DeepEqual(got, original) == pending {
			t.Fatalf("reset attempt changed ordinary snapshot or retained pending reuse: pending=%t", pending)
		}
	}
}
