package launch

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func newShellEditTestUI(t *testing.T) *appServerUI {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	setTestAppServerIntegration(u, proxy)
	if err := openRetainedSession(u.ctx, u.history, &u.retainedSession, u.Thread, u.retained.Store, u.retained.Writes, u.retained.Limits); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.retainedSession.Close)
	u.shellEdits.shell = execTrackShellExecutable(t, "bash")
	return u
}

func startShellEditTestCommand(t *testing.T, u *appServerUI, command, item string) *executionobservation.ShellOperation {
	t.Helper()
	u.Text = "!" + command
	if err := submitShell(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, u.restoring != nil, u.skillEnvironment, u.ctx, u.review, &u.shellEdits, u.retained, u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane); err != nil {
		t.Fatal(err)
	}
	edit := u.shellEdits.pending
	if u.Turn == "" {
		appServerTestTurn(t, u, "shell-turn")
	}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": u.Thread, "turnId": u.Turn,
		"item": binding.ThreadItem{ID: item, Type: "commandExecution", Source: "userShell", Command: command, Status: "inProgress"}})
	shellResponse(&u.attached, u.shell, &u.Client, &u.Editor, u.clock, &u.shellEdits, nil)
	if edit != nil && u.shellEdits.running[[2]string{u.Thread, item}] != edit {
		t.Fatalf("shell start was not matched: pending=%+v running=%+v notice=%s", u.shellEdits.pending, u.shellEdits.running, u.attached.feedback.Text)
	}
	return edit
}

func completeShellEditTestCommand(t *testing.T, u *appServerUI, item string, exit int) {
	t.Helper()
	status := "completed"
	if exit != 0 {
		status = "failed"
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": u.Thread, "turnId": u.Turn,
		"item": binding.ThreadItem{ID: item, Type: "commandExecution", Source: "userShell", Status: status, ExitCode: &exit}})
	if u.Shell.Standalone() {
		appServerTestTurnEnd(t, u, "shell-turn", "completed")
	}
}

func TestAppServerShellEditsRevertApplyReplay(t *testing.T) {
	u := newShellEditTestUI(t)
	workspace, store := u.attached.cwd, testUIStore(u)
	ctx := context.WithValue(u.retainedSession.Context, storageSessionKey{}, storageSessionIdentity{Thread: u.retainedSession.Thread, Namespace: u.retainedSession.Namespace})
	path := filepath.Join(workspace, "ignored.txt")
	// Source-named mchanges targets must not depend on workspace snapshot admission.
	runExecVCSTestGit(t, workspace, "init", "-q")
	writeTestFile(t, filepath.Join(workspace, ".gitignore"), "ignored.txt\n")
	writeTestFile(t, path, "after\n")
	id := putTestChange(t, ctx, store, workspace, "original", diffmodel.RenderReviewFile(path, path, "before\n", "after\n"))
	for index, mutation := range []string{"revert", "apply"} {
		item := fmt.Sprintf("shell-%d", index)
		edit := startShellEditTestCommand(t, u, "mchanges "+mutation+" "+id, item)
		if edit == nil {
			t.Fatal("shell mutation has no pre-submission observation")
		}
		if _, found := lookupShellTestRecord(t, store, workspace, u.Thread, item); found {
			t.Fatal("start allocated completed evidence")
		}
		if output, exit := mutateTestChanges(t, ctx, store, workspace, mutation, id); exit != 0 {
			t.Fatalf("mutation failed: %s", output)
		}
		completeShellEditTestCommand(t, u, item, 0)
		if u.attached.feedback.Alert {
			t.Fatal(u.attached.feedback.Text)
		}
		// Duplicate notifications and resumed host history cannot allocate another capture.
		completeShellEditTestCommand(t, u, item, 0)
		fresh, err := openMekugiReplayStore(store.directory)
		if err != nil {
			t.Fatal(err)
		}
		history := shellTestRecord(t, fresh, workspace, u.Thread, item).History
		if history.ChangeID == "" || len(history.ReviewFiles) != 1 || len(history.HostResults) != 1 || history.HostResults[0].CallID != item || history.HostResults[0].Tool != "userShell" {
			t.Fatalf("durable shell edit: %+v", history)
		}
		text, err := fresh.changes.Read(ctx, fresh.scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{history.ChangeID}, View: "history"})
		if err != nil || !strings.Contains(text, "mchanges "+mutation+" "+id) {
			t.Fatalf("shell change history: %q, %v", text, err)
		}
		data, err := fresh.liveDiffSnapshot(ctx, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {u.Thread: true, "stock-thread": true}}})
		if err != nil || data.Len() != index+2 {
			t.Fatalf("Changes history: %+v, %v", data, err)
		}
		u.review.Projection.Data = data
		publishFixtureReview(t, u)
		savedPane(terminalTest(u)).Update(data.Publication, "")
		counts := u.review.Projection.Data.Publication.Counts.Net
		if counts == nil || index == 0 && (counts.Added != 0 || counts.Removed != 0) || index == 1 && (counts.Added != 1 || counts.Removed != 1) {
			t.Fatalf("saved Diff after %s: files=%+v counts=%+v", mutation, data.Publication.Files, counts)
		}
	}
}

