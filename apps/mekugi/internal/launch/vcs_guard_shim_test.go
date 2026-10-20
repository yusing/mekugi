package launch

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Model mise's system fallback: skip the manager and its active delegating
// shim, then run the first remaining PATH match with the original arguments.
func TestVCSGuardShimFallbackKeepsNestedWritesGuarded(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"bash", "git"} {
		t.Run(name, func(t *testing.T) {
			shell := newVCSGuardShell(t)
			shimDirectory := t.TempDir()
			manager := filepath.Join(shimDirectory, "mise")
			body := `#!/bin/sh
name=${0##*/}
oldIFS=$IFS; IFS=:
for directory in $PATH; do
  candidate=$directory/$name
  [ -x "$candidate" ] || continue
  [ "$candidate" -ef "$0" ] && continue
  [ -n "${__MISE_SHIM_PATH-}" ] && [ "$candidate" -ef "$__MISE_SHIM_PATH" ] && continue
  IFS=$oldIFS
  unset __MISE_SHIM_PATH
  exec "$candidate" "$@"
done
exit 127
`
			if err := os.WriteFile(manager, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(manager, filepath.Join(shimDirectory, name)); err != nil {
				t.Fatal(err)
			}
			guard, _ := shellapproval.Paths(filepath.Join(shell.root, "bin"))
			path := guard + ":" + shimDirectory + ":" + shell.real + ":" + filepath.Dir(execTrackShellExecutable(t, "bash")) + ":" + execTrackPath()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			args := []string{"status"}
			key := [3]string{"thread", "turn", "shim-command"}
			if name == "bash" {
				helper, err := execTrackTestHelper()
				if err != nil {
					t.Fatal(err)
				}
				script, err := shellinstrumentation.RewriteCommands("printf 'RAN <%s>\\n' \"$1\" | { cat; sleep .04; }; git push origin main", helper, guard, "", "")
				if err != nil {
					t.Fatal(err)
				}
				shell.hub.start(key, "bash -c "+quoteShellWord(script))
				args = []string{"--noprofile", "--norc", "-c", script, "label", "argument with spaces"}
			}
			cmd := exec.CommandContext(ctx, filepath.Join(guard, name), args...)
			cmd.Env = append(shell.env, "PATH="+path)
			if name == "bash" {
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				line, err := bufio.NewReader(stdout).ReadString('\n')
				if err != nil || line != "RAN <argument with spaces>\n" {
					t.Fatalf("live stdout or arguments changed: %q, %v", line, err)
				}
				var request *sessioncontrol.ShellApprovalRequest
				select {
				case request = <-shell.guard.Requests():
				case <-ctx.Done():
					t.Fatal("second command did not reach its approval")
				}
				// Approval and report streams are independent; await the report boundary.
				shell.hub.await(t, key, func(view execTrackView) bool {
					return len(view.segments) == 2 && !view.complete && !view.segments[0].Running && view.segments[1].Running
				})
				live, _ := shell.hub.view(key, false, execSegmentText)
				first, second := live.segments[0], live.segments[1]
				if first.Timing.ElapsedNS < int64(20*time.Millisecond) || second.Timing.Started.IsZero() || first.Output == nil || second.Output == nil || first.Output == second.Output {
					t.Fatal("commands lost individual elapsed time or output identity")
				}
				if strings.Join(first.Output.View().Lines, "\n") != strings.TrimSpace(line) || len(second.Output.View().Lines) != 0 {
					t.Fatal("live output crossed a command boundary")
				}
				view := newActivityAdapter()
				view.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: execSegmentText(args[3]), Native: &sessionpresentation.NativeItem{Command: args[3], Running: true, Segments: live.segments}}}})
				finishPacing(view)
				feed := view.RenderFeed(100, 30)
				if !strings.Contains(strings.Join(feed.Lines, "\n"), "RAN <argument with spaces>") {
					t.Fatal("first command output did not stream while the second command was running")
				}
				seen := make(map[activityui.Snippet]bool)
				for _, snippet := range feed.Snippets {
					if snippet.Run == 0 || seen[snippet] {
						continue
					}
					i := len(seen)
					seen[snippet] = true
					block, _ := view.SnippetBlock(snippet)
					u := &terminalUI{Attachment: &terminalsession.Attachment{}}
					if block.BatchExit || !view.ClickTarget(&block, snippet, 100) || !u.openOutput(view.bind(u), snippet) || len(u.Dialog.Pages) != 2 || u.Dialog.Page != i {
						t.Fatal("command click did not select its separate output")
					}
				}
				if len(seen) != 2 {
					t.Fatalf("command snippets=%d: %q; records=%+v", len(seen), feed.Lines, view.Entries)
				}
				request.Reply <- shellapproval.Reply{Reason: "denied in test"}
				if exit, ok := cmd.Wait().(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "remote write denied") {
					t.Fatalf("nested write escaped: %s", stderr.String())
				}
				shell.awaitView(t, key)
				final, _ := shell.hub.view(key, true, execSegmentText)
				if !final.complete || final.segments[1].Timing.ElapsedNS <= 0 || !strings.Contains(strings.Join(final.segments[1].Output.View().Lines, "\n"), "remote write denied") || strings.Join(final.segments[0].Output.View().Lines, "\n") != strings.TrimSpace(line) {
					t.Fatal("completion lost separate retained output or timing")
				}
			} else if output, err := cmd.CombinedOutput(); err != nil || len(shell.invoked(t)) != 1 {
				t.Fatalf("read-only shim dispatch: %v, %s", err, output)
			}
		})
	}
}
