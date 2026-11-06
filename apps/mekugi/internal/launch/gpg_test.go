package launch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

// Real GPG and Git consume the guarded command, private inquiry pipes and actual
// secret editor. The random answer exists only in memory and the isolated agent.
func TestGPGUnlock(t *testing.T) {
	gpg := execTrackShellExecutable(t, "gpg")
	gpgconf := execTrackShellExecutable(t, "gpgconf")
	git := execTrackShellExecutable(t, "git")
	shell, directory, _ := newSudoShell(t)
	helper, err := execTrackTestHelper()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gpg", "gpg2"} {
		if err := os.Symlink(helper, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	// gpg-agent's Unix socket needs a short path, independent of the test name.
	keyring, err := os.MkdirTemp("", "mekugi-gpg-")
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=" + directory + ":" + execTrackPath(), "GNUPGHOME=" + keyring, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "CODEX_THREAD_ID=main", shellinstrumentation.ItemEnvironment + "=cmd"}
	killAgent := func() {
		cmd := exec.Command(gpgconf, "--homedir", keyring, "--kill", "gpg-agent")
		cmd.Env = environment
		if err := cmd.Run(); err != nil {
			t.Error("isolated agent shutdown failed")
		}
	}
	t.Cleanup(func() { killAgent(); os.RemoveAll(keyring) })
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(secret)
	keygen := exec.Command(gpg, "--batch", "--pinentry-mode", "loopback", "--passphrase-fd", "0", "--quick-generate-key", "Acceptance <acceptance@example.invalid>", "rsa2048", "sign,encr", "0")
	keygen.Env, keygen.Stdin = environment, strings.NewReader(password+"\n")
	if err := keygen.Run(); err != nil {
		t.Fatal("isolated protected-key creation failed")
	}
	data, encrypted := filepath.Join(keyring, "data"), filepath.Join(keyring, "encrypted")
	if err := os.WriteFile(data, []byte("GPG consumer acceptance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encrypt := exec.Command(gpg, "--batch", "--trust-model", "always", "--recipient", "acceptance@example.invalid", "--output", encrypted, "--encrypt", data)
	encrypt.Env = environment
	if err := encrypt.Run(); err != nil {
		t.Fatal("isolated encrypted input creation failed")
	}
	repo := t.TempDir()
	init := exec.Command(git, "init", "--quiet", repo)
	init.Env = environment
	if err := init.Run(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sign", "cached", "decrypt", "git", "wrong", "skip", "cancel-command", "cancel-owner", "cancel-input", "signal-input", "signal-quit", "signal-input-pty", "signal-quit-pty", "non-passphrase", "caller-input", "caller-command", "inherited-status", "inherited-output", "status-failure", "invalid-status", "option-operand"} {
		t.Run(name, func(t *testing.T) {
			if name != "cached" {
				killAgent()
			}
			output := filepath.Join(keyring, name+".out")
			args := []string{"--batch", "--status-fd=2", "--output", output, "--detach-sign", data}
			if name == "decrypt" {
				args = []string{"--batch", "--decrypt", encrypted}
			}
			if name == "non-passphrase" {
				args = []string{"--edit-key", "acceptance@example.invalid"}
			}
			if name == "caller-input" {
				args = append([]string{"--pinentry-mode", "loopback", "--passphrase-fd", "0"}, args...)
			}
			if name == "caller-command" {
				args = append([]string{"--pinentry-mode", "loopback", "--command-fd", "0"}, args...)
			}
			if name == "inherited-status" {
				args[1] = "--status-fd=9"
			}
			if name == "status-failure" {
				args = []string{"--status-fd=9", "--batch", "--decrypt", data}
			}
			if name == "invalid-status" {
				args = []string{"--status-fd=10", "--batch", "--store", data}
			}
			if name == "option-operand" {
				args = []string{"--batch", "--yes", "--output", "--status-fd=2", "--store", data}
			}
			if name == "inherited-output" {
				args = []string{"--batch", "--yes", "--output", "/dev/fd/9", "--store", data}
			}
			waitingInput := name == "cancel-input" || strings.HasPrefix(name, "signal-")
			if waitingInput {
				args = []string{"--batch", "--yes", "--output", output, "--store"}
			}
			// Exercise absolute-command hook rewriting, not just the PATH shim.
			source := shellCommandString(gpg, args)
			source, err := shellinstrumentation.RewriteCommands(source, helper, "", directory, "cmd")
			if err != nil {
				t.Fatal(err)
			}
			if name == "inherited-status" || name == "status-failure" {
				source += " 9>" + quoteShellWord(output+".status")
			}
			if name == "inherited-output" {
				source += " 9>" + quoteShellWord(output)
			}
			cmd := exec.CommandContext(t.Context(), execTrackShellExecutable(t, "bash"), "-c", source)
			if name == "git" {
				cmd = exec.CommandContext(t.Context(), git, "-C", repo, "-c", "user.name=Acceptance", "-c", "user.email=acceptance@example.invalid", "-c", "user.signingkey=acceptance@example.invalid", "-c", "commit.gpgsign=true", "commit", "--allow-empty", "-m", "signed acceptance")
			}
			cmd.Env = environment
			cmd.Dir = repo
			if name == "caller-input" || name == "caller-command" {
				cmd.Stdin = strings.NewReader(password + "\n")
			}
			if waitingInput {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { reader.Close(); writer.Close() })
				cmd.Stdin = reader // Keep ordinary stdin open throughout cancellation.
				if strings.HasSuffix(name, "-pty") {
					master, slave, err := pty.Open()
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { master.Close(); slave.Close() })
					cmd.Stdin = slave
				}
			}
			result := make(chan execTrackRun, 1)
			var captured bytes.Buffer
			cmd.Stdout, cmd.Stderr = &captured, &captured
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			go func() {
				err := cmd.Wait()
				code := 0
				if err != nil {
					code = cmd.ProcessState.ExitCode()
				}
				result <- execTrackRun{stdout: captured.String(), code: code}
			}()
			if waitingInput {
				deadline := time.After(5 * time.Second)
				tick := time.NewTicker(5 * time.Millisecond)
				defer tick.Stop()
				for {
					if _, err := os.Stat(output); err == nil {
						break
					}
					select {
					case <-tick.C:
					case <-deadline:
						t.Fatal("GPG did not start reading ordinary stdin")
					}
				}
				var sig os.Signal = syscall.SIGKILL
				if strings.HasPrefix(name, "signal-input") {
					sig = syscall.SIGTERM
				}
				if strings.HasPrefix(name, "signal-quit") {
					sig = syscall.SIGQUIT
				}
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
			}
			u, input := newAppServerTestUI()
			u.ctx = t.Context()
			u.startSession("main", repo)
			u.Turn = "turn"
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: "gpg", Status: "inProgress"}})
			if name != "cached" && name != "non-passphrase" && name != "status-failure" && name != "invalid-status" && name != "option-operand" && name != "inherited-output" && !waitingInput && !strings.HasPrefix(name, "caller-") {
				var request *sessioncontrol.ShellApprovalRequest
				select {
				case request = <-shell.guard.Requests():
					if request.Kind != "gpg-passphrase" || request.Thread != "main" || request.Item != "cmd" {
						t.Fatal("GPG request lost its owning command")
					}
				case <-result:
					t.Fatal("GPG ended before requesting its passphrase")
				case <-time.After(10 * time.Second):
					t.Fatal("GPG did not request its passphrase")
				}
				addGuardApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, u.review, &u.pendingEditReceipts, request)
				if call := u.questions.activeCall(); call == nil || call.Thread != "main" || call.Turn != "turn" {
					t.Fatal("GPG question lost native thread/turn ownership")
				}
				if frame := questionTestPaint(t, u, 80); !strings.Contains(frame, "GPG passphrase") {
					t.Fatal("GPG secret editor was not rendered")
				}
				switch name {
				case "cancel-command":
					if err := cmd.Process.Kill(); err != nil {
						t.Fatal(err)
					}
					select {
					case <-request.Done:
					case <-time.After(5 * time.Second):
						t.Fatal("cancelled GPG left its secret request alive")
					}
					expireApprovals(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.notifications, u.review, &u.pendingEditReceipts)
				case "cancel-owner":
					endSyncQuestions(&u.attached, u.shell, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, u.review, &u.pendingEditReceipts, "unrelated-turn")
					if u.questions.activeCall() == nil {
						t.Fatal("unrelated turn cancelled GPG authentication")
					}
					if err := applyInterruption(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.review, &u.pendingEditReceipts, sessioncontrol.Interrupt{Turn: "turn"}); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(input.String(), "turn/interrupt") {
						t.Fatal("owning turn interruption did not reach native host")
					}
					input.Reset()
				case "skip":
					appServerTestKeys(t, u, "\x1d\r")
				default:
					answer := password
					if name == "wrong" {
						answer += "wrong"
					}
					appServerTestKeys(t, u, answer)
					if strings.Contains(questionTestPaint(t, u, 80), password) {
						t.Fatal("passphrase rendered in plaintext")
					}
					appServerTestKeys(t, u, "\r")
				}
			}
			select {
			case request := <-shell.guard.Requests():
				request.Reply <- shellapproval.Reply{}
				t.Fatal("unexpected repeated or cached-key passphrase request")
			case got := <-result:
				failed := name == "wrong" || name == "skip" || strings.HasPrefix(name, "cancel-") || waitingInput || name == "non-passphrase" || name == "status-failure" || name == "invalid-status"
				if (got.code != 0) != failed || strings.Contains(got.stdout, password) {
					t.Fatalf("GPG exit=%d, expected failure=%v (output withheld)", got.code, failed)
				}
				if name == "invalid-status" && got.code != 2 {
					t.Fatal("closed caller status descriptor changed native failure")
				}
				if name == "decrypt" && !strings.Contains(got.stdout, "GPG consumer acceptance") {
					t.Fatal("GPG did not deliver decrypted stdout")
				}
				if (name == "sign" || name == "cached") && !strings.Contains(got.stdout, "[GNUPG:] SIG_CREATED") {
					t.Fatal("caller status descriptor lost native status")
				}
				if name == "inherited-status" || name == "status-failure" {
					status, err := os.ReadFile(output + ".status")
					want := "[GNUPG:] SIG_CREATED"
					if name == "status-failure" {
						want = "[GNUPG:] FAILURE"
					}
					if err != nil || !strings.Contains(string(status), want) || strings.Contains(string(status), password) {
						t.Fatal("inherited status descriptor lost native status or disclosed a secret")
					}
				}
				if name == "option-operand" {
					if info, err := os.Stat(filepath.Join(repo, "--status-fd=2")); err != nil || info.Size() == 0 {
						t.Fatal("GPG output operand was consumed as an option")
					}
				}
				if name == "inherited-output" {
					if info, err := os.Stat(output); err != nil || info.Size() == 0 {
						t.Fatal("GPG lost an inherited output descriptor")
					}
				}
				if waitingInput {
					status := cmd.ProcessState.Sys().(syscall.WaitStatus)
					if !status.Signaled() || strings.HasPrefix(name, "signal-input") && status.Signal() != syscall.SIGTERM || strings.HasPrefix(name, "signal-quit") && status.Signal() != syscall.SIGQUIT {
						t.Fatal("GPG signal termination became an ordinary numeric exit")
					}
				}
				if failed && !waitingInput {
					if info, err := os.Stat(output); err == nil && info.Size() != 0 {
						t.Fatal("failed GPG operation produced a signature")
					}
				}
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
				t.Fatal("GPG did not terminate")
			}
			if input.Len() != 0 || len(u.InputHistory) != 0 || len(u.attached.conversation.Unsent) != 0 || u.questions.activeCall() != nil || strings.Contains(approvalTestTranscript(t, u), password) {
				t.Fatal("GPG secret escaped into retained or model-visible state")
			}
		})
	}
}

func TestGPGNativeStartupFailure(t *testing.T) {
	helper, err := execTrackTestHelper()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "gpg")
	// The selected executable disappears after its metadata probe succeeds.
	// The watcher must close its pipe ends when native process startup fails.
	body := "#!/bin/sh\nprintf 'pinentry-mode:900:2:@:\\nstore:115:128:@:\\n'\nrm -- \"$0\"\n"
	if err := os.WriteFile(executable, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, "--gpg-command", directory, executable, "--store")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if _, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 126 {
		t.Fatal("native startup failure did not terminate with the execution failure")
	}
}
