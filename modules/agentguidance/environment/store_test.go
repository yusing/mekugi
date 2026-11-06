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
