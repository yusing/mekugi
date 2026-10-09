package router

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/vcsguard"
)

type vcsGuardShell struct {
	*execTrackShell
	log  string // One line per real git or gh invocation.
	real string // Directory of the fake git and gh behind the guard.
}

// newVCSGuardShell puts the guard ahead of a fake git and gh that log their
// arguments, as the wrapper puts it ahead of the frontend directory.
func newVCSGuardShell(t *testing.T, timeout ...time.Duration) *vcsGuardShell {
	t.Helper()
	shell := newExecTrackShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	if len(timeout) > 0 {
		shell.hub.approvalTimeout = timeout[0]
	}
	guard, channel := vcsguard.Paths(filepath.Join(shell.root, "bin"))
	if err := shell.hub.listenVCSGuard(t.Context(), channel); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(guard, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tool := range slices.Concat(vcsguard.Tools, vcsguard.Shells) {
		if err := os.Symlink(helper, filepath.Join(guard, tool)); err != nil {
			t.Fatal(err)
		}
	}
	real := t.TempDir()
	log := filepath.Join(t.TempDir(), "git.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"${0##*/} $*\" >> " + quoteShellWord(log) + "\n"
	for _, tool := range []string{"git", "gh"} {
		if err := os.WriteFile(filepath.Join(real, tool), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Login Bash resets PATH, so the startup file puts the guard first and
	// defines the absolute-path functions, as the wrapper's does.
	startup := filepath.Join(shell.root, "bash-env")
	hook, err := os.ReadFile(startup)
	if err != nil {
		t.Fatal(err)
	}
	front := "PATH=" + quoteShellWord(guard+string(os.PathListSeparator)+real) + ":\"$PATH\"; export PATH\n"
	if err := os.WriteFile(startup, append([]byte(front+vcsguard.Functions(guard, nil)), hook...), 0o600); err != nil {
		t.Fatal(err)
	}
	// Plain sh has no startup hook. Its nested commands must also find only
	// this fixture's guard and fake tools before the isolated host PATH.
	shell.env[0] = "PATH=" + guard + string(os.PathListSeparator) + real + string(os.PathListSeparator) + execTrackPath()
	return &vcsGuardShell{execTrackShell: shell, log: log, real: real}
}

func (s *vcsGuardShell) invoked(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Remove(s.log); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(data)), " ", "_"))
}

// answer replies to each guarded request in turn and records its argv.
func (s *vcsGuardShell) answer(t *testing.T, ok bool) <-chan []string {
	t.Helper()
	asked := make(chan []string, 8)
	go func() {
		for {
			select {
			case request := <-s.hub.approvals:
				asked <- request.argv
				request.reply <- vcsguard.Reply{OK: ok, Reason: "denied in test"}
			case <-t.Context().Done():
				return
			}
		}
	}()
	return asked
}

// A denied push fails only its own command. Independent commands in a list
// still run; an && chain stops at the failure as Bash decides.
func TestVCSGuardDenialFailsOnlyThePush(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	asked := shell.answer(t, false)
	for i, tc := range []struct {
		script   string
		code     int
		segments []commandSegment
		invoked  []string
	}{
		{
			script: "git add -A; git commit -m x; git push origin main; git log",
			code:   0,
			segments: []commandSegment{
				{text: "git add -A"},
				{text: "git commit -m x"},
				{text: "git push origin main", exit: 1, tail: []string{"mekugi: remote write denied: denied in test"}},
				{text: "git log"},
			},
			invoked: []string{"git_add_-A", "git_commit_-m_x", "git_log"},
		},
		{
			script: "git add -A && git commit -m x && git push origin main && git log",
			code:   1,
			segments: []commandSegment{
				{text: "git add -A"},
				{text: "git commit -m x"},
				{text: "git push origin main", exit: 1, tail: []string{"mekugi: remote write denied: denied in test"}},
				{text: "git log", skipped: true},
			},
			invoked: []string{"git_add_-A", "git_commit_-m_x"},
		},
	} {
		key := [3]string{"thread", "turn", string(rune('a' + i))}
		shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(tc.script))
		run := runExecTrackShell(t, shell.env, tc.script)
		if run.code != tc.code || run.stderr != "mekugi: remote write denied: denied in test\n" {
			t.Fatalf("%q: run = %+v", tc.script, run)
		}
		view := shell.awaitView(t, key)
		if !slices.EqualFunc(view.segments, tc.segments, func(a, b commandSegment) bool {
			return a.text == b.text && a.exit == b.exit && a.skipped == b.skipped && !a.running && slices.Equal(a.tail, b.tail)
		}) {
			t.Fatalf("%q: segments = %+v, want %+v", tc.script, view.segments, tc.segments)
		}
		if got := shell.invoked(t); !slices.Equal(got, tc.invoked) {
			t.Fatalf("%q: real tools ran %q, want %q", tc.script, got, tc.invoked)
		}
		select {
		case argv := <-asked:
			if !slices.Equal(argv, []string{"git", "push", "origin", "main"}) {
				t.Fatalf("%q: asked about %q", tc.script, argv)
			}
		default:
			t.Fatalf("%q: push was not guarded", tc.script)
		}
		shell.hub.finish(key)
	}
}

func TestVCSGuardApprovedWriteRunsUnchanged(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	asked := shell.answer(t, true)
	run := runExecTrackShell(t, shell.env, "git push origin main && git tag -l; git status")
	if run.code != 0 || run.stderr != "" {
		t.Fatalf("run = %+v", run)
	}
	if got := shell.invoked(t); !slices.Equal(got, []string{"git_push_origin_main", "git_tag_-l", "git_status"}) {
		t.Fatalf("real tools ran %q", got)
	}
	if argv := <-asked; !slices.Equal(argv, []string{"git", "push", "origin", "main"}) || len(asked) != 0 {
		t.Fatalf("asked about %q and %d more", argv, len(asked))
	}
}

// Nobody answering denies the write once the timeout passes, whether or not
// the UI has taken the request.
func TestVCSGuardDeniesWithoutAnswer(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t, 200*time.Millisecond)
	run := runExecTrackShell(t, shell.env, "git push; echo $?")
	if run.code != 0 || run.stdout != "1\n" || run.stderr != "mekugi: remote write denied: no answer within 5 minutes\n" {
		t.Fatalf("unreceived run = %+v", run)
	}
	taken := make(chan *vcsApproval, 1)
	go func() { taken <- <-shell.hub.approvals }()
	run = runExecTrackShell(t, shell.env, "gh pr merge 1; echo $?")
	if run.stdout != "1\n" || !strings.Contains(run.stderr, "no answer within 5 minutes") {
		t.Fatalf("received run = %+v", run)
	}
	request := <-taken
	<-request.done
	if request.outcome != "timed out" {
		t.Fatalf("outcome = %q", request.outcome)
	}
	if got := shell.invoked(t); len(got) != 0 {
		t.Fatalf("real tools ran %q", got)
	}
}

