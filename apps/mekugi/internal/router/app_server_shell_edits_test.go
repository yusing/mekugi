package router

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func newShellEditTestUI(t *testing.T) *appServerUI {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.proxy = newManagedMekugiProxy(t)
	attachTestReplayStore(t, u.proxy)
	if err := u.openWaitStore(u.proxy.replayStore); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.waitRelease)
	u.shellEdits.shell = execTrackShellExecutable(t, "bash")
	return u
}

func startShellEditTestCommand(t *testing.T, u *appServerUI, command, item string) *appServerShellEdit {
	t.Helper()
	u.Text = "!" + command
	if err := u.submitShell(); err != nil {
		t.Fatal(err)
	}
	edit := u.shellEdits.pending
	if u.Turn == "" {
		appServerTestTurn(t, u, "shell-turn")
	}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": u.Thread, "turnId": u.Turn,
		"item": binding.ThreadItem{ID: item, Type: "commandExecution", Source: "userShell", Command: command, Status: "inProgress"}})
	u.shellResponse(nil)
	if edit != nil && u.shellEdits.running[[2]string{u.Thread, item}] != edit {
		t.Fatalf("shell start was not matched: pending=%+v running=%+v notice=%s", u.shellEdits.pending, u.shellEdits.running, u.feedback.Text)
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
	workspace, ctx, store := u.session.cwd, u.session.waitContext, u.proxy.replayStore
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
		if _, found, err := store.lookup(ctx, workspace, legacycall.ExecCallID(edit.call, false)); found || err != nil {
			t.Fatalf("start allocated completed evidence: %v, %v", found, err)
		}
		if output, exit := mutateTestChanges(t, ctx, store, workspace, mutation, id); exit != 0 {
			t.Fatalf("mutation failed: %s", output)
		}
		completeShellEditTestCommand(t, u, item, 0)
		if u.feedback.Alert {
			t.Fatal(u.feedback.Text)
		}
		// Duplicate notifications and resumed host history cannot allocate another capture.
		completeShellEditTestCommand(t, u, item, 0)
		fresh, err := openMekugiReplayStore(store.directory)
		if err != nil {
			t.Fatal(err)
		}
		history, found, err := fresh.lookup(ctx, workspace, legacycall.ExecCallID(edit.call, false))
		if err != nil || !found || history.ChangeID == "" || len(history.ReviewFiles) != 1 || len(history.HostResults) != 1 || history.HostResults[0].CallID != item || history.HostResults[0].Tool != "userShell" {
			t.Fatalf("durable shell edit: %+v, %v, %v", history, found, err)
		}
		text, err := fresh.changes.Read(ctx, fresh.scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{history.ChangeID}, View: "history"})
		if err != nil || !strings.Contains(text, "mchanges "+mutation+" "+id) {
			t.Fatalf("shell change history: %q, %v", text, err)
		}
		data, err := fresh.liveDiffSnapshot(ctx, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {u.Thread: true, "stock-thread": true}}})
		if err != nil || data.Len() != index+2 {
			t.Fatalf("Changes history: %+v, %v", data, err)
		}
		u.shell.saved.data = data
		publishFixtureReview(t, u)
		savedPane(u.shell).Update(data.Publication, "")
		counts := u.shell.saved.data.Publication.Counts.Net
		if counts == nil || index == 0 && (counts.Added != 0 || counts.Removed != 0) || index == 1 && (counts.Added != 1 || counts.Removed != 1) {
			t.Fatalf("saved Diff after %s: files=%+v counts=%+v", mutation, data.Publication.Files, counts)
		}
	}
}

func TestAppServerShellEditsFailedWriterAndNoEffect(t *testing.T) {
	u := newShellEditTestUI(t)
	appServerTestTurn(t, u, "model")
	for _, command := range []string{"printf 'saved\\n' > \"$SHELL_EDIT_TARGET\"; exit 7", "rm -f missing.txt"} {
		edit := startShellEditTestCommand(t, u, command, command)
		cmd := exec.Command(u.shellEdits.shell, "-c", command)
		cmd.Dir = u.session.cwd
		cmd.Env = append(mchangesSliceInvocation(u.session.cwd, u.Thread).environment, "SHELL_EDIT_TARGET=saved.txt")
		err := cmd.Run()
		exit := 0
		if err != nil {
			exit = err.(*exec.ExitError).ExitCode()
		}
		// Presentation may discard an old thread's completion after a switch.
		u.retiredThreads = map[string]string{u.Thread: u.Thread}
		completeShellEditTestCommand(t, u, command, exit)
		u.retiredThreads = nil
		if u.Turn != "model" {
			t.Fatal("shell completion ended the model turn")
		}
		if u.feedback.Alert {
			t.Fatal(u.feedback.Text)
		}
		history, found, err := u.proxy.replayStore.lookup(u.session.waitContext, u.session.cwd, legacycall.ExecCallID(edit.call, false))
		if err != nil || !found || history.ExecOutcome.Exit == nil || *history.ExecOutcome.Exit != exit {
			t.Fatalf("shell outcome: %+v, %v, %v", history, found, err)
		}
		if exit == 7 && history.ChangeID == "" || exit == 0 && history.ChangeID != "" {
			t.Fatalf("saved edits depend on command status: %+v", history)
		}
	}
	// A rejected request closes its window without capturing external edits.
	u.Text = "!printf rejected > saved.txt"
	if err := u.submitShell(); err != nil {
		t.Fatal(err)
	}
	u.shellResponse(&appserver.Error{Message: "rejected"})
	if u.shellEdits.pending != nil {
		t.Fatal("rejected shell retained its observation")
	}
}
