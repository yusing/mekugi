//go:build journal_e2e

package router

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/vcsguard"
)

// Real Codex runs guarded remote writes with its yolo policy. Guard answers
// apply only to the reached write, preserving local commands and later reads.
func TestAppServerVCSGuardNativeCodexYolo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		shell      string // Model-chosen exec_command shell; empty for Codex's default.
		command    string // ABS_GIT names an absolute fake executable outside PATH.
		answers    []string
		prompts    []string // Distinct later dialogs; avoid answering a stale frame.
		want       []string
		absent     []string
		executions int // Expected occurrences of each FAKE output; default 1.
	}{
		{
			name:    "denied",
			command: "git add -A; git push origin main; echo PUSH_EXIT $?; git log",
			answers: []string{"wait for review"},
			want:    []string{"FAKE git add -A", "mekugi: remote write denied: user denied this command with a reason: wait for review", "PUSH_EXIT 1", "FAKE git log"},
			absent:  []string{"FAKE git push"},
		},
		{
			name:    "approved",
			command: "git add -A; git push origin main; echo PUSH_EXIT $?; git log",
			answers: []string{"1"},
			want:    []string{"FAKE git add -A", "FAKE git push origin main", "PUSH_EXIT 0", "FAKE git log"},
			absent:  []string{"remote write denied"},
		},
		{
			name:       "session-approved",
			command:    "git push origin main; git push origin main; git push origin other; echo OTHER_EXIT $?",
			answers:    []string{"2", "3"},
			prompts:    []string{"git push origin other"},
			want:       []string{"FAKE git push origin main", "OTHER_EXIT 1"},
			absent:     []string{"FAKE git push origin other"},
			executions: 2,
		},
		{
			// An absolute path in an unlisted directory bypasses PATH. The
			// native hook instruments that exact executable, including spaces.
			name:    "absolute-path",
			command: "ABS_GIT add -A; ABS_GIT push origin main; echo PUSH_EXIT $?; ABS_GIT log",
			answers: []string{"3"},
			want:    []string{"FAKE git add -A", "mekugi: remote write denied: user denied this command without a reason", "PUSH_EXIT 1", "FAKE git log"},
			absent:  []string{"FAKE git push"},
		},
		{
			// Zsh reads no BASH_ENV. The native hook preserves its selected
			// shell and guards both executable forms at command reachability.
			name: "zsh", shell: "zsh",
			command: "git push origin main; echo NAME_EXIT $?; ABS_GIT push --tags && echo NEVER; echo SHELL $ZSH_NAME; git log",
			answers: []string{"3", "3"},
			prompts: []string{"git push --tags"},
			want:    []string{"NAME_EXIT 1", "SHELL zsh", "FAKE git log"},
			absent:  []string{"FAKE git push", "NEVER"},
		},
		{
			name: "sh-wrappers", shell: "/bin/sh",
			command: "false && ABS_GIT push unreachable refs/heads/unused; echo BRANCH_EXIT $?; git add -A && env -i ABS_GIT push mirror refs/heads/topic:refs/heads/review && echo NEVER_ENV; echo ENV_EXIT $?; command ABS_GIT push other --tags && echo NEVER; echo COMMAND_EXIT $?; git log",
			answers: []string{"3", "3"},
			prompts: []string{"git push other --tags"},
			want:    []string{"BRANCH_EXIT 1", "FAKE git add -A", "ENV_EXIT 1", "COMMAND_EXIT 1", "FAKE git log"},
			absent:  []string{"FAKE git push", "NEVER"},
		},
		{
			name: "sh-exec-wrapper", shell: "/bin/sh",
			command: "exec ABS_GIT push alternate refs/tags/v2",
			answers: []string{"1"},
			want:    []string{"FAKE git push alternate refs/tags/v2"},
			absent:  []string{"remote write denied"},
		},
		{
			// Each write has a different tool/verb. Denial preserves the
			// following local read, while approval executes exactly once.
			name: "sh-other-vcs", shell: "/bin/sh",
			command: "hg push https://example.invalid/hg; echo HG_EXIT $?; svn commit -m fixture; echo SVN_EXIT $?; jj git push --remote mirror; echo JJ_EXIT $?; hg status; svn status; jj status",
			answers: []string{"3", "3", "1"},
			prompts: []string{"svn commit -m fixture", "jj git push --remote mirror"},
			want:    []string{"HG_EXIT 1", "SVN_EXIT 1", "JJ_EXIT 0", "FAKE jj git push --remote mirror", "FAKE hg status", "FAKE svn status", "FAKE jj status"},
			absent:  []string{"FAKE hg push", "FAKE svn commit"},
		},
		{
			name: "sh-gh-writes", shell: "/bin/sh",
			command: "gh issue create --title fixture --body fixture; echo ISSUE_EXIT $?; gh api -X DELETE repos/fixture/project; echo API_EXIT $?; gh issue list; gh api repos/fixture/project",
			answers: []string{"1", "3"},
			prompts: []string{"gh api -X DELETE repos/fixture/project"},
			want:    []string{"FAKE gh issue create --title fixture --body fixture", "ISSUE_EXIT 0", "API_EXIT 1", "FAKE gh issue list", "FAKE gh api repos/fixture/project"},
			absent:  []string{"FAKE gh api -X DELETE"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Resolve shells outside the caller's guard before starting Codex.
			// A guarded shell from PATH would use the live user's approval socket.
			shellPath := tc.shell
			if shellPath == "" {
				shellPath = execTrackShellExecutable(t, "bash")
			} else if !strings.Contains(shellPath, "/") {
				shellPath = execTrackShellExecutable(t, shellPath)
			}
			shell := newVCSGuardShell(t)
			script := "#!/bin/sh\nprintf 'FAKE %s\\n' \"${0##*/} $*\"\n"
			for _, tool := range vcsguard.Tools {
				if err := os.WriteFile(filepath.Join(shell.real, tool), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy) // As a session has.
			proxy.execTrack = shell.hub
			// The absolute target is deliberately outside both the startup
			// directory and PATH. Only the producer hook can guard this call.
			absoluteDirectory := filepath.Join(t.TempDir(), "unlisted tools with spaces")
			if err := os.Mkdir(absoluteDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			absoluteGit := filepath.Join(absoluteDirectory, "git")
			if err := os.WriteFile(absoluteGit, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			helper, err := execTrackHelper()
			if err != nil {
				t.Fatal(err)
			}
			guard, _ := vcsguard.Paths(filepath.Join(shell.root, "bin"))
			hook, state, err := vcsguard.HookConfig(helper, guard)
			if err != nil {
				t.Fatal(err)
			}
			command := "PATH=" + quoteShellWord(shell.real) + ":\"$PATH\"; export PATH; " + strings.ReplaceAll(tc.command, "ABS_GIT", quoteShellWord(absoluteGit))
			arguments := `{cmd:` + string(mustMarshalJSON(command)) + `, login:false`

			arguments += `, shell:` + string(mustMarshalJSON(shellPath))
			provider := &toolFrontendCodexProvider{
				program:   `const result = await tools.exec_command(` + arguments + `}); text(result.output);`,
				expected:  tc.want,
				finalText: "Recovered after a retry.",
			}
			during := func(t *testing.T, outer io.Writer, await func(string), awaitFrame func(func(string) bool), _ *vt.Emulator) {
				t.Helper()
				for i, answer := range tc.answers {
					// Each request shows once the previous one has ended.
					if i > 0 {
						awaitFrame(func(frame string) bool {
							_, dialog, _ := strings.Cut(frame, "Allow this remote write?")
							return strings.Contains(dialog, tc.prompts[i-1])
						})
					}
					await("Allow this remote write?")
					for _, key := range []string{answer, "\r"} {
						if _, err := io.WriteString(outer, key); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			runAppServerPreviewWith(t, provider, proxy, appServerPreview{
				environment: []string{"PATH=" + execTrackPath(), "ZDOTDIR=" + t.TempDir(), vcsguard.HookEnvironment + "=" + vcsguard.HookCommand(helper, guard)},
				codexArgs:   []string{"-c", hook, "-c", "hooks.state={" + state + "}", "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`},
				approvals:   false,
				noJournal:   true, // Journal delivery is not under test.
				duringTurn:  during,
			})
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if !provider.resultSeen {
				t.Fatalf("command output = %s", provider.output)
			}
			for _, want := range tc.want {
				count := max(1, tc.executions)
				if strings.HasPrefix(want, "FAKE ") && strings.Count(provider.output, want) != count {
					t.Fatalf("expected %d executions of %q: %s", count, want, provider.output)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(provider.output, absent) {
					t.Fatalf("output has %q: %s", absent, provider.output)
				}
			}
		})
	}
}