// A nested Mekugi from another install leaves its own guard in PATH; each
// guard must pass over the other rather than exec it forever.
func TestVCSGuardSkipsAnotherSessionsGuard(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "mekugi-exec")
	if err := os.WriteFile(other, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(t.TempDir(), vcsguard.Directory)
	if err := os.Mkdir(guard, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(guard, "git")); err != nil {
		t.Fatal(err)
	}
	run := runExecTrackShell(t, shell.env, "PATH="+quoteShellWord(guard)+":$PATH timeout 5 git status")
	if run.code != 0 || run.stderr != "" {
		t.Fatalf("run = %+v", run)
	}
	if got := shell.invoked(t); !slices.Equal(got, []string{"git_status"}) {
		t.Fatalf("real tools ran %q", got)
	}
}

// A command that stops while it waits closes its approval socket, withdrawing the
// request; without the router, a write fails at once.
func TestVCSGuardWithdrawalAndUnreachableRouter(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	taken := make(chan *vcsApproval, 1)
	go func() { taken <- <-shell.hub.approvals }()
	run := runExecTrackShell(t, shell.env, "timeout 1 git push; echo $?")
	if run.stdout != "124\n" {
		t.Fatalf("interrupted run = %+v", run)
	}
	request := <-taken
	select {
	case <-request.done:
	case <-time.After(5 * time.Second):
		t.Fatal("request was not withdrawn")
	}
	if request.outcome != "withdrawn" {
		t.Fatalf("outcome = %q", request.outcome)
	}
	shell.hub.close()
	run = runExecTrackShell(t, shell.env, "git push; echo $?; git status")
	if run.stdout != "1\n" || run.stderr != "mekugi: remote write denied: Mekugi approval is unavailable\n" {
		t.Fatalf("unreachable run = %+v", run)
	}
	if got := shell.invoked(t); !slices.Equal(got, []string{"git_status"}) {
		t.Fatalf("real tools ran %q", got)
	}
}

