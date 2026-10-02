package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func observedRuntimeUIFixture(t *testing.T) (*appServerUI, *ObservationService, ObservationBinding) {
	t.Helper()
	u, _ := runtimeTestUI(t)
	service, binding, _ := observationHTTPFixture(t)
	u.session.cwd = binding.Workspace
	u.shell.diff.workspace = binding.Workspace
	u.attachRuntimeObservation(service)
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		u.applyRuntimeObservation(<-u.runtime.observationEvents)
	}
	return u, service, binding
}
func TestNativeRuntimeObservationMailboxAndStickyMode(t *testing.T) {
	u, service, binding := observedRuntimeUIFixture(t)
	u.runtime.busy = true
	call := ObservationCall{Binding: binding, ID: "write", Tool: "Write", Input: `{}`, Paths: []string{"example.txt"}}
	if err := service.owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, filepath.Join(binding.Workspace, "example.txt"), "captured content\n")
	if _, err := service.owner.after(t.Context(), call, ObservationTerminal{Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		u.applyRuntimeObservation(<-u.runtime.observationEvents)
	}
	if len(u.shell.diff.view.Files) != 1 {
		t.Fatal("saved capture missing from shared controller")
	}
	if err := u.runtimeEvent(session.Event{Kind: "done"}); err != nil {
		t.Fatal(err)
	}
	if !u.shell.diff.diffMode {
		t.Fatal("settled completion did not select saved view")
	}
	u.shell.diff.handleKey('v')
	if u.shell.diff.diffMode {
		t.Fatal("live mode control disabled")
	}
	u.applyRuntimeObservation(liveDiffEvent{Kind: "turn", Status: "completed", TurnRevision: 3})
	if u.shell.diff.diffMode {
		t.Fatal("update stole user-selected mode")
	}
	// Overflow recovery is a store snapshot, not native tool replay.
	service.owner.broker.resync(service.owner.broker.scope)
	u.attachRuntimeObservation(service)
	u.applyRuntimeObservation(<-u.runtime.observationEvents)
	if len(u.shell.diff.view.Files) != 1 || u.shell.diff.diffMode {
		t.Fatal("resnapshot lost evidence or user mode")
	}
}
func TestUISnapshotNativeRuntimeSavedObservation(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, service, binding := observedRuntimeUIFixture(t)
			call := ObservationCall{Binding: binding, ID: "write", Tool: "Write", Input: `{}`, Paths: []string{"example.txt"}}
			nativeObservationWrite(t, filepath.Join(binding.Workspace, "example.txt"), "before\n")
			if err := service.owner.before(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			nativeObservationWrite(t, filepath.Join(binding.Workspace, "example.txt"), "after observed\n")
			if _, err := service.owner.after(t.Context(), call, ObservationTerminal{Status: "failed"}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				u.applyRuntimeObservation(<-u.runtime.observationEvents)
			}
			u.shell.diffOpen = true
			u.shell.focus = 1
			// Hide tmp-workspace identities and provider time from reviewed snapshots.
			u.thread = "native-session"
			u.session.cwd = "/workspace"

			frame := runtimeFrame(t, u, width, 32)
			frame = strings.ReplaceAll(frame, binding.Workspace, "/workspace")
			name := "native-runtime-saved-observation-42.txt"
			if width == 120 {
				name = "native-runtime-saved-observation-120.txt"
			}
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", name), frame)
		})
	}
}

func TestNativeRuntimeObservationAutomaticModeRepaints(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			u, service, binding := observedRuntimeUIFixture(t)
			u.runtime.busy = true
			u.shell.diffOpen = true
			runtimePreviewEvent(t, u, session.Event{Kind: "edit", ID: "write", Role: "Write", Edit: &session.Edit{Path: "example.txt", Content: "PROPOSED_CONTENT", Partial: true}})
			runtimePreviewEvent(t, u, session.Event{Kind: "tool_result", ID: "write", Role: "Write", Text: "native result"})
			call := ObservationCall{Binding: binding, ID: "write", Tool: "Write", Input: `{}`, Paths: []string{"example.txt"}}
			if err := service.owner.before(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			pending := ObservationCall{Binding: binding, ID: "pending", Tool: "Bash", Input: `{}`, Command: "true", Shell: "bash"}
			if late {
				if err := service.owner.before(t.Context(), pending); err != nil {
					t.Fatal(err)
				}
			}
			nativeObservationWrite(t, filepath.Join(binding.Workspace, "example.txt"), "SAVED_CONTENT\n")
			if _, err := service.owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				u.applyRuntimeObservation(<-u.runtime.observationEvents)
			}
			before := runtimeFrame(t, u, 120, 32)
			if !strings.Contains(before, "PROPOSED_CONTENT") {
				t.Fatal("fixture never painted the proposal")
			}
			runtimePreviewEvent(t, u, session.Event{Kind: "done"})
			if late {
				_ = runtimeFrame(t, u, 120, 32)
				if _, err := service.owner.after(t.Context(), pending, ObservationTerminal{Status: "completed"}); err != nil {
					t.Fatal(err)
				}
				u.applyRuntimeObservation(<-u.runtime.observationEvents)
			}
			after := runtimeFrame(t, u, 120, 32)
			if !u.shell.diff.diffMode || strings.Contains(after, "PROPOSED_CONTENT") || !strings.Contains(after, "SAVED_CONTENT") {
				t.Fatalf("automatic saved mode retained stale proposals:\n%s", after)
			}
		})
	}
}
