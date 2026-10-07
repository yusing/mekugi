package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func waitRuntimeCommandPreview(t *testing.T, u *appServerUI, id, text string, complete bool) diffview.Preview {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-u.runtime.previewReady:
			u.applyRuntimeCommandPreviews()
			if v := u.shell.diff.previewPane.Views[id]; v != nil && v.Current.Complete == complete && len(v.Current.Files) == 1 && strings.Contains(v.Current.Files[0].Diff, text) {
				return v.Current
			}
		case <-deadline.C:
			t.Fatal("native command proposal did not reach shared pane")
		}
	}
}

func TestNativeRuntimeCommandPreviewInputAndSettlement(t *testing.T) {
	t.Parallel()
	u, path := runtimePreviewFixture(t, "before\n")
	t.Cleanup(u.closeRuntimeCommandPreviews)
	prefix := "cat > example.txt <<'EOF'\nstreamed台\n"
	e := session.Event{Kind: "command_preview", ID: "bash", Role: "Bash", Caller: "child", CommandInput: &session.CommandInput{Text: prefix}}
	runtimePreviewEvent(t, u, e)
	p := waitRuntimeCommandPreview(t, u, e.ID, "+streamed台", false)
	if p.Caller != "native/child" || p.Input != "" || p.Footer != "Proposed input · not saved edit evidence" {
		t.Fatalf("shared proposal identity or evidence: %+v", p)
	}
	runtimeAssertNoCapture(t, u)
	e.CommandInput = &session.CommandInput{Text: prefix + "FINAL_ROW\nEOF\n", Complete: true}
	u.shell.diff.diffMode = true // A late final input cannot replace saved selection.
	runtimePreviewEvent(t, u, e)
	waitRuntimeCommandPreview(t, u, e.ID, "+FINAL_ROW", true)
	if !u.shell.diff.diffMode {
		t.Fatal("completed proposal replaced the saved Diff view")
	}
	runtimePreviewEvent(t, u, session.Event{Kind: "tool_result", ID: e.ID, Caller: "child", Failed: true, Text: "Denied"})
	if len(u.runtime.commandPreviews) != 0 {
		t.Fatal("native result left a command proposal worker registered")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != "before\n" {
		t.Fatalf("proposal executed effects: %q, %v", actual, err)
	}
	runtimeAssertNoCapture(t, u)
}

func TestNativeRuntimeCommandPreviewHistoryAndRetirement(t *testing.T) {
	t.Parallel()
	u, _ := runtimePreviewFixture(t, "before\n")
	e := session.Event{Kind: "command_preview", ID: "bash", Historical: true, CommandInput: &session.CommandInput{Text: "cat > example.txt <<'EOF'\nlive\n"}}
	runtimePreviewEvent(t, u, e)
	if u.runtime.previewBroker != nil {
		t.Fatal("native history revived a live proposal worker")
	}
	e.Historical = false
	runtimePreviewEvent(t, u, e)
	waitRuntimeCommandPreview(t, u, e.ID, "+live", false)
	runtimeRevealPreview(u)
	p := u.runtime.commandPreviews[e.ID]
	runtimePreviewEvent(t, u, session.Event{Kind: "session", SessionID: "next-session"})
	waitLiveDiffWorkerDone(t, p.worker)
	if u.runtime.previewReady != nil || u.runtime.previewBroker != nil || len(u.runtime.commandPreviews) != 0 {
		t.Fatal("retired query left command preview resources")
	}
	if u.shell.liveDock.Live() != 0 || u.shell.diff.previewPane.Live() != 0 || len(u.shell.livePending) != 0 {
		t.Fatal("query retirement left visible or pending live command proposals")
	}
}

func TestNativeRuntimeCommandPreviewFinalDrain(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"pwd", "cat > example.txt <<'EOF'\nfinal proposal\nEOF\n"} {
		for _, resultFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("edit=%t/result-first=%t", command != "pwd", resultFirst), func(t *testing.T) {
				t.Parallel()
				u, _ := runtimePreviewFixture(t, "before\n")
				t.Cleanup(u.closeRuntimeCommandPreviews)
				runtimePreviewEvent(t, u, session.Event{Kind: "command_preview", ID: "finished", CommandInput: &session.CommandInput{Text: command, Complete: true}})
				worker := u.runtime.commandPreviews["finished"].worker
				result := session.Event{Kind: "tool_result", ID: "finished", Text: "native result"}
				if resultFirst {
					runtimePreviewEvent(t, u, result)
				}
				waitLiveDiffWorkerDone(t, worker)
				if resultFirst {
					u.reapRuntimeCommandPreviews() // The native terminal clock drains late projection.
				} else {
					runtimePreviewEvent(t, u, result)
				}
				if len(u.runtime.commandPreviews) != 0 {
					t.Fatal("completed command retained a proposal registration")
				}
				view := u.shell.diff.previewPane.Views["finished"]
				if command == "pwd" {
					if view != nil {
						t.Fatal("ordinary Bash manufactured a proposal")
					}
				} else if view == nil || !view.Current.Complete || len(view.Current.Files) != 1 || !strings.Contains(view.Current.Files[0].Diff, "+final proposal") {
					t.Fatal("native settlement lost the pending final projection")
				}
				runtimeAssertNoCapture(t, u)
			})
		}
	}
}