// A command that names a guarded tool by absolute path reaches the guard
// through the startup file's function for that path, with Bash's per-command
// semantics; the real tool sees the path as its argv[0].
func TestVCSGuardAbsolutePathBash(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	asked := shell.answer(t, false)
	git := filepath.Join(shell.real, "git")
	script := strings.ReplaceAll("GIT add -A; GIT push origin main; echo $?; (GIT push --tags) && echo no; d=DIR; $d/git status", "GIT", quoteShellWord(git))
	script = strings.ReplaceAll(script, "DIR", quoteShellWord(shell.real))
	run := runExecTrackShell(t, shell.env, script)
	if run.code != 0 || run.stdout != "1\n" || strings.Count(run.stderr, "mekugi: remote write denied: denied in test\n") != 2 {
		t.Fatalf("run = %+v", run)
	}
	if got := shell.invoked(t); !slices.Equal(got, []string{"git_add_-A", "git_status"}) {
		t.Fatalf("real tools ran %q", got)
	}
	for _, want := range [][]string{{"git", "push", "origin", "main"}, {"git", "push", "--tags"}} {
		if argv := <-asked; !slices.Equal(argv, want) {
			t.Fatalf("asked about %q, want %q", argv, want)
		}
	}
	run = runExecTrackShell(t, shell.env, "printf '#!/bin/sh\\necho \"$0\"\\n' > "+quoteShellWord(git)+"; "+quoteShellWord(git)+" status; "+quoteShellWord(git)+" status | cat")
	if want := git + "\n" + git + "\n"; run.stdout != want || run.stderr != "" {
		t.Fatalf("argv[0] run = %+v, want %q", run, want)
	}
	if len(asked) != 0 {
		t.Fatalf("non-writes asked %d times", len(asked))
	}
}

// Separate connections isolate large simultaneous requests and their answers.
func TestVCSGuardConcurrentApprovalRequests(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	const commands = 4
	results := make(chan execTrackRun, commands)
	for i := range commands {
		go func() {
			body := fmt.Sprintf("request-%d-", i) + strings.Repeat("ünïcode body ", 1000)
			results <- runExecTrackShell(t, shell.env, "gh pr create --body "+quoteShellWord(body))
		}()
	}
	seen := make(map[string]bool)
	for range commands {
		select {
		case request := <-shell.hub.approvals:
			if len(request.argv) != 5 || request.argv[0] != "gh" || request.argv[3] != "--body" {
				t.Fatalf("request = %q", request.argv)
			}
			body := request.argv[4]
			prefix, content, ok := strings.Cut(body, "-ünïcode")
			if !ok || seen[prefix] || content != " body "+strings.Repeat("ünïcode body ", 999) {
				t.Fatalf("request body was lost or mixed: prefix=%q, bytes=%d", prefix, len(body))
			}
			seen[prefix] = true
			request.reply <- vcsguard.Reply{Reason: "concurrent denial"}
		case <-time.After(5 * time.Second):
			t.Fatal("approval request missing")
		}
	}
	for range commands {
		run := <-results
		if run.code != 1 || run.stderr != "mekugi: remote write denied: concurrent denial\n" {
			t.Fatalf("run = %+v", run)
		}
	}
	if got := shell.invoked(t); len(got) != 0 {
		t.Fatalf("denied commands ran: %q", got)
	}
}

func TestVCSGuardClosesPendingApproval(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	_, link := vcsguard.Paths(filepath.Join(shell.root, "bin"))
	socket, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan execTrackRun, 1)
	go func() { results <- runExecTrackShell(t, shell.env, "git push") }()
	var request *vcsApproval
	select {
	case request = <-shell.hub.approvals:
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not arrive")
	}
	proxy := newManagedMekugiProxy(t)
	proxy.execTrack = shell.hub
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case run := <-results:
		if run.code != 1 || !strings.Contains(run.stderr, "remote write denied") {
			t.Fatalf("closed router = %+v", run)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not finish after router close")
	}
	<-request.done
	if request.outcome != "withdrawn" {
		t.Fatalf("outcome = %q", request.outcome)
	}
	for _, path := range []string{link, filepath.Dir(socket)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("guard resource retained: %s: %v", path, err)
		}
	}
}
