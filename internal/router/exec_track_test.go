package router

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/execsegment"
	"golang.org/x/sys/unix"
)

// Owned by TestMain, like the shared registry fixtures. Build once per test
// process, but do not leave one executable behind after every validation run.
var execTrackHelperDirectory string

var execTrackHelper = sync.OnceValues(func() (string, error) {
	directory, err := os.MkdirTemp("", "mekugi-exec-test-")
	if err != nil {
		return "", err
	}
	execTrackHelperDirectory = directory
	helper := filepath.Join(directory, "mekugi-exec")
	output, err := exec.Command("go", "build", "-o", helper, "github.com/yusing/mekugi/cmd/mekugi-exec").CombinedOutput()
	if err != nil {
		return "", errors.Join(err, errors.New(string(output)))
	}
	return helper, nil
})

type execTrackShell struct {
	hub  *execTrackHub
	env  []string
	home string
	root string // Holds the session's bin, request FIFO and per-command files.
}

func newExecTrackShell(t *testing.T) *execTrackShell {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	socket, directory := ExecTrackPaths(filepath.Join(root, "bin"))
	hub, err := listenExecTrack(t.Context(), socket, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.close)
	tracker := filepath.Join(root, "exec-track.bash")
	if err := os.WriteFile(tracker, []byte(execsegment.Tracker(helper, socket, directory)), 0o600); err != nil {
		t.Fatal(err)
	}
	startup := filepath.Join(root, "bash-env")
	if err := os.WriteFile(startup, []byte(execsegment.Hook(tracker)), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_THREAD_ID=thread", "BASH_ENV=" + startup}
	return &execTrackShell{hub: hub, env: env, home: home, root: root}
}

type execTrackRun struct {
	stdout, stderr string
	code           int
}

func runExecTrackShell(t *testing.T, env []string, script string) execTrackRun {
	t.Helper()
	return runShell(t, env, "bash", "-lc", script)
}

func runShell(t *testing.T, env []string, shell string, args ...string) execTrackRun {
	t.Helper()
	cmd := exec.Command(shell, args...)
	cmd.Env, cmd.Dir = env, t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return execTrackRun{stdout.String(), stderr.String(), code}
}

// awaitView waits for the report to end, as the UI does after completion.
func (s *execTrackShell) awaitView(t *testing.T, key [3]string) execTrackView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		view, _ := s.hub.view(key, true, func(source string) string { return source }, nil)
		if view.ended || time.Now().After(deadline) {
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExecTrackReportsEachSegmentWithoutChangingTheCommand(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	for i, tc := range []struct {
		script   string
		segments []commandSegment
	}{
		{
			script: "echo one; echo two >&2 && false || echo three",
			segments: []commandSegment{
				{text: "echo one", tail: []string{"one"}},
				{text: "echo two >&2", tail: []string{"two"}},
				{text: "false", exit: 1},
				{text: "echo three", tail: []string{"three"}},
			},
		},
		{
			script: "echo a && false && echo never",
			segments: []commandSegment{
				{text: "echo a", tail: []string{"a"}},
				{text: "false", exit: 1},
				{text: "echo never", skipped: true},
			},
		},
		{
			script: "false; echo \"status $?\"; cd / && pwd",
			segments: []commandSegment{
				{text: "false", exit: 1},
				{text: "echo \"status $?\"", tail: []string{"status 1"}},
				{text: "cd /"},
				{text: "pwd", tail: []string{"/"}},
			},
		},
		{
			script: "echo before; exit 3; echo after",
			segments: []commandSegment{
				{text: "echo before", tail: []string{"before"}},
				{text: "exit 3", exit: 3},
				{text: "echo after", skipped: true},
			},
		},
		{
			script: "set -e\necho x\nfalse\necho y",
			segments: []commandSegment{
				{text: "set -e"},
				{text: "echo x", tail: []string{"x"}},
				{text: "false", exit: 1},
				{text: "echo y", skipped: true},
			},
		},
		{
			script: "cat <<'EOF' && printf 'tail\\n'\nheredoc body\nEOF",
			segments: []commandSegment{
				{text: "cat <<'EOF'\nheredoc body\nEOF", tail: []string{"heredoc body"}},
				{text: "printf 'tail\\n'", tail: []string{"tail"}},
			},
		},
	} {
		plain := runExecTrackShell(t, slices.DeleteFunc(slices.Clone(shell.env), func(entry string) bool { return entry[:min(len(entry), 9)] == "BASH_ENV=" }), tc.script)
		key := [3]string{"thread", "turn", string(rune('a' + i))}
		shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(tc.script))
		tracked := runExecTrackShell(t, shell.env, tc.script)
		if tracked != plain {
			t.Fatalf("%q: tracked run = %+v, plain run = %+v", tc.script, tracked, plain)
		}
		view := shell.awaitView(t, key)
		if !view.complete || view.code != plain.code || !view.output {
			t.Fatalf("%q: view = %+v, want a complete report with exit %d", tc.script, view, plain.code)
		}
		if !slices.EqualFunc(view.segments, tc.segments, func(a, b commandSegment) bool {
			return a.text == b.text && a.exit == b.exit && a.skipped == b.skipped && !a.running && slices.Equal(a.tail, b.tail)
		}) {
			t.Fatalf("%q: segments = %+v, want %+v", tc.script, view.segments, tc.segments)
		}
		shell.hub.finish(key)
	}
}

// Tracked scripts keep Bash's own error handling: its messages, fatal
// expansion errors that end the script, and set -e around recovered and
// negated failures. Output printed before a fatal error shows that the
// script ran once.
func TestExecTrackMatchesPlainBash(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	for i, script := range []string{
		"nosuchcmd; echo after",
		"echo one\ncd /nope && echo never\nnosuch",
		"s=1 r=2; readonly s r; echo \"$s$r\"; echo done",
		"set -e; false || echo recovered; echo end",
		"set -e; ! true; echo after $?",
		"set -e; { false && true; }; echo after",
		"set -e; echo x; false; echo never",
		"set -u; echo once; echo ${nope}; echo never",
		"echo once; echo ${nope:?unset}; echo never",
		"echo once; echo $((1/0)); echo never",
		"readonly r=1; echo once; r=2; echo never",
		"false; exit",
		"ls /proc/self/fd; echo done",
	} {
		plain := runExecTrackShell(t, slices.DeleteFunc(slices.Clone(shell.env), func(entry string) bool { return entry[:min(len(entry), 9)] == "BASH_ENV=" }), script)
		key := [3]string{"thread", "turn", string(rune('a' + i))}
		shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(script))
		tracked := runExecTrackShell(t, shell.env, script)
		if tracked != plain {
			t.Errorf("%q: tracked run = %+v, plain run = %+v", script, tracked, plain)
		}
		if view := shell.awaitView(t, key); !view.complete || view.code != plain.code {
			t.Errorf("%q: view = %+v, want a complete report with exit %d", script, view, plain.code)
		}
		shell.hub.finish(key)
	}
}

