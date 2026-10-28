package executionobservation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/libraries/legacycall"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestClaudeNativeToolCapture(t *testing.T) {
	for _, scenario := range []string{"edit", "write", "notebook", "failed-bash", "failed-powershell", "interrupted", "partial", "no-effect"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.snapshots.Close)
			ctx, _ := retentionTestSession(t, store, "child", 0)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			workspace := t.TempDir()
			path := filepath.Join(workspace, "target.txt")
			if scenario != "write" {
				writeTestFile(t, path, "before\n")
			}
			if scenario == "partial" {
				if err := os.Truncate(path, changecapture.MaxFileBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			input := ClaudeToolInput{Thread: "child", Turn: "turn", Namespace: "child", Workspace: workspace, Caller: "/root/worker", ToolUseID: "native-id", ToolName: "Edit",
				Arguments: []byte(`{ "file_path":"target.txt", "old_string":"before", "new_string":"after" }`)}
			if scenario == "write" {
				input.ToolName, input.Arguments = "Write", []byte(`{"file_path":"target.txt","content":"after\n"}`)
			}
			if scenario == "failed-bash" {
				input.ToolName, input.Shell = "Bash", "/bin/bash"
				input.Arguments = []byte(`{"command":"printf 'after\\n' > target.txt; printf 'failure detail'; exit 7"}`)
			}
			if scenario == "notebook" {
				input.ToolName, input.Arguments = "NotebookEdit", []byte(`{"notebook_path":"target.txt","new_source":"after","edit_mode":"replace"}`)
			}
			if scenario == "failed-powershell" {
				input.ToolName, input.Shell = "PowerShell", "bash"
				input.Arguments = []byte(`{"command":"Set-Content target.txt 'after'; exit 7"}`)
			}
			selected := store.scoped(ctx).changeScope()
			windows := &Windows{}
			audience := sessioncontrol.NewAudience(nil)
			audience.Observe("parent", "", "/root", false)
			audience.Observe("child", "parent", input.Caller, true)
			audience.Attach("parent")
			published := 0
			operation, err := PrepareClaudeTool(ctx, selected, store.changes, store.snapshots, store.recovery, func(updates []changes.Update) {
				for _, update := range updates {
					text, err := store.changes.Read(context.WithoutCancel(ctx), selected, changes.Query{Workspace: workspace, IDs: []string{update.ID}, View: "summary"})
					if err != nil || !strings.Contains(text, "target.txt") {
						t.Errorf("publication preceded readable evidence: %q, %v", text, err)
					}
					published++
				}
			}, windows, audience, nil, input)
			if err != nil || operation == nil {
				t.Fatalf("prepare: %v, %v", operation, err)
			}
			if scenario == "failed-powershell" {
				observation := operation.operation.history.ExecObservation
				if len(observation.Commands) != 1 || observation.Commands[0].Shell != "pwsh" || observation.Tree == "" || !strings.Contains(observation.Reason, "not parsed") {
					t.Fatalf("PowerShell must use actual-shell workspace capture: %+v", observation)
				}
			}
			t.Cleanup(operation.Discard)
			outcome := ClaudeToolOutcome{Terminal: true, Succeeded: true, Status: "completed", Output: "original native result"}
			switch scenario {
			case "failed-bash":
				command := exec.CommandContext(ctx, "/bin/bash", "-c", operation.operation.history.Script)
				command.Dir = workspace
				output, err := command.CombinedOutput()
				if err == nil || command.ProcessState.ExitCode() != 7 {
					t.Fatalf("native command outcome: %q, %v", output, err)
				}
				outcome.Output, outcome.ExitCode, outcome.Succeeded, outcome.Status = string(output), new(7), false, "failed"
			case "no-effect":
			case "failed-powershell":
				writeTestFile(t, path, "after\n")
				outcome.Succeeded, outcome.Status, outcome.ExitCode = false, "failed", new(7)
			default:
				writeTestFile(t, path, "after\n")
				if scenario == "interrupted" {
					outcome.Succeeded, outcome.Status = false, "interrupted"
					cancel()
				}
			}
			original := outcome.Output
			receipt, err := operation.Complete(outcome)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := operation.Complete(outcome)
			if err != nil || repeated != receipt || outcome.Output != original {
				t.Fatalf("changed native output or repeated receipt: %q, %q, %v", receipt, repeated, err)
			}
			retained, found, err := store.lookup(context.WithoutCancel(ctx), workspace, legacycall.ExecCallID(operation.operation.call, false))
			if err != nil || !found || retained.ToolName != input.ToolName || retained.CarrierName != input.ToolName || retained.CarrierPayload != string(input.Arguments) || retained.ReplayCarrier || retained.ExecutingThread != "child" || retained.Caller != "/root/worker" {
				t.Fatalf("native identity lost: %+v, %v, %v", retained, found, err)
			}
			if input.ToolName != "Bash" && input.ToolName != "PowerShell" && retained.Script != string(input.Arguments) {
				t.Fatalf("native input replaced with invented source: %q", retained.Script)
			}
			if len(retained.HostResults) != 1 || retained.HostResults[0].CallID != input.ToolUseID || retained.HostResults[0].Tool != input.ToolName || retained.HostResults[0].Status != outcome.Status || retained.Report != original && scenario != "partial" {
				t.Fatalf("native result changed: %+v", retained)
			}
			if scenario == "interrupted" && (retained.ExecOutcome.Exit != nil || retained.HostResults[0].ExitCode != nil) {
				t.Fatal("interruption invented an exit code")
			}
			_, live := audience.Take("parent")
			if scenario == "no-effect" {
				if receipt != "" || retained.ChangeID != "" || published != 0 || len(live) != 0 {
					t.Fatalf("no-effect edit allocated evidence: %q, %+v", receipt, retained)
				}
				return
			}
			if !strings.Contains(receipt, "mchanges "+retained.ChangeID+" --summary") || !strings.Contains(receipt, "target.txt") || published != 1 {
				t.Fatalf("missing durable receipt: %q, publications=%d", receipt, published)
			}
			if len(live) != 1 || live[0].Kind != activitypresentation.SupplementEditReceipt || live[0].Thread != input.Thread || !slices.Equal(live[0].Calls, []string{input.ToolUseID}) {
				t.Fatalf("live receipt lost native host identity: %+v", live)
			}
			// Restored review must retain the same native call and evidence even
			// after unrelated writes, without a live operation or Activity state.
			writeTestFile(t, path, "unobserved later content\n")
			resumed, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(resumed.snapshots.Close)
			snapshot, err := resumed.scoped(ctx).liveDiffSnapshot(context.WithoutCancel(ctx), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {input.Thread: true}}})
			if err != nil {
				t.Fatal(err)
			}
			if restored := snapshot.Receipts(); len(restored) != 1 || restored[0].Thread != input.Thread || !slices.Equal(restored[0].Calls, live[0].Calls) || restored[0].Text != live[0].Text {
				t.Fatalf("restored receipt differs from native live receipt: %+v, live=%+v", restored, live)
			}
			history, err := store.changes.Read(context.WithoutCancel(ctx), selected, changes.Query{Workspace: workspace, IDs: []string{retained.ChangeID}, View: "history"})
			if err != nil || !strings.Contains(history, input.ToolName+" input:\n"+retained.Script) {
				t.Fatalf("native history input unavailable: %q, %v", history, err)
			}
			if scenario == "partial" {
				if !strings.Contains(receipt, "capture bound") || len(retained.ReviewFiles) != 1 || retained.ReviewFiles[0].Incomplete == "" {
					t.Fatalf("incomplete evidence claimed complete: %q, %+v", receipt, retained.ReviewFiles)
				}
				return
			}
			diff, err := store.changes.Read(context.WithoutCancel(ctx), selected, changes.Query{Workspace: workspace, IDs: []string{retained.ChangeID}})
			if err != nil || !strings.Contains(diff, "+after") || scenario != "write" && !strings.Contains(diff, "-before") {
				t.Fatalf("ordinary mchanges evidence: %q, %v", diff, err)
			}
		})
	}
}