func TestNativeRuntimeCommandPreviewShellRestart(t *testing.T) {
	t.Parallel()
	u, _ := runtimePreviewFixture(t, "before\n")
	t.Cleanup(u.closeRuntimeCommandPreviews)
	runtimePreviewEvent(t, u, session.Event{Kind: "command_preview", ID: "bash", CommandInput: &session.CommandInput{Text: "cat > example.txt <<'EOF'\nproposal\n"}})
	waitRuntimeCommandPreview(t, u, "bash", "+proposal", false)
	runtimeRevealPreview(u)
	runtimePreviewEvent(t, u, session.Event{Kind: "shell_restarting"})
	runtimePreviewEvent(t, u, session.Event{Kind: "shell_restarted"})
	u.shell.animating(time.Now().Add(time.Hour))
	if u.shell.liveDock.Live() != 0 || u.shell.diff.previewPane.Live() != 0 {
		t.Fatal("same-session shell restart kept a retired live proposal")
	}
}

func TestNativeRuntimeCommandPreviewMailboxRecovery(t *testing.T) {
	t.Parallel()
	u, _ := runtimePreviewFixture(t, "before\n")
	t.Cleanup(u.closeRuntimeCommandPreviews)
	input := &session.CommandInput{Text: "cat > example.txt <<'EOF'\nproposal\n"}
	runtimePreviewEvent(t, u, session.Event{Kind: "command_preview", ID: "seed", CommandInput: input})
	waitRuntimeCommandPreview(t, u, "seed", "+proposal", false)
	for i := range 33 {
		u.runtime.previewBroker.publishPreview(diffview.Preview{ID: fmt.Sprint(i), Workspace: u.session.cwd, Thread: u.thread, Complete: true, Status: diffview.PreviewEdit, Input: "fixture", DiffText: true}, false)
	}
	u.applyRuntimeCommandPreviews()
	runtimePreviewEvent(t, u, session.Event{Kind: "command_preview", ID: "after-gap", CommandInput: input})
	waitRuntimeCommandPreview(t, u, "after-gap", "+proposal", false)
}

func TestUISnapshotNativeRuntimeCommandPreview(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		width := 120
		name := "main"
		if child {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, _ := runtimePreviewFixture(t, "before\n")
			t.Cleanup(u.closeRuntimeCommandPreviews)
			caller := ""
			if child {
				caller = "spawn-call"
			}
			runtimePreviewEvent(t, u, session.Event{Kind: "command_preview", ID: "bash", Role: "Bash", Caller: caller, CommandInput: &session.CommandInput{Text: "cat > example.txt <<'EOF'\nstreamed command proposal\n"}})
			waitRuntimeCommandPreview(t, u, "bash", "+streamed command proposal", false)
			runtimeRevealPreview(u)
			fixture := "native-runtime-command-preview-120.txt"
			if child {
				// Native task identity can arrive after the tool input stream.
				runtimePreviewEvent(t, u, session.Event{Kind: "edit", ID: "write", Caller: caller, Role: "Write", Edit: &session.Edit{Path: "child.txt", Content: "child write\n", Partial: true}})
				runtimeRevealPreview(u)
				runtimePreviewEvent(t, u, session.Event{Kind: "task", Task: &session.Task{ID: "child-id", ToolID: caller, Kind: "local_agent", Status: "running"}})
				for _, id := range []string{"bash", "write"} {
					if p := u.shell.diff.previewPane.Views[id]; p == nil || p.Current.Caller != runtimeTaskLane("child-id") {
						t.Fatal("late task correlation did not restore the selected child proposal lane")
					}
				}
				u.agents.selected, u.agents.only = runtimeTaskLane("child-id"), true
				runtimeKeys(t, u, "\x02"+"3")
				runtimeFrame(t, u, width, 32) // The shared batch layout selects its current unit.
				runtimeKeys(t, u, "\x02"+"e")
				fixture = "native-runtime-command-preview-child-120.txt"
			}
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fixture), runtimeFrame(t, u, width, 32))
		})
	}
}