func TestExecTrackRunsUnmatchedScriptsUntracked(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	script := "echo one; echo two"
	tracked := runExecTrackShell(t, shell.env, script)
	if want := (execTrackRun{stdout: "one\ntwo\n"}); tracked != want {
		t.Fatalf("run = %+v, want %+v", tracked, want)
	}
	if len(shell.hub.tracks) != 0 {
		t.Fatalf("tracks = %v, want none", shell.hub.tracks)
	}
}

func TestExecTrackMatchesDelayedHostStart(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	script := "echo first; false && echo never; echo last"
	key := [3]string{"thread", "turn", "delayed"}
	cmd := exec.CommandContext(t.Context(), "bash", "-lc", script)
	cmd.Env, cmd.Dir = shell.env, t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Codex emits item/started after its early-exit grace, not at spawn.
	time.Sleep(250 * time.Millisecond)
	shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(script))
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "first\nlast\n" || stderr.Len() != 0 {
		t.Fatalf("host output changed: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	view := shell.awaitView(t, key)
	if !view.complete || !view.output || view.code != 0 || len(view.segments) != 4 {
		t.Fatalf("missing delayed command boundaries: %+v", view)
	}
	if strings.Join(view.segments[0].tail, "\n") != "first" || view.segments[1].exit != 1 || !view.segments[2].skipped || strings.Join(view.segments[3].tail, "\n") != "last" {
		t.Fatalf("incorrect segment outputs/states: %+v", view.segments)
	}
}

func TestExecTrackLeavesNestedShellsAlone(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	key := [3]string{"thread", "turn", "item"}
	script := "echo outer; bash -c 'echo inner; echo nested'"
	shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(script))
	// A nested shell's script matches no item; without the guard it would
	// wait for one before running.
	shell.hub.start([3]string{"thread", "turn", "nested"}, "/usr/bin/bash -lc "+quoteShellWord("echo inner; echo nested"))
	tracked := runExecTrackShell(t, shell.env, script)
	if want := (execTrackRun{stdout: "outer\ninner\nnested\n"}); tracked != want {
		t.Fatalf("run = %+v, want %+v", tracked, want)
	}
	view := shell.awaitView(t, key)
	if len(view.segments) != 2 || !slices.Equal(view.segments[1].tail, []string{"inner", "nested"}) {
		t.Fatalf("segments = %+v", view.segments)
	}
	if shell.hub.tracking([3]string{"thread", "turn", "nested"}) {
		t.Fatal("nested shell was tracked")
	}
}

