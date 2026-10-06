package router

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

	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/vcsguard"
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
			guard, _ := vcsguard.Paths(filepath.Join(shell.root, "bin"))
			path := guard + ":" + shimDirectory + ":" + shell.real + ":" + filepath.Dir(execTrackShellExecutable(t, "bash")) + ":" + execTrackPath()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			args := []string{"status"}
			key := [3]string{"thread", "turn", "shim-command"}
			if name == "bash" {
				helper, err := execTrackHelper()
				if err != nil {
					t.Fatal(err)
				}
				script, err := vcsguard.Rewrite("printf 'RAN <%s>\\n' \"$1\" | { cat; sleep .04; }; git push origin main", helper, guard)
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
				var request *vcsApproval
				select {
				case request = <-shell.hub.approvals:
				case <-ctx.Done():
					t.Fatal("second command did not reach its approval")
				}
				outputs := new(activityui.Retention)
				var live execTrackView
				// Approval and segment reports use separate transports. Wait
				// for the observed boundary, not only the approval request.
				for {
					shell.hub.mu.Lock()
					changed := shell.hub.changed
					shell.hub.mu.Unlock()
					live, _ = shell.hub.view(key, false, execSegmentText, outputs)
					if len(live.segments) == 2 && !live.complete && !live.segments[0].running && live.segments[1].running {
						break
					}
					select {
					case <-changed:
					case <-ctx.Done():
						t.Fatalf("commands did not stream separately: %+v", live)
					}
				}
				first, second := live.segments[0], live.segments[1]
				if first.timing.ElapsedNS < int64(20*time.Millisecond) || second.timing.Started.IsZero() || first.output == nil || second.output == nil || first.output == second.output {
					t.Fatal("commands lost individual elapsed time or output identity")
				}
				if strings.Join(first.output.View().Lines, "\n") != strings.TrimSpace(line) || len(second.output.View().Lines) != 0 {
					t.Fatal("live output crossed a command boundary")
				}
				view := newLiveActivityView()
				view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: execSegmentText(args[3]), native: &liveActivityNativeItem{command: args[3], running: true, segments: live.segments}}}})
				finishPacing(view)
				feed := view.renderFeed(100, 30)
				if !strings.Contains(strings.Join(feed.lines, "\n"), "RAN <argument with spaces>") {
					t.Fatal("first command output did not stream while the second command was running")
				}
				seen := make(map[liveActivitySnippet]bool)
				for _, snippet := range feed.snippets {
					if snippet.run == 0 || seen[snippet] {
						continue
					}
					i := len(seen)
					seen[snippet] = true
					block, _ := view.snippetBlock(snippet)
					u := &terminalUI{}
					if block.BatchExit || !view.clickTarget(&block, snippet, 100) || !u.openOutput(view, snippet) || len(u.output.pages) != 2 || u.output.page != i {
						t.Fatal("command click did not select its separate output")
					}
				}
				if len(seen) != 2 {
					t.Fatalf("command snippets=%d: %q; records=%+v", len(seen), feed.lines, view.entries)
				}
				request.reply <- vcsguard.Reply{Reason: "denied in test"}
				if exit, ok := cmd.Wait().(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "remote write denied") {
					t.Fatalf("nested write escaped: %s", stderr.String())
				}
				shell.awaitView(t, key)
				final, _ := shell.hub.view(key, true, execSegmentText, outputs)
				if !final.complete || final.segments[1].timing.ElapsedNS <= 0 || !strings.Contains(strings.Join(final.segments[1].output.View().Lines, "\n"), "remote write denied") || strings.Join(final.segments[0].output.View().Lines, "\n") != strings.TrimSpace(line) {
					t.Fatal("completion lost separate retained output or timing")
				}
			} else if output, err := cmd.CombinedOutput(); err != nil || len(shell.invoked(t)) != 1 {
				t.Fatalf("read-only shim dispatch: %v, %s", err, output)
			}
		})
	}
}