func TestAppServerShellEditsFailedWriterAndNoEffect(t *testing.T) {
	u := newShellEditTestUI(t)
	appServerTestTurn(t, u, "model")
	for _, command := range []string{"printf 'saved\\n' > \"$SHELL_EDIT_TARGET\"; exit 7", "rm -f missing.txt"} {
		startShellEditTestCommand(t, u, command, command)
		cmd := exec.Command(u.shellEdits.shell, "-c", command)
		cmd.Dir = u.attached.cwd
		cmd.Env = append(mchangesSliceInvocation(u.attached.cwd, u.Thread).environment, "SHELL_EDIT_TARGET=saved.txt")
		err := cmd.Run()
		exit := 0
		if err != nil {
			exit = err.(*exec.ExitError).ExitCode()
		}
		// Presentation may discard an old thread's completion after a switch.
		u.RetireThreads(u.Thread)
		completeShellEditTestCommand(t, u, command, exit)
		u.Readmit(u.Thread)
		if u.Turn != "model" {
			t.Fatal("shell completion ended the model turn")
		}
		if u.attached.feedback.Alert {
			t.Fatal(u.attached.feedback.Text)
		}
		history := shellTestRecord(t, testUIStore(u), u.attached.cwd, u.Thread, command).History
		if history.ExecOutcome.Exit == nil || *history.ExecOutcome.Exit != exit {
			t.Fatalf("shell outcome: %+v", history)
		}
		if exit == 7 && history.ChangeID == "" || exit == 0 && history.ChangeID != "" {
			t.Fatalf("saved edits depend on command status: %+v", history)
		}
	}
	// A rejected request closes its window without capturing external edits.
	u.Text = "!printf rejected > saved.txt"
	if err := submitShell(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, u.restoring != nil, u.skillEnvironment, u.ctx, u.review, &u.shellEdits, u.retained, u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane); err != nil {
		t.Fatal(err)
	}
	shellResponse(&u.attached, u.shell, &u.Client, &u.Editor, u.clock, &u.shellEdits, &appserver.Error{Message: "rejected"})
	if u.shellEdits.pending != nil {
		t.Fatal("rejected shell retained its observation")
	}
}

func TestAppServerShellObservationKeepsOriginalOwnersAfterSwitch(t *testing.T) {
	u := newShellEditTestUI(t)
	thread, workspace, retained := u.Thread, u.attached.cwd, testUIStore(u)
	path := filepath.Join(workspace, "selected.txt")
	writeTestFile(t, path, "before\n")
	edit := startShellEditTestCommand(t, u, "printf 'after\\n' > selected.txt", "shell-original")
	if edit == nil {
		t.Fatal("missing shell observation")
	}
	other, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.snapshots.Close)
	setTestUIStore(u, other)
	u.Thread = "other-thread"
	u.startSession(u.Thread, t.TempDir())
	if err := openRetainedSession(u.ctx, u.history, &u.retainedSession, u.Thread, other.publication, other.writes, other.retentionLimits()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.retainedSession.Close)
	writeTestFile(t, path, "after\n")
	exit := 0
	observeShellEdit(&u.shellEdits, &u.attached, u.shell, u.clock, thread, "item/completed", binding.ThreadItem{ID: "shell-original", Type: "commandExecution", Source: "userShell", Status: "completed", ExitCode: &exit})
	if u.attached.feedback.Alert {
		t.Fatal(u.attached.feedback.Text)
	}
	record := shellTestRecord(t, retained, workspace, thread, "shell-original")
	id := record.CallID
	if record.History.ExecutingThread != thread || record.History.ChangeID == "" {
		t.Fatalf("original observation: %+v", record)
	}
	if _, found, err := other.publication.LookupHostCall(t.Context(), workspace, id); err != nil || found {
		t.Fatalf("observation crossed retained owners: %v, %v", found, err)
	}
}