func quoteShellWord(value string) string {
	return "'" + string(bytes.ReplaceAll([]byte(value), []byte("'"), []byte(`'\''`))) + "'"
}

// execTrackReport plays a helper's report for one command.
type execTrackReport struct {
	t    *testing.T
	conn *os.File
}

func dialExecTrackReport(t *testing.T, hub *execTrackHub, script string) *execTrackReport {
	t.Helper()
	conn, answer, _, err := execsegment.OpenReport(hub.requests.Name(), hub.directory, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); answer.Close() })
	segments, ok := execsegment.Split(script)
	if !ok {
		t.Fatalf("script %q is not tracked", script)
	}
	var sources []string
	for _, segment := range segments {
		sources = append(sources, segment.Source)
	}
	r := &execTrackReport{t: t, conn: conn}
	r.send(execsegment.Message{Type: execsegment.Hello, Version: execsegment.Protocol, Thread: "main", Script: script, Segments: sources})
	deadline := time.Now().Add(time.Second)
	for {
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(answer.Fd()), Events: unix.POLLIN}}, max(0, int(time.Until(deadline).Milliseconds())))
		if errors.Is(err, unix.EINTR) && time.Now().Before(deadline) {
			continue
		}
		if err != nil || n == 0 {
			t.Fatalf("reply not ready: %d, %v", n, err)
		}
		break
	}
	line, err := bufio.NewReader(answer).ReadBytes('\n')
	if err != nil || string(line) != "{\"ok\":true}\n" {
		t.Fatalf("reply = %q, %v", line, err)
	}
	return r
}

func (r *execTrackReport) send(messages ...execsegment.Message) {
	r.t.Helper()
	for _, message := range messages {
		line, err := json.Marshal(message)
		if err != nil {
			r.t.Fatal(err)
		}
		if _, err := r.conn.Write(append(line, '\n')); err != nil {
			r.t.Fatal(err)
		}
	}
}