func TestClaudeCaptureBackgroundAndSharedNamespace(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.Close)
	ctx, _ := retentionTestSession(t, store, "parent", 0)
	bindTestHandleScope(t, store, ctx, "", "")
	if err := store.publication.Transaction(ctx, store.writes, func(tx *history.Transaction) error {
		return tx.InitializeScope(history.Scope{Thread: "child", Namespace: "parent", Parent: "parent"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	selected := store.scoped(ctx).changeScope()
	windows := &Windows{}
	input := ClaudeToolInput{Thread: "child", Turn: "turn", Namespace: "parent", Workspace: workspace, ToolUseID: "background", ToolName: "Bash", Shell: "/bin/bash", Arguments: []byte(`{"command":"printf 'done\\n' > new.txt","run_in_background":true}`)}
	operation, err := PrepareClaudeTool(ctx, selected, store.changes, store.snapshots, store.recovery, nil, windows, nil, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Discard()
	writeTestFile(t, filepath.Join(workspace, "new.txt"), "done\n")
	if receipt, err := operation.Complete(ClaudeToolOutcome{Output: "background task accepted"}); err != nil || receipt != "" {
		t.Fatalf("acknowledgement completed capture: %q, %v", receipt, err)
	}
	if !windows.find(operation.operation.call).closed.IsZero() {
		t.Fatal("background capture closed early")
	}
	if _, found, err := store.lookup(ctx, workspace, legacycall.ExecCallID(operation.operation.call, false)); err != nil || found {
		t.Fatalf("background acknowledgement published outcome: %v, %v", found, err)
	}
	receipt, err := operation.Complete(ClaudeToolOutcome{Terminal: true, Succeeded: true, Status: "completed"})
	if err != nil || receipt == "" {
		t.Fatalf("terminal receipt: %q, %v", receipt, err)
	}
	retained, _, _ := store.lookup(ctx, workspace, legacycall.ExecCallID(operation.operation.call, false))
	parentList, err := store.changes.Read(ctx, selected, changes.Query{Workspace: workspace, View: "list"})
	if err != nil || strings.Contains(parentList, retained.ChangeID) {
		t.Fatalf("child attribution leaked into parent implicit selection: %q, %v", parentList, err)
	}
	diff, err := store.changes.Read(ctx, selected, changes.Query{Workspace: workspace, IDs: []string{retained.ChangeID}})
	if err != nil || !strings.Contains(diff, "+done") {
		t.Fatalf("parent explicit handoff failed: %q, %v", diff, err)
	}
	input.ToolUseID = "unfinished"
	unfinished, err := PrepareClaudeTool(ctx, selected, store.changes, store.snapshots, store.recovery, nil, windows, nil, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	unfinished.Discard()
	if receipt, err := unfinished.Complete(ClaudeToolOutcome{Terminal: true}); err == nil || receipt != "" || windows.find(unfinished.operation.call).closed.IsZero() {
		t.Fatalf("discard invented an outcome: %q, %v", receipt, err)
	}
}

type claudePublicationFailure struct {
	changes.Service
	fail bool
}

func (s *claudePublicationFailure) Publish(tx *history.Transaction, selected changes.Scope, workspace string, publications map[string]changes.Publication) ([]changes.Update, error) {
	if s.fail {
		return nil, errors.New("publication unavailable")
	}
	return s.Service.Publish(tx, selected, workspace, publications)
}

func TestClaudeCaptureReceiptWaitsForPublication(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.Close)
	ctx, _ := retentionTestSession(t, store, "child", 0)
	workspace := t.TempDir()
	source := &claudePublicationFailure{Service: store.changes, fail: true}
	operation, err := PrepareClaudeTool(ctx, store.scoped(ctx).changeScope(), source, store.snapshots, store.recovery, nil, &Windows{}, nil, nil,
		ClaudeToolInput{Thread: "child", Namespace: "child", Workspace: workspace, ToolUseID: "write", ToolName: "Write", Arguments: []byte(`{"file_path":"file.txt","content":"effect"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Discard()
	writeTestFile(t, filepath.Join(workspace, "file.txt"), "effect\n")
	outcome := ClaudeToolOutcome{Terminal: true, Succeeded: true, Status: "completed"}
	if receipt, err := operation.Complete(outcome); err == nil || receipt != "" {
		t.Fatalf("failed publication advertised receipt: %q, %v", receipt, err)
	}
	// A retry publishes the already retained effect, never a new endpoint read.
	writeTestFile(t, filepath.Join(workspace, "file.txt"), "later unrelated effect\n")
	source.fail = false
	receipt, err := operation.Complete(outcome)
	if err != nil || !strings.Contains(receipt, "file.txt") {
		t.Fatalf("publication retry: %q, %v", receipt, err)
	}
	retained, _, _ := store.lookup(ctx, workspace, legacycall.ExecCallID(operation.operation.call, false))
	diff, err := store.changes.Read(ctx, store.scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{retained.ChangeID}})
	if err != nil || !strings.Contains(diff, "+effect") || strings.Contains(diff, "unrelated") {
		t.Fatalf("retry reread filesystem instead of retained evidence: %q, %v", diff, err)
	}
}

func TestClaudeNamedTargetFollowsSymlink(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.Close)
	ctx, _ := retentionTestSession(t, store, "child", 0)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "ignored.generated")
	writeTestFile(t, path, "before\n")
	if err := os.Symlink(path, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	operation, err := PrepareClaudeTool(ctx, store.scoped(ctx).changeScope(), store.changes, store.snapshots, store.recovery, nil, &Windows{}, nil, nil,
		ClaudeToolInput{Thread: "child", Namespace: "child", Workspace: workspace, ToolUseID: "write", ToolName: "Write", Arguments: []byte(`{"file_path":"link","content":"after"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Discard()
	writeTestFile(t, filepath.Join(workspace, "link"), "after\n")
	receipt, err := operation.Complete(ClaudeToolOutcome{Terminal: true, Succeeded: true, Status: "completed"})
	if err != nil || !strings.Contains(receipt, "ignored.generated") {
		t.Fatalf("named write-through target lost: %q, %v", receipt, err)
	}
}

func TestClaudeCaptureMissingNativeBaseline(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.Close)
	ctx, _ := retentionTestSession(t, store, "child", 0)
	input := ClaudeToolInput{Thread: "child", Namespace: "child", Workspace: t.TempDir(), ToolUseID: "native", ToolName: "PowerShell", Arguments: []byte(`{"command":"Set-Content target.txt 'after'"}`)}
	for _, workdir := range []string{"", "relative"} {
		input.Workdir = workdir
		operation, err := PrepareClaudeTool(ctx, store.scoped(ctx).changeScope(), store.changes, nil, store.recovery, nil, &Windows{}, nil, nil, input)
		want := "no available workspace baseline"
		if workdir != "" {
			want = "absolute working directory"
		}
		if operation != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("unavailable baseline silently accepted: %v, %v", operation, err)
		}
	}
}
