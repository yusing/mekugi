package router

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/appserver"
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
	u.draft = "!" + command
	if err := u.submitShell(); err != nil {
		t.Fatal(err)
	}
	edit := u.shellEdits.pending
	if u.turn == "" {
		appServerTestTurn(t, u, "shell-turn")
	}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": u.thread, "turnId": u.turn,
		"item": appServerItem{ID: item, Type: "commandExecution", Source: "userShell", Command: command, Status: "inProgress"}})
	u.shellResponse(nil)
	if edit != nil && u.shellEdits.running[[2]string{u.thread, item}] != edit {
		t.Fatalf("shell start was not matched: pending=%+v running=%+v notice=%s", u.shellEdits.pending, u.shellEdits.running, u.notice)
	}
	return edit
}

func completeShellEditTestCommand(t *testing.T, u *appServerUI, item string, exit int) {
	t.Helper()
	status := "completed"
	if exit != 0 {
		status = "failed"
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": u.thread, "turnId": u.turn,
		"item": appServerItem{ID: item, Type: "commandExecution", Source: "userShell", Status: status, ExitCode: &exit}})
	if u.shellCommand.standalone() {
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
	id := putTestChange(t, ctx, store, workspace, "original", mekugi.RenderReviewFile(path, path, "before\n", "after\n"))
	for index, mutation := range []string{"revert", "apply"} {
		item := fmt.Sprintf("shell-%d", index)
		edit := startShellEditTestCommand(t, u, "mchanges "+mutation+" "+id, item)
		if edit == nil {
			t.Fatal("shell mutation has no pre-submission observation")
		}
		if _, found, err := store.lookup(ctx, workspace, execDerivedCallID(edit.call, false)); found || err != nil {
			t.Fatalf("start allocated completed evidence: %v, %v", found, err)
		}
		if output, exit := mutateTestChanges(t, ctx, store, workspace, mutation, id); exit != 0 {
			t.Fatalf("mutation failed: %s", output)
		}
		completeShellEditTestCommand(t, u, item, 0)
		if u.noticeAlert {
			t.Fatal(u.notice)
		}
		// Duplicate notifications and resumed host history cannot allocate another capture.
		completeShellEditTestCommand(t, u, item, 0)
		fresh, err := openMekugiReplayStore(store.directory)
		if err != nil {
			t.Fatal(err)
		}
		history, found, err := fresh.lookup(ctx, workspace, execDerivedCallID(edit.call, false))
		if err != nil || !found || history.ChangeID == "" || len(history.ReviewFiles) != 1 || len(history.HostResults) != 1 || history.HostResults[0].CallID != item || history.HostResults[0].Tool != "userShell" {
			t.Fatalf("durable shell edit: %+v, %v, %v", history, found, err)
		}
		text, err := fresh.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{history.ChangeID}, view: "history"})
		if err != nil || !strings.Contains(text, "mchanges "+mutation+" "+id) {
			t.Fatalf("shell change history: %q, %v", text, err)
		}
		data, err := fresh.liveDiffSnapshot(ctx, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {u.thread: true, "stock-thread": true}}})
		if err != nil || len(data.order) != index+2 {
			t.Fatalf("Changes history: %+v, %v", data, err)
		}
		u.shell.diff.data = data
		u.shell.diff.view.Files = data.files()
		counts := liveDiffNetCounts(&u.shell.diff.view)
		if counts == nil || index == 0 && (counts.Added != 0 || counts.Removed != 0) || index == 1 && (counts.Added != 1 || counts.Removed != 1) {
			t.Fatalf("saved Diff after %s: files=%+v counts=%+v", mutation, data.files(), counts)
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
		cmd.Env = append(mchangesSliceInvocation(u.session.cwd, u.thread).environment, "SHELL_EDIT_TARGET=saved.txt")
		err := cmd.Run()
		exit := 0
		if err != nil {
			exit = err.(*exec.ExitError).ExitCode()
		}
		// Presentation may discard an old thread's completion after a switch.
		u.retiredThreads = map[string]string{u.thread: u.thread}
		completeShellEditTestCommand(t, u, command, exit)
		u.retiredThreads = nil
		if u.turn != "model" {
			t.Fatal("shell completion ended the model turn")
		}
		if u.noticeAlert {
			t.Fatal(u.notice)
		}
		history, found, err := u.proxy.replayStore.lookup(u.session.waitContext, u.session.cwd, execDerivedCallID(edit.call, false))
		if err != nil || !found || history.ExecOutcome.Exit == nil || *history.ExecOutcome.Exit != exit {
			t.Fatalf("shell outcome: %+v, %v, %v", history, found, err)
		}
		if exit == 7 && history.ChangeID == "" || exit == 0 && history.ChangeID != "" {
			t.Fatalf("saved edits depend on command status: %+v", history)
		}
	}
	// A rejected request closes its window without capturing external edits.
	u.draft = "!printf rejected > saved.txt"
	if err := u.submitShell(); err != nil {
		t.Fatal(err)
	}
	u.shellResponse(&appserver.Error{Message: "rejected"})
	if u.shellEdits.pending != nil {
		t.Fatal("rejected shell retained its observation")
	}
}