// awaitMain flushes frames until Main shows want, as the UI's ticker would.
func awaitMain(t *testing.T, u *appServerUI, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		u.flushStreamOutput()
		u.startCommitReads()
		for drained := false; !drained; {
			select {
			case key := <-u.commitReads:
				u.commitRead(key)
			default:
				drained = true
			}
		}
		u.view.pace(time.Now())
		main := ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n"))
		if strings.Contains(main, want) {
			return main
		}
		if time.Now().After(deadline) {
			t.Fatalf("Main lacks %q:\n%s", want, main)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAppServerTrackedCommandShowsEachSegment(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	script := "pwd && ls && cat a b"
	command := "/usr/bin/bash -lc " + quoteShellWord(script)
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": command, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(
		execsegment.Message{Type: execsegment.Begin, Index: 0},
		execsegment.Message{Type: execsegment.Output, Index: 0, Data: "/w\n"},
		execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)},
		execsegment.Message{Type: execsegment.Begin, Index: 1},
		execsegment.Message{Type: execsegment.Output, Index: 1, Data: "a\n"},
	)
	awaitMain(t, u, "┆ a")

	report.send(
		execsegment.Message{Type: execsegment.Output, Index: 1, Data: "b\n"},
		execsegment.Message{Type: execsegment.End, Index: 1, Code: new(0)},
		execsegment.Message{Type: execsegment.Begin, Index: 2},
		execsegment.Message{Type: execsegment.Output, Index: 2, Data: "cat: b: No such file or directory\n"},
		execsegment.Message{Type: execsegment.End, Index: 2, Code: new(1)},
		execsegment.Message{Type: execsegment.Done, Code: new(1)},
	)
	report.conn.Close()
	item["status"], item["exitCode"], item["aggregatedOutput"] = "failed", 1, "/w\na\nb\ncat: b: No such file or directory\n"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	want := "├ Ran  pwd\n│      ┆ /w\n├ List .\n│      ┆ a\n│      ┆ b\n└ Read a · b · exit 1\n       ┆ cat: b: No such file or directory"
	if main := awaitMain(t, u, "exit 1"); !strings.Contains(main, want) {
		t.Fatalf("Main lacks per-segment results %q:\n%s", want, main)
	}
}

func TestAppServerTrackedCommandShowsSkippedSegments(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	script := "false && echo never"
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(
		execsegment.Message{Type: execsegment.Begin, Index: 0},
		execsegment.Message{Type: execsegment.End, Index: 0, Code: new(1)},
		execsegment.Message{Type: execsegment.Done, Code: new(1)},
	)
	report.conn.Close()
	item["status"], item["exitCode"], item["aggregatedOutput"] = "failed", 1, ""
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	if main := awaitMain(t, u, "skipped"); !strings.Contains(main, "├ Ran false · exit 1\n└ Run echo never · skipped") {
		t.Fatalf("Main lacks the skipped segment:\n%s", main)
	}
}

func TestAppServerIncompleteReportFallsBackToHostOutput(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	script := "echo a; cat b"
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	// The shell never reported its exit, as when it replaced itself.
	report.send(
		execsegment.Message{Type: execsegment.Begin, Index: 0},
		execsegment.Message{Type: execsegment.Output, Index: 0, Data: "a\n"},
		execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)},
		execsegment.Message{Type: execsegment.Begin, Index: 1},
	)
	report.conn.Close()
	item["status"], item["exitCode"], item["aggregatedOutput"] = "failed", 2, "a\nb\n"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	main := awaitMain(t, u, "exit 2")
	if strings.Count(main, "exit 2") != 1 || !strings.Contains(main, "shell batch · exit 2") {
		t.Fatalf("incomplete report attributed the batch exit to segments:\n%s", main)
	}
	if !strings.Contains(main, "┆ a\n") || !strings.Contains(main, "┆ b") || strings.Contains(main, "skipped") {
		t.Fatalf("Main does not show the host's combined result:\n%s", main)
	}
	if hub.tracking([3]string{"main", "t", "cmd"}) {
		t.Fatal("completed report was retained")
	}
}

