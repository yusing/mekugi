package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/conversation/prompt"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

// Exercise the installed wrapper, askpass pipe, approval socket and real input
// controller without requiring a privileged test or changing sudo credentials.
func TestSudoUnlockDialog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  []string
		shell string
		want  int
	}{
		{"approve", []string{"1\r", "1234-secret\r"}, "", 0},
		{"deny", []string{"3\r"}, "", 1},
		{"skip-password", []string{"1\r", "\x1d\r"}, "", 1},
		{"retry", []string{"2\r", "wrong\r", "1234-secret\r"}, "", 0},
		{"absolute-bash", []string{"1\r", "1234-secret\r"}, "bash", 0},
		{"clean-env-sh", []string{"1\r", "1234-secret\r"}, "sh", 0},
		{"script-absolute", []string{"1\r", "1234-secret\r"}, "script-bash", 0},
		{"script-bare", []string{"1\r", "1234-secret\r"}, "script-sh", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell, directory, real := newSudoShell(t)
			helper, err := execTrackHelper()
			if err != nil {
				t.Fatal(err)
			}
			// Only askpass reads the secret. Its output never reaches the host.
			fake := "#!/bin/sh\n[ \"$1\" = -A ] || exit 42\n[ -z \"${MEKUGI_VCS_GUARD_REAL+x}\" ] || exit 43\npassword=$(\"$SUDO_ASKPASS\" 'Password:') || exit 1\n[ \"$password\" = 1234-secret ] || exit 1\nprintf 'unlocked\\n'\n"
			if tc.name == "retry" {
				fake = strings.Replace(fake, "[ \"$password\" = 1234-secret ] || exit 1", "[ \"$password\" = 1234-secret ] || password=$(\"$SUDO_ASKPASS\" 'Password:') || exit 1\n[ \"$password\" = 1234-secret ] || exit 1", 1)
			}
			if err := os.WriteFile(filepath.Join(real, "sudo"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(filepath.Join(directory, "sudo"), "id")
			executable := filepath.Join(real, "sudo")
			startup := ""
			if name, scriptFile := strings.CutPrefix(tc.shell, "script-"); scriptFile {
				body := "sudo id"
				if name == "bash" {
					// Keep the selected target off PATH to detect a wrong lookup.
					executable = filepath.Join(t.TempDir(), "sudo")
					if err := os.WriteFile(executable, []byte(fake), 0o700); err != nil {
						t.Fatal(err)
					}
					body = quoteShellWord(executable) + " id"
					startup = filepath.Join(real, "bash-env")
					if err := os.WriteFile(startup, []byte(shellinstrumentation.Functions(directory, []string{executable}, []string{"sudo"})), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				file := filepath.Join(real, "script")
				if err := os.WriteFile(file, []byte(body+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(execTrackShellExecutable(t, name), file)
			} else if tc.shell != "" {
				script := quoteShellWord(filepath.Join(real, "sudo")) + " id"
				if tc.shell == "sh" {
					script = "env -i " + script
				}
				script, err = shellinstrumentation.RewriteCommands(script, helper, "", directory, "cmd")
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(execTrackShellExecutable(t, tc.shell), "-c", script)
			}
			cmd.Env = []string{"PATH=" + directory + ":" + real + ":" + execTrackPath(), "CODEX_THREAD_ID=main", shellinstrumentation.ItemEnvironment + "=cmd"}
			if startup != "" {
				cmd.Env = append(cmd.Env, "BASH_ENV="+startup)
			}
			result := make(chan execTrackRun, 1)
			go func() {
				out, err := cmd.CombinedOutput()
				code := 0
				if err != nil {
					code = cmd.ProcessState.ExitCode()
				}
				result <- execTrackRun{stdout: string(out), code: code}
			}()
			u, input := newAppServerTestUI()
			u.Turn = "turn"
			for i, keys := range tc.keys {
				var request *vcsApproval
				select {
				case request = <-shell.guard.requests:
					kind := "sudo"
					if i > 0 {
						kind = "sudo-password"
					}
					if request.kind != kind || request.item != "cmd" || request.executable != executable {
						t.Fatalf("wrong approval identity: %#v", request)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("sudo did not request approval or a password")
				}
				u.addGuardApproval(request)
				questionTestPaint(t, u, 80)
				for _, key := range keys {
					appServerTestKeys(t, u, string(key))
					frame := questionTestPaint(t, u, 80)
					if strings.Contains(frame, "1234-secret") {
						t.Fatal("password rendered in plaintext")
					}
				}
			}
			select {
			case got := <-result:
				if got.code != tc.want || strings.Contains(got.stdout, "1234-secret") {
					t.Fatalf("command code=%d output=%q", got.code, got.stdout)
				}
				if tc.want == 0 && got.stdout != "unlocked\n" {
					t.Fatalf("output=%q", got.stdout)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("sudo did not complete")
			}
			if input.Len() != 0 || len(u.conversation().Unsent) != 0 || len(u.InputHistory) != 0 || u.activeQuestionCall() != nil {
				t.Fatal("local sudo answer escaped into host input or history")
			}
			if strings.Contains(approvalTestTranscript(t, u), "1234-secret") {
				t.Fatal("password entered activity")
			}
		})
	}
}

func TestUISnapshotSudoDialogs(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.Painter.Theme = diffrender.DarkTheme
	u.status, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
	u.session.start("main", "/workspace")
	u.Turn = "turn"
	u.addGuardApproval(&vcsApproval{kind: "sudo", thread: "main", cwd: "/workspace", argv: []string{"sudo", "apt", "update"}, reply: make(chan shellapproval.Reply, 1), done: make(chan struct{})})
	rows, _ := u.mainFrame(80, 20, 0)
	assertNativeUISnapshot(t, "native-sudo-approval", rows)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	u.addGuardApproval(&vcsApproval{kind: "sudo-password", thread: "main", cwd: "/workspace", argv: []string{"sudo", "apt", "update"}, reply: make(chan shellapproval.Reply, 1), done: make(chan struct{})})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1234-secret")
	rows, _ = u.mainFrame(80, 20, 0)
	assertNativeUISnapshot(t, "native-sudo-password", rows)
}

func TestSudoPasswordWithdrawal(t *testing.T) {
	u, _ := newAppServerTestUI()
	request := &vcsApproval{kind: "sudo-password", thread: "main", argv: []string{"sudo", "id"}, reply: make(chan shellapproval.Reply, 1), done: make(chan struct{})}
	u.addGuardApproval(request)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "private-draft")
	request.outcome = "withdrawn"
	close(request.done)
	if !u.expireApprovals() || u.activeQuestionCall() != nil || len(u.InputHistory) != 0 || len(u.questionCalls()) != 0 {
		t.Fatal("withdrawal retained a secret draft")
	}
	if len(request.reply) != 0 {
		t.Fatal("answered a withdrawn sudo request")
	}
}

func TestSudoSecretEditorAndSessionSwitch(t *testing.T) {
	u, input := newAppServerTestUI()
	request := &vcsApproval{kind: "sudo-password", thread: "main", argv: []string{"sudo", "id"}, reply: make(chan shellapproval.Reply, 1), done: make(chan struct{})}
	u.openSudoPassword(&nativeApproval{Approval: prompt.Approval{Thread: "main", Turn: "old-turn", Subject: "sudo id"}, guard: request})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "fixture-secret")
	if _, err := u.key("\x07"); err != nil || u.Text != "fixture-secret" {
		t.Fatalf("secret editor dispatch: %v", err)
	}
	u.hideQuestions()
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	input.Reset()
	u.Thread, u.Turn = "new-thread", ""
	appServerTestKeys(t, u, "new task\r")
	if !strings.Contains(input.String(), "turn/start") {
		t.Fatal("old-session password blocked new-session input")
	}
	u.openQuestions()
	questionTestPaint(t, u, 80)
	if u.Text != "fixture-secret" {
		t.Fatal("session switch lost the password draft")
	}
	appServerTestKeys(t, u, "\r")
	if reply := <-request.reply; !reply.OK || reply.Password != "fixture-secret" {
		t.Fatal("password did not resolve after switch")
	}
	if strings.Contains(input.String(), "fixture-secret") {
		t.Fatal("password entered new-session host input")
	}
}

func newSudoShell(t *testing.T) (*approvalShell, string, string) {
	t.Helper()
	shell := newApprovalShell(t, shellapproval.Timeout)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	frontend := filepath.Join(shell.root, "bin")
	directory := shellapproval.SudoDirectory(frontend)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sudo", shellapproval.Askpass} {
		if err := os.Symlink(helper, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	return shell, directory, t.TempDir()
}

func TestSudoApprovalWithoutPassword(t *testing.T) {
	shell, directory, real := newSudoShell(t)
	// Simulate NOPASSWD/cached sudo by not invoking askpass. Explicit native
	// input modes must still ask approval and retain their original argv.
	if err := os.WriteFile(filepath.Join(real, "sudo"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	large := []string{"id"}
	for range 1000 {
		large = append(large, strings.Repeat("x", 200))
	}
	for _, tc := range []struct {
		args                    []string
		approval, askpass, deny bool
	}{
		{[]string{"id"}, true, true, false}, {[]string{"-l"}, false, false, false}, {[]string{"--list"}, false, false, false},
		{[]string{"-n", "id"}, true, false, false}, {[]string{"-S", "id"}, true, false, false}, {[]string{"-A", "id"}, true, false, false},
		{large, true, true, false},
		{[]string{"id"}, true, true, true},
	} {
		args := tc.args
		allow := !tc.deny
		cmd := exec.Command(filepath.Join(directory, "sudo"), args...)
		cmd.Env = []string{"PATH=" + directory + ":" + real + ":" + execTrackPath()}
		result := make(chan execTrackRun, 1)
		go func() {
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				code = cmd.ProcessState.ExitCode()
			}
			result <- execTrackRun{stdout: string(out), code: code}
		}()
		if tc.approval {
			select {
			case request := <-shell.guard.requests:
				if request.kind != "sudo" || !slices.Equal(request.argv, append([]string{"sudo"}, args...)) {
					t.Fatal("command approval lost exact arguments")
				}
				request.reply <- shellapproval.Reply{OK: allow, Reason: "fixture denial"}
			case <-result:
				t.Fatal("sudo ran without command approval")
			case <-time.After(10 * time.Second):
				t.Fatal("sudo did not ask for command approval")
			}
		}
		select {
		case got := <-result:
			want := args
			if tc.askpass {
				want = append([]string{"-A"}, args...)
			}
			if allow && (got.code != 0 || got.stdout != strings.Join(want, "\n")+"\n") {
				t.Fatalf("sudo output mismatch: code=%d", got.code)
			}
			if !allow && (got.code != 1 || got.stdout != "mekugi: sudo command denied: fixture denial\n") {
				t.Fatalf("denied sudo ran: %+v", got)
			}
		case request := <-shell.guard.requests:
			t.Fatalf("unexpected request kind=%s", request.kind)
		case <-time.After(10 * time.Second):
			t.Fatal("sudo did not complete")
		}
	}
}

func TestSudoInstrumentedArgv0(t *testing.T) {
	t.Parallel()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "sudo")
	source := filepath.Join(directory, "argv.go")
	// A native executable is necessary: a shebang interpreter replaces argv0.
	if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "os")
func main() { fmt.Println(os.Args[0]) }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", target, source).CombinedOutput(); err != nil {
		t.Fatalf("build native argv fixture: %v\n%s", err, output)
	}
	for _, prefix := range []string{"", "exec -a caller-selected ", "env --argv0=caller-selected "} {
		t.Run(prefix, func(t *testing.T) {
			if strings.HasPrefix(prefix, "env ") {
				help, err := exec.CommandContext(t.Context(), execTrackShellExecutable(t, "env"), "--help").Output()
				if err != nil || !strings.Contains(string(help), "--argv0") {
					t.Skip("env --argv0 is unavailable")
				}
			}
			// Listing mode avoids approval and authentication, isolating forwarding.
			script, err := shellinstrumentation.RewriteCommands(prefix+quoteShellWord(target)+" -l", helper, "", directory, "")
			if err != nil {
				t.Fatal(err)
			}
			run := runShell(t, []string{"PATH=" + execTrackPath()}, "bash", "-c", script)
			want := "caller-selected\n"
			if prefix == "" {
				want = target + "\n"
			}
			if run.code != 0 || run.stdout != want || run.stderr != "" {
				t.Fatalf("argv0 run = %+v; want %q", run, want)
			}
		})
	}
}

func TestGuardShellArgv0DoesNotSelectSudo(t *testing.T) {
	t.Parallel()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	// A dispatch regression must reach this harmless fake, never host sudo.
	if err := os.WriteFile(filepath.Join(directory, "sudo"), []byte("#!/bin/sh\nprintf WRONG_DISPATCH\nexit 47\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, argv0 := range []string{"sudo", shellapproval.Askpass} {
		t.Run(argv0, func(t *testing.T) {
			script := "exec -a " + quoteShellWord(argv0) + " " + quoteShellWord(execTrackShellExecutable(t, "sh")) + ` -c 'printf "shell:%s" "$0"; exit 23'`
			rewritten, err := shellinstrumentation.RewriteCommands(script, helper, filepath.Join(directory, "vcs-guard"), "", "")
			if err != nil {
				t.Fatal(err)
			}
			run := runShell(t, []string{"PATH=" + directory + ":" + execTrackPath()}, "bash", "-c", rewritten)
			if run.code != 23 || run.stdout != "shell:"+argv0 || run.stderr != "" {
				t.Fatalf("instrumented shell = %+v; want original shell with argv0 %q", run, argv0)
			}
		})
	}
}

func TestSudoNestedShellPreservesGuardResources(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		clean, path, allow, sudoOnly bool
	}{
		{name: "ordinary", allow: true},
		{name: "clean-environment-denial", clean: true},
		{name: "clean-environment-approval", clean: true, allow: true},
		{name: "path-trampoline", path: true},
		{name: "sudo-only-clean-denial", clean: true, sudoOnly: true},
		{name: "sudo-only-clean-approval", clean: true, allow: true, sudoOnly: true},
		{name: "sudo-only-path", path: true, sudoOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell, sudoDirectory, real := newSudoShell(t)
			helper, err := execTrackHelper()
			if err != nil {
				t.Fatal(err)
			}
			guardDirectory, _ := shellapproval.Paths(filepath.Join(shell.root, "bin"))
			shellDirectory := guardDirectory
			if tc.sudoOnly {
				guardDirectory, shellDirectory = "", sudoDirectory
			} else if err := os.Mkdir(guardDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			trampoline := filepath.Join(shellDirectory, "sh")
			if err := os.Symlink(helper, trampoline); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(real, "sudo")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\n' \"$@\"\nexit 23\n"), 0700); err != nil {
				t.Fatal(err)
			}
			body := quoteShellWord(executable) + " -n probe 'arg with spaces'"
			prefix := ""
			if tc.sudoOnly {
				git := filepath.Join(real, "git")
				if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf git-ran\n"), 0700); err != nil {
					t.Fatal(err)
				}
				body = quoteShellWord(git) + " push; " + body
				prefix = "git-ran"
			}
			cmd := exec.CommandContext(t.Context(), trampoline, "-c", body)
			cmd.Env = []string{"PATH=" + execTrackPath()}
			if !tc.path {
				nativeShell := execTrackShellExecutable(t, "sh")
				script := quoteShellWord(nativeShell) + " -c " + quoteShellWord(body)
				if tc.clean {
					script = "env -i " + script
				}
				rewritten, err := shellinstrumentation.RewriteCommands(script, helper, guardDirectory, sudoDirectory, "nested-command")
				if err != nil {
					t.Fatal(err)
				}
				if displayed := shellinstrumentation.DisplayScript(rewritten, func(path string) bool { return path == guardDirectory || path == sudoDirectory }); displayed != script {
					t.Fatalf("display changed authored shell: %q want %q", displayed, script)
				}
				cmd = exec.CommandContext(t.Context(), nativeShell, "-c", rewritten)
				cmd.Env = []string{"PATH=" + execTrackPath(), shellapproval.DirectoryEnvironment + "=" + sudoDirectory}
			}
			result := make(chan execTrackRun, 1)
			go func() {
				output, err := cmd.CombinedOutput()
				code := 0
				if err != nil {
					code = cmd.ProcessState.ExitCode()
				}
				result <- execTrackRun{stdout: string(output), code: code}
			}()
			select {
			case request := <-shell.guard.requests:
				if request.kind != "sudo" || request.executable != executable || !slices.Equal(request.argv, []string{executable, "-n", "probe", "arg with spaces"}) || !tc.path && request.item != "nested-command" {
					t.Fatalf("nested approval lost command identity: %#v", request)
				}
				request.reply <- shellapproval.Reply{OK: tc.allow, Reason: "nested fixture denial"}
			case got := <-result:
				t.Fatalf("nested sudo ran without approval: %+v", got)
			case <-time.After(10 * time.Second):
				t.Fatal("nested sudo did not request approval")
			}
			select {
			case got := <-result:
				if tc.allow {
					if got.code != 23 || got.stdout != prefix+"-n\nprobe\narg with spaces\n" {
						t.Fatalf("approved command changed: %+v", got)
					}
				} else if got.code != 1 || got.stdout != prefix+"mekugi: sudo command denied: nested fixture denial\n" {
					t.Fatalf("denied command ran: %+v", got)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("nested sudo did not finish")
			}
		})
	}
}
