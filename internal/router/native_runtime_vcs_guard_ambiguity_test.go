//go:build unix

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"golang.org/x/sys/unix"
)

func TestNativeRuntimeVCSGuardAmbiguousConcurrentBash(t *testing.T) {
	t.Parallel()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	for _, vcs := range []bool{true, false} {
		t.Run(fmt.Sprintf("vcs=%t", vcs), func(t *testing.T) {
			u, service, binding := observedRuntimeUIFixture(t)
			startup, err := service.PrepareCommandTracking(t.Context(), helper, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := service.PrepareVCSGuard(t.Context(), helper); err != nil {
				t.Fatal(err)
			}
			fake := t.TempDir()
			nativeObservationWrite(t, filepath.Join(fake, "git"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> vcs-effects\n")
			if err := os.Chmod(filepath.Join(fake, "git"), 0700); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(binding.Workspace, "snapshot")
			nativeObservationWrite(t, snapshot, "")
			script := "printf 'native\\n'; printf once >> effects"
			if vcs {
				script = "git push concurrent; " + script
			}
			input, err := json.Marshal(map[string]string{"command": script})
			if err != nil {
				t.Fatal(err)
			}
			// Both official observer tuples are pending before either Bash starts.
			// Neither identical script has a defensible segment or guard item owner.
			for _, id := range []string{"first", "second"} {
				call := ObservationCall{Binding: binding, ID: id, Tool: "Bash", Input: string(input), Command: script}
				if err := service.owner.before(t.Context(), call); err != nil {
					t.Fatal(err)
				}
				if err := u.runtimeEvent(session.Event{Kind: "tool", ID: id, Role: "Bash", Text: string(input)}); err != nil {
					t.Fatal(err)
				}
			}
			hub := service.owner.execTrack
			hub.mu.Lock()
			pending := len(hub.started)
			hub.mu.Unlock()
			if pending != 2 {
				t.Fatalf("fixture did not register both identical observer candidates: %d", pending)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			var workers sync.WaitGroup
			t.Cleanup(func() {
				cancel()
				workers.Wait()
			})
			type outcome struct {
				stdout, stderr string
				err            error
			}
			results := make(chan outcome, 2)
			for i := range 2 {
				cwd := filepath.Join(binding.Workspace, fmt.Sprintf("claude-ambiguity%d-cwd", i))
				wrapper := "source " + shellsyntax.Quote(snapshot) + " 2>/dev/null || true && eval " + shellsyntax.Quote(script) + " < /dev/null && pwd -P >| " + shellsyntax.Quote(cwd)
				command := exec.CommandContext(ctx, execTrackShellExecutable(t, "bash"), "--noprofile", "--norc", "-c", wrapper)
				command.Dir = binding.Workspace
				command.Env = []string{"PATH=" + fake + ":" + execTrackPath(), "HOME=" + t.TempDir(), "BASH_ENV=" + startup, "CODEX_THREAD_ID=foreign"}
				command.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
				command.Cancel = func() error { return unix.Kill(-command.Process.Pid, unix.SIGKILL) }
				command.WaitDelay = time.Second
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				workers.Go(func() {
					err := command.Wait()
					results <- outcome{stdout.String(), stderr.String(), err}
				})
			}
			if vcs {
				// Receiving both requests before answering proves both real Bash
				// processes are concurrently blocked at their own reached write.
				var requests []*vcsApproval
				for range 2 {
					select {
					case request := <-service.owner.execTrack.approvals:
						if request.item != "" {
							t.Fatalf("ambiguous guard guessed native item %q", request.item)
						}
						requests = append(requests, request)
					case result := <-results:
						t.Fatalf("Bash settled before both independent approvals: %+v", result)
					case <-ctx.Done():
						t.Fatal("concurrent guard requests timed out")
					}
				}
				for _, request := range requests {
					u.runtimeGuardApproval(request)
					if request.item != "" || request.thread != binding.Session || len(u.approvals.pending) != 1 {
						t.Fatalf("fallback approval borrowed identity: %+v", request)
					}
					a := u.approvals.pending[0]
					if a.item != "" {
						t.Fatal("shared approval assigned an ambiguous command row")
					}
					if err := u.answerApproval(a, a.choices[0]); err != nil {
						t.Fatal(err)
					}
				}
			}
			for range 2 {
				select {
				case result := <-results:
					if result.err != nil || result.stdout != "native\n" || result.stderr != "" {
						t.Fatalf("native fallback behavior changed: %+v", result)
					}
				case request := <-service.owner.execTrack.approvals:
					t.Fatalf("unexpected extra or non-VCS approval: %+v", request)
				case <-ctx.Done():
					t.Fatal("native fallback did not finish")
				}
			}
			for name, want := range map[string]string{"effects": "onceonce", "claude-ambiguity0-cwd": binding.Workspace + "\n", "claude-ambiguity1-cwd": binding.Workspace + "\n"} {
				data, err := os.ReadFile(filepath.Join(binding.Workspace, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
				}
			}
			if vcs {
				data, err := os.ReadFile(filepath.Join(binding.Workspace, "vcs-effects"))
				if err != nil || string(data) != "push concurrent\npush concurrent\n" {
					t.Fatalf("remote write execution count changed: %q, %v", data, err)
				}
			}
			hub.mu.Lock()
			defer hub.mu.Unlock()
			if len(hub.tracks) != 0 || len(hub.started) != 2 {
				t.Fatalf("ambiguous commands acquired segment evidence: tracks=%d pending=%d", len(hub.tracks), len(hub.started))
			}
			for _, view := range []*liveActivityView{u.view, u.agents} {
				for _, row := range view.entries {
					if row.native != nil && (row.native.approval != "" || len(row.native.segments) != 0) {
						t.Fatalf("fallback assigned command-row evidence: %+v", row.native)
					}
				}
			}
		})
	}
}