// A helper that declines after the match, as when the router's reply came
// too late, leaves the shell to run the script untracked.
func TestAppServerEarlyEndedReportShowsHostOutputLive(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	script := "echo a; echo b"
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.conn.Close()
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "cmd", "delta": "a\nb\n"})
	awaitMain(t, u, "┆ b")
}

func newTrackedAppServerUI(t *testing.T) (*appServerUI, *execTrackHub) {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	root := t.TempDir()
	hub, err := listenExecTrack(t.Context(), filepath.Join(root, "exec.sock"), filepath.Join(root, "exec"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.close)
	u.execTrack = hub
	return u, hub
}

// Codex's default shell snapshot wraps each command in a shell that sources
// the snapshot and re-executes the command shell with the original script.
func TestExecTrackTracksTheCommandInsideCodexSnapshotWrapper(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	script := "echo one; echo two"
	key := [3]string{"thread", "turn", "snapshot"}
	shell.hub.start(key, bash+" -lc "+quoteShellWord(script))
	snapshot := filepath.Join(t.TempDir(), "snapshot.sh")
	if err := os.WriteFile(snapshot, []byte("export SNAPSHOTTED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper := "__CODEX_SNAPSHOT_OVERRIDE_SET_0=\"${CODEX_THREAD_ID+x}\"\n\nif . " + quoteShellWord(snapshot) + " >/dev/null 2>&1; then :; fi\n\nexec " + quoteShellWord(bash) + " -c " + quoteShellWord(script)
	cmd := exec.Command(bash, "-c", wrapper)
	cmd.Env = shell.env
	output, err := cmd.Output()
	if err != nil || string(output) != "one\ntwo\n" {
		t.Fatalf("output = %q, %v", output, err)
	}
	view := shell.awaitView(t, key)
	if !view.complete || len(view.segments) != 2 || !slices.Equal(view.segments[1].tail, []string{"two"}) {
		t.Fatalf("view = %+v", view)
	}
}

// A terminal cannot be relayed without changing what its programs detect,
// so terminal commands report statuses and keep their output in place.
func TestExecTrackReportsTerminalCommandStatusOnly(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	script := "test -t 1 && echo tty; false"
	key := [3]string{"thread", "turn", "tty"}
	shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(script))
	cmd := exec.Command("bash", "-lc", script)
	cmd.Env = shell.env
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Skip("pty unavailable:", err)
	}
	defer terminal.Close()
	output, _ := io.ReadAll(terminal)
	_ = cmd.Wait()
	if !strings.Contains(string(output), "tty") {
		t.Fatalf("output = %q, want the command to see its terminal", output)
	}
	view := shell.awaitView(t, key)
	if !view.complete || view.output || view.code != 1 || len(view.segments) != 3 || view.segments[2].exit != 1 {
		t.Fatalf("view = %+v", view)
	}
}

func TestAppServerTrackedMChangesShowsRichRows(t *testing.T) {
	t.Parallel()
	for _, script := range []string{"echo before && mchanges --list --max-tokens 800", "echo before; mchanges --list --max-tokens 800"} {
		t.Run(script, func(t *testing.T) {
			u, hub := newTrackedAppServerUI(t)
			item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			report := dialExecTrackReport(t, hub, script)
			index := 1
			report.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.Output, Index: 0, Data: "before\n"}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)})
			output := "amber1..amber5 +343 -658\namber6 +158 -81\namber7..amber9 +86 -30\n"
			report.send(execsegment.Message{Type: execsegment.Begin, Index: index}, execsegment.Message{Type: execsegment.Output, Index: index, Data: output}, execsegment.Message{Type: execsegment.End, Index: index, Code: new(0)}, execsegment.Message{Type: execsegment.Done, Code: new(0)})
			report.conn.Close()
			item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, output
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			got := awaitMain(t, u, "━━━━━━━━")
			if !strings.Contains(got, "amber1..amber5  +343 -658") || strings.Contains(got, "┆ amber") {
				t.Fatalf("tracked listing not rendered as change rows:\n%s", got)
			}
		})
	}
}

