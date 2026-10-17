package router

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

func TestShellApprovalLifecycleIndependentOfReports(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t, 5*time.Second)
	_, link := shellapproval.Paths(filepath.Join(shell.root, "bin"))
	socket, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	shell.hub.Close()

	start := func() <-chan execTrackRun {
		results := make(chan execTrackRun, 1)
		go func() { results <- runExecTrackShell(t, shell.env, "git push origin main") }()
		return results
	}
	awaitRequest := func(results <-chan execTrackRun) *sessioncontrol.ShellApprovalRequest {
		t.Helper()
		select {
		case request := <-shell.guard.Requests():
			if !slices.Equal(request.Argv, []string{"git", "push", "origin", "main"}) {
				t.Fatalf("approval argv = %q", request.Argv)
			}
			return request
		case run := <-results:
			t.Fatalf("command finished without approval: %+v", run)
		case <-time.After(5 * time.Second):
			t.Fatal("approval missing after report hub closed")
		}
		return nil
	}
	awaitRun := func(results <-chan execTrackRun) execTrackRun {
		t.Helper()
		select {
		case run := <-results:
			return run
		case <-time.After(5 * time.Second):
			t.Fatal("guarded command did not finish")
		}
		return execTrackRun{}
	}

	results := start()
	request := awaitRequest(results)
	if got := shell.invoked(t); len(got) != 0 {
		t.Fatalf("command ran before approval: %q", got)
	}
	request.Reply <- shellapproval.Reply{OK: true}
	if run := awaitRun(results); run.code != 0 || run.stderr != "" {
		t.Fatalf("approved command: %+v", run)
	}
	if got := shell.invoked(t); !slices.Equal(got, []string{"git_push_origin_main"}) {
		t.Fatalf("approved invocations = %q", got)
	}

	results = start()
	request = awaitRequest(results)
	shell.guard.Close()
	select {
	case <-request.Done:
		if request.Outcome != "withdrawn" {
			t.Fatalf("closed approval outcome = %q", request.Outcome)
		}
	default:
		t.Fatal("approval owner returned before withdrawing its request")
	}
	if run := awaitRun(results); run.code != 1 {
		t.Fatalf("withdrawn command: %+v", run)
	}
	if got := shell.invoked(t); len(got) != 0 {
		t.Fatalf("withdrawn command ran: %q", got)
	}
	for _, path := range []string{link, filepath.Dir(socket)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("approval resource retained: %s: %v", path, err)
		}
	}
}
