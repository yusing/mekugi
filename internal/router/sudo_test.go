package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/sudoask"
	"github.com/yusing/mekugi/internal/vcsguard"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell, directory, real := newSudoShell(t)
			helper, err := execTrackHelper()
			if err != nil {
				t.Fatal(err)
			}
			// Only askpass reads the secret. Its output never reaches the host.
			fake := "#!/bin/sh\n[ \"$1\" = -A ] || exit 42\npassword=$(\"$SUDO_ASKPASS\" 'Password:') || exit 1\n[ \"$password\" = 1234-secret ] || exit 1\nprintf 'unlocked\\n'\n"
			if tc.name == "retry" {
				fake = strings.Replace(fake, "[ \"$password\" = 1234-secret ] || exit 1", "[ \"$password\" = 1234-secret ] || password=$(\"$SUDO_ASKPASS\" 'Password:') || exit 1\n[ \"$password\" = 1234-secret ] || exit 1", 1)
			}
			if err := os.WriteFile(filepath.Join(real, "sudo"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(filepath.Join(directory, "sudo"), "id")
			if tc.shell != "" {
				script := quoteShellWord(filepath.Join(real, "sudo")) + " id"
				if tc.shell == "sh" {
					script = "env -i " + script
				}
				script, err = vcsguard.RewriteCommands(script, helper, "", directory, "cmd")
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(execTrackShellExecutable(t, tc.shell), "-c", script)
			}
			cmd.Env = []string{"PATH=" + directory + ":" + real + ":" + execTrackPath(), "CODEX_THREAD_ID=main", vcsguard.ItemEnvironment + "=cmd"}
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
			u.turn = "turn"
			for i, keys := range tc.keys {
				var request *vcsApproval
				select {
				case request = <-shell.hub.approvals:
					kind := "sudo"
					if i > 0 {
						kind = "sudo-password"
					}
					if request.kind != kind || request.item != "cmd" {
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
			if input.Len() != 0 || len(u.unsent) != 0 || len(u.inputHistory) != 0 || u.questions.active != nil {
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
	u.view.painter.Theme = livediff.DarkTheme
	u.status, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
	u.session.start("main", "/workspace")
	u.turn = "turn"
	u.addGuardApproval(&vcsApproval{kind: "sudo", thread: "main", cwd: "/workspace", argv: []string{"sudo", "apt", "update"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})})
	rows, _ := u.mainFrame(80, 20, 0)
	assertNativeUISnapshot(t, "native-sudo-approval", rows)
	appServerTestKeys(t, u, "1\r")
	u.addGuardApproval(&vcsApproval{kind: "sudo-password", thread: "main", cwd: "/workspace", argv: []string{"sudo", "apt", "update"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1234-secret")
	rows, _ = u.mainFrame(80, 20, 0)
	assertNativeUISnapshot(t, "native-sudo-password", rows)
}

func TestSudoPasswordWithdrawal(t *testing.T) {
	u, _ := newAppServerTestUI()
	request := &vcsApproval{kind: "sudo-password", thread: "main", argv: []string{"sudo", "id"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})}
	u.addGuardApproval(request)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "private-draft")
	request.outcome = "withdrawn"
	close(request.done)
	if !u.expireApprovals() || u.questions.active != nil || len(u.inputHistory) != 0 || len(u.questions.calls) != 0 {
		t.Fatal("withdrawal retained a secret draft")
	}
	if len(request.reply) != 0 {
		t.Fatal("answered a withdrawn sudo request")
	}
}

func TestSudoSecretEditorAndSessionSwitch(t *testing.T) {
	u, input := newAppServerTestUI()
	request := &vcsApproval{kind: "sudo-password", thread: "main", argv: []string{"sudo", "id"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})}
	u.openSudoPassword(&nativeApproval{thread: "main", turn: "old-turn", subject: "sudo id", guard: request})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "fixture-secret")
	if _, err := u.key(7); err != nil || u.draft != "fixture-secret" {
		t.Fatalf("secret editor dispatch: %v", err)
	}
	u.hideQuestions()
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	input.Reset()
	u.thread, u.turn = "new-thread", ""
	appServerTestKeys(t, u, "new task\r")
	if !strings.Contains(input.String(), "turn/start") {
		t.Fatal("old-session password blocked new-session input")
	}
	u.openQuestions()
	questionTestPaint(t, u, 80)
	if u.draft != "fixture-secret" {
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

func newSudoShell(t *testing.T) (*execTrackShell, string, string) {
	t.Helper()
	shell := newExecTrackShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	frontend := filepath.Join(shell.root, "bin")
	_, channel := vcsguard.Paths(frontend)
	if err := shell.hub.listenVCSGuard(t.Context(), channel); err != nil {
		t.Fatal(err)
	}
	directory := sudoask.Path(frontend)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sudo", sudoask.Askpass} {
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
			case request := <-shell.hub.approvals:
				if request.kind != "sudo" || !slices.Equal(request.argv, append([]string{"sudo"}, args...)) {
					t.Fatal("command approval lost exact arguments")
				}
				request.reply <- vcsguard.Reply{OK: allow, Reason: "fixture denial"}
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
		case request := <-shell.hub.approvals:
			t.Fatalf("unexpected request kind=%s", request.kind)
		case <-time.After(10 * time.Second):
			t.Fatal("sudo did not complete")
		}
	}
}