// Concurrent commands share only the atomic request FIFO. Each helper must
// retain its own report, output and exit status, then release its private files.
func TestExecTrackConcurrentReportsAreIsolated(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	for _, script := range []string{"echo FIRST; false", "echo SECOND; true"} {
		key := [3]string{"thread", "turn", script}
		shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(script))
	}
	for _, script := range []string{"echo FIRST; false", "echo SECOND; true"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			got := runExecTrackShell(t, shell.env, script)
			key := [3]string{"thread", "turn", script}
			view := shell.awaitView(t, key)
			if !view.complete || view.code != got.code || len(view.segments) != 2 {
				t.Fatalf("report = %+v, command = %+v", view, got)
			}
			if want := strings.TrimSpace(got.stdout); !slices.Equal(view.segments[0].tail, []string{want}) {
				t.Fatalf("segment output = %v, want %q", view.segments[0].tail, want)
			}
		})
	}
	t.Cleanup(func() {
		shell.hub.close()
		shell.hub.wg.Wait()
		entries, err := os.ReadDir(shell.hub.directory)
		if err != nil || len(entries) != 0 {
			t.Errorf("private report files remain: %v, %v", entries, err)
		}
	})
}

func TestExecTrackUnavailableRouterRunsOriginalCommand(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	shell.hub.close() // The existing request FIFO now has no reader.
	script := "echo ORIGINAL; echo STDERR >&2; false"
	got := runExecTrackShell(t, shell.env, script)
	if got.stdout != "ORIGINAL\n" || got.stderr != "STDERR\n" || got.code != 1 {
		t.Fatalf("unavailable tracker changed command: %+v", got)
	}
}

// Losing auxiliary tracking before or after startup acquisition must still
// run the original command exactly once, with its output and exit status.
func TestExecTrackCancellationDuringStartupPreservesCommand(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"before_acquisition", "before_output", "after_acquisition"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			channel, directory := ExecTrackPaths(filepath.Join(root, "bin"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hub, err := listenExecTrack(ctx, channel, directory)
			if err != nil {
				t.Fatal(err)
			}
			helper, err := execTrackHelper()
			if err != nil {
				t.Fatal(err)
			}
			marker, gate := filepath.Join(root, "ready"), filepath.Join(root, "gate")
			tracker := execsegment.Tracker(helper, channel, directory)
			pause := "touch " + quoteShellWord(marker) + "\nwhile [ ! -e " + quoteShellWord(gate) + " ]; do sleep 0.01; done\n"
			at := "case $__mekugi_m in\n"
			if phase == "before_output" {
				at = "    if ! { exec {__mekugi_y}"
			}
			if phase == "after_acquisition" {
				at = "  unset -v __mekugi_o"
			}
			if !strings.Contains(tracker, at) {
				t.Fatal("startup pause point missing")
			}
			tracker = strings.Replace(tracker, at, pause+at, 1)
			trackerPath, startup := filepath.Join(root, "tracker"), filepath.Join(root, "startup")
			for path, source := range map[string]string{trackerPath: tracker, startup: execsegment.Hook(trackerPath)} {
				if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			script := "echo FIRST; echo SECOND >&2; false"
			hub.start([3]string{"thread", "turn", "item"}, "bash -lc "+quoteShellWord(script))
			commandCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			cmd := exec.CommandContext(commandCtx, "bash", "-c", script)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "BASH_ENV=" + startup, "CODEX_THREAD_ID=thread"}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("shell did not accept tracking")
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			hub.wg.Wait() // All router-created startup paths have been removed.
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			exit, ok := errors.AsType[*exec.ExitError](err)
			if !ok || exit.ExitCode() != 1 || stdout.String() != "FIRST\n" || stderr.String() != "SECOND\n" {
				t.Fatalf("command = stdout %q, stderr %q, exit %v", stdout.String(), stderr.String(), err)
			}
		})
	}
}
