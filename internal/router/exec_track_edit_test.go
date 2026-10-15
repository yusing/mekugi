package router

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/libraries/diffmodel"

	"github.com/yusing/mekugi/internal/appserver"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestUISnapshotExecTrackEditCompletesBeforeFollowingCommand(t *testing.T) {
	t.Parallel()
	edits := []string{"cat > source.go <<'EOF'\nafter\nEOF", "python3 - <<'PY'\nfrom pathlib import Path\nPath('source.go').write_text('package p\\n\\nvar A = 1\\n')\nPath('other.go').write_text('package p\\n\\nvar B = 2\\n')\nPY\ngit --version >/dev/null\ngofmt -w source.go other.go"}
	edits = append(edits, strings.ReplaceAll(edits[len(edits)-1], "git --version >/dev/null\n", ""))
	edits = append(edits,
		"python3 - <<'PY'\nfrom pathlib import Path\np=Path('source.go')\np.write_text('package p\\n\\nvar A = 1\\n')\nPath('obsolete.txt').unlink()\nPY\ncat > other.go <<'EOF'\npackage p\n\nvar B = 2\nEOF",
		"python3 - <<'PY'\nexec(\"from pathlib import Path\\nfor name, letter in [('source', 'A'), ('other', 'B')]:\\n Path(name + '.go').write_text('package p\\\\n\\\\nvar ' + letter + ' = 1\\\\n')\")\nPY",
	)
	for _, edit := range edits {
		t.Run(filepath.Base(strings.Fields(edit)[0]), func(t *testing.T) {
			shell := newExecTrackShell(t)
			workspace := t.TempDir()
			delayedUI := strings.HasPrefix(edit, "python3") && !strings.Contains(edit, "git --version")
			opaque := strings.Contains(edit, "unlink()") || strings.Contains(edit, "exec(")
			if strings.Contains(edit, "unlink()") {
				writeTestFile(t, filepath.Join(workspace, "obsolete.txt"), "delete me\n")
			}
			before, after, completedText := "before\n", "+after", "Edited"
			if strings.HasPrefix(edit, "python3") {
				after = "+var A = 1"
				writeTestFile(t, filepath.Join(workspace, "other.go"), "before\n")
			}
			writeTestFile(t, filepath.Join(workspace, "source.go"), before)
			// The following command cannot exit until the test has inspected a
			// real terminal frame and the preview's completed filesystem view.
			script := edit + "\nprintf 'test running\\n'; read -r answer"
			if strings.HasPrefix(edit, "python3") {
				writeTestFile(t, filepath.Join(workspace, "Makefile"), "test:\n\t@printf 'test running\\n'; read -r answer\n")
				script = strings.TrimSuffix(edit, "\ngofmt -w source.go other.go") + "\nif [ \"$?\" -eq 0 ]; then gofmt -w source.go other.go && make test; fi"
			}
			if delayedUI {
				script = edit + "\nmake test"
			}
			if strings.Contains(edit, "exec(") {
				script = edit + "\npython3 -c 'import sys; print(\"test running\", flush=True); assert sys.stdin.readline().strip() == \"continue\"'"
			}
			observation, ok := captureExecObservation([]execCommandInput{{Command: script, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{previewOnly: true, directory: workspace})
			var snapshots *workspaceSnapshots
			if observation == nil || !ok && !opaque {
				t.Fatal("missing observation")
			}
			if opaque {
				snapshots = newWorkspaceSnapshots(t.TempDir())
				defer snapshots.close()
				// This fixture requires a baseline. Use the error-bearing snapshot
				// operation rather than the production best-effort fallback, which
				// deliberately hides setup failures and narrows capture coverage.
				baselineCtx, cancel := context.WithTimeout(t.Context(), workspaceSnapshotWarm)
				var err error
				observation.Tree, err = snapshots.repo(workspace).snapshot(baselineCtx)
				cancel()
				if err != nil {
					t.Fatalf("prepare workspace baseline: %v", err)
				}
				if files, _ := execPreviewProjected(t.Context(), *observation); len(files) != 0 {
					t.Fatal("regression must bypass source prediction")
				}
			} else if !ok || observation == nil || len(observation.Files) == 0 {
				t.Fatal("missing edit capture")
			}
			if strings.Contains(edit, "git --version") {
				var err error
				script, err = shellinstrumentation.RewriteCommands(script, "/private/mekugi-exec", shell.hub.instrumentation.vcs, "", "edit")
				if err != nil {
					t.Fatal(err)
				}
			}
			if delayedUI {
				script = shellinstrumentation.ShScript(filepath.Join(shell.root, "exec-track.sh"), script)
			}
			u := newAppServerSessionTestUI(t, workspace)
			u.execTrack = shell.hub
			u.ensureShell()
			defer u.shell.diffScreen.Close()
			u.status = "Working"
			item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			broker := newLiveDiffBroker(ctx)
			broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := broker.subscribe()
			registry := &execWindowRegistry{tracker: shell.hub}
			registry.open(&execWindow{ref: "edit", thread: "main", turn: "turn", roots: []string{workspace}})
			defer registry.close("edit")
			registry.preview("edit", *observation, broker, workspace, "main", "/root", snapshots)
			params := map[string]any{"threadId": "main", "turnId": "turn", "item": item}
			if delayedUI {
				wire, err := json.Marshal(&params)
				if err != nil {
					t.Fatal(err)
				}
				shell.hub.observeCommandStart(appserver.Message{Method: "item/started", Params: wire})
			} else {
				appServerTestNotify(t, u, "item/started", params)
			}
			// Seed the same running dock before execution so completion must
			// actively retire it, rather than simply avoid opening a new card.
			if !opaque {
				u.shell.preview(diffview.Preview{ID: "running:edit", Workspace: workspace, Thread: "main", Caller: "/root", Status: diffview.PreviewRunning, Input: "observing edit"})
			}
			cmd := exec.CommandContext(ctx, execTrackShellExecutable(t, "bash"), "-lc", script)
			cmd.Dir, cmd.Env = workspace, shell.env
			if strings.Contains(edit, "gofmt") {
				cmd.Args[1] = "-c" // The isolated HOME has no mise configuration.
				goRoot, err := exec.CommandContext(ctx, "go", "env", "GOROOT").Output()
				if err != nil || strings.TrimSpace(string(goRoot)) == "" {
					t.Fatalf("resolve Go toolchain root: %v (%q)", err, goRoot)
				}
				for i, value := range cmd.Env {
					if strings.HasPrefix(value, "PATH=") {
						cmd.Env[i] = "PATH=" + filepath.Join(strings.TrimSpace(string(goRoot)), "bin") + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
					}
				}
			}
			for i, value := range cmd.Env {
				if strings.HasPrefix(value, "CODEX_THREAD_ID=") {
					cmd.Env[i] = "CODEX_THREAD_ID=main"
				}
			}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					cancel()
					_ = cmd.Wait()
				}
			}()
			if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "test running\n" {
				t.Fatalf("following command did not start: %q, %v", line, err)
			}
			if delayedUI {
				// The shell reached the test before the UI consumed its start.
				// Without transport observation its tracking deadline has expired.
				if !shell.hub.tracking([3]string{"main", "turn", "edit"}) {
					t.Fatal("edit report lost while host start waited for presentation")
				}
				appServerTestNotify(t, u, "item/started", params)
			}
			wantFiles := 1
			if strings.HasPrefix(edit, "python3") {
				wantFiles = 2
			}
			if strings.Contains(edit, "unlink()") {
				wantFiles++
			}
			var completed *diffview.Preview
			streamDone := opaque
			for completed == nil || !streamDone {
				select {
				case <-sub.previewReady:
					for _, event := range broker.takePreviews(sub) {
						if event.Preview != nil {
							u.shell.preview(*event.Preview)
							if event.Preview.Complete {
								if event.Preview.EditReceipt != "" && len(event.Preview.Files) == wantFiles {
									completed = event.Preview
								} else {
									streamDone = true
								}
							}
						}
					}
				case <-ctx.Done():
					t.Fatal("edit preview waited for the following command")
				}
			}
			if len(completed.Files) != wantFiles || completed.Footer != "observed edit" {
				t.Fatalf("completion lacks actual observed edit: %+v", completed)
			}
			if !slices.ContainsFunc(completed.Files, func(file diffmodel.ReviewFile) bool { return strings.Contains(file.Diff, after) }) {
				t.Fatalf("completion lost observed source content: %+v", completed.Files)
			}
			awaitMain(t, u, completedText)
			awaitMain(t, u, "Running")
			if strings.HasPrefix(edit, "python3") {
				var rows []string
				for _, block := range u.view.entries[0].blocks {
					if _, _, _, _, ok := activityui.EditStat(block.Label); ok {
						rows = append(rows, u.view.painter.Block(block, 120)...)
					}
				}
				name := "tracked-python-edited-before-test"
				if opaque {
					name = "tracked-python-opaque-before-test"
				}
				if strings.Contains(edit, "exec(") {
					name = "tracked-python-computed-before-test"
				}
				assertNativeUISnapshot(t, name, rows)
			}
			u.shell.animating(time.Now().Add(nativeDockMinimum + time.Second))
			if len(u.shell.liveDock.Order) != 0 || u.status != "Working" || !registry.find("edit").closed.IsZero() {
				t.Fatal("edit completion retained the dock or completed the host lifecycle")
			}
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			screen := vt.NewEmulator(140, 40)
			defer screen.Close()
			frame := make(chan string, 1)
			go func() {
				var all strings.Builder
				buf := make([]byte, 8192)
				for {
					n, err := master.Read(buf)
					all.Write(buf[:n])
					if strings.Contains(all.String(), "\x1b[?2026l") || err != nil {
						frame <- all.String()
						return
					}
				}
			}()
			finishPacing(u.view, u.agents)
			if err := u.paint(slave, 140, 40); err != nil {
				t.Fatal(err)
			}
			select {
			case raw := <-frame:
				_, _ = screen.Write([]byte(raw))
				shown := screen.String()
				if strings.Contains(shown, "requested") || strings.Contains(shown, " · ran") || strings.Contains(shown, "LIVE ·") || !strings.Contains(shown, completedText) || !strings.Contains(shown, "Running") {
					t.Fatalf("terminal did not settle only the edit:\n%s", shown)
				}
			case <-ctx.Done():
				t.Fatal("terminal frame timed out")
			}
			if _, err := io.WriteString(input, "continue\n"); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAppServerTrackedEditCommandOutcomeBeforeHostExit(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	script := "sed -i 's/absent/after/' source.go; go test ./..."
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0})
	awaitMain(t, u, "via sed · requested")
	report.send(segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	awaitMain(t, u, "via sed · ran")
	shown := awaitMain(t, u, "Running go test")
	if strings.Contains(shown, "requested") || !strings.Contains(shown, "via sed · ran") || u.session.commands[[3]string{"main", "turn", "edit"}] == nil {
		t.Fatalf("segment completion changed host lifecycle:\n%s", shown)
	}
	// A zero exit also describes a no-op. Without file evidence, show the
	// command outcome but do not invent a confirmed edit or applied counts.
	for _, block := range u.view.entries[0].blocks {
		if block.Verb == "Edit" {
			if _, _, _, _, counts := activityui.EditStat(block.Label); counts || block.GroupHeader == "Edited" || block.EditOutcome != "ran" {
				t.Fatalf("command completion confirmed a file change: %+v", block)
			}
		}
	}
}

func TestExecPreviewTrackIdentityAndLifecycle(t *testing.T) {
	t.Parallel()
	const script = "sed -i 's/a/b/' a; go test ./...; sed -i 's/b/c/' a"
	for _, tc := range []struct {
		name                                                                 string
		thread, turn                                                         string
		ambiguous, pending, ended, done, lastEnded, wantTracked, wantSettled bool
	}{
		{name: "later edit pending", thread: "main", turn: "turn", wantTracked: true},
		{name: "last edit ended", thread: "main", turn: "turn", lastEnded: true, wantTracked: true, wantSettled: true},
		{name: "short circuited", thread: "main", turn: "turn", done: true, wantTracked: true, wantSettled: true},
		{name: "report lost", thread: "main", turn: "turn", ended: true},
		{name: "other thread", thread: "child", turn: "turn"},
		{name: "other turn", thread: "main", turn: "old"},
		{name: "pending duplicate", thread: "main", turn: "turn", pending: true, lastEnded: true, wantTracked: true},
		{name: "ambiguous", thread: "main", turn: "turn", ambiguous: true, lastEnded: true, wantTracked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			track := &execTrack{script: script, serial: 1, ended: tc.ended, done: tc.done, segments: []execTrackSegment{{edit: true, ended: true}, {began: true}, {edit: true, ended: tc.lastEnded}}}
			hub := &execTrackHub{changed: make(chan struct{}), tracks: map[[3]string]*execTrack{{"main", "turn", "one"}: track}}
			if tc.ambiguous {
				hub.tracks[[3]string{"main", "turn", "two"}] = track
			}
			if tc.pending {
				hub.started = []execTrackCommand{{key: [3]string{"main", "turn", "two"}, script: script, serial: 1}}
			}
			tracking := newExecPreviewTrack(hub, tc.thread, tc.turn, []execCommandInput{{Command: script}})
			defer tracking.close()
			if tracking != nil {
				for key, track := range hub.tracks {
					tracking.retain(key, track)
				}
			}
			state := tracking.state()
			tracked, settled := state.tracked, state.settled
			if tracked != tc.wantTracked || settled != tc.wantSettled {
				t.Fatalf("state = %v, %v", tracked, settled)
			}
		})
	}
}

func TestExecPreviewTrackGuardIdentity(t *testing.T) {
	for _, command := range []struct {
		name, tail string
		vcs        bool
	}{
		{"vcs", "git --version", true},
		{"sudo", "sudo id", false},
		{"mixed", "git --version; sudo id", true},
		{"vcs disabled", "git --version; sudo id", false},
	} {
		t.Run(command.name, func(t *testing.T) {
			original := "gofmt -w source.go; " + command.tail
			const guard = "/private/session/vcs-guard"
			for _, scenario := range []string{"current", "item-bound", "wrong item", "captured guarded", "foreign", "pending duplicate", "lookalike helper"} {
				t.Run(scenario, func(t *testing.T) {
					hub := &execTrackHub{instrumentation: execInstrumentation{vcs: "/private/session/vcs-guard", sudo: "/private/session/sudo-guard"}, changed: make(chan struct{}), tracks: make(map[[3]string]*execTrack)}
					script, directory := original, guard
					sudoDirectory := "/private/session/sudo-guard"
					if !command.vcs {
						directory = ""
					}
					if scenario == "foreign" {
						if command.vcs {
							directory = "/private/other/vcs-guard"
						}
						sudoDirectory = "/private/other/sudo-guard"
					}
					if scenario == "lookalike helper" {
						if command.vcs {
							script = strings.Replace(script, "git --version", "/usr/bin/git --version", 1)
						} else {
							script = strings.Replace(script, "sudo id", "/usr/bin/sudo id", 1)
						}
					}
					item := ""
					if scenario == "item-bound" {
						item = "command"
					} else if scenario == "wrong item" {
						item = "other"
					}
					wrapped, err := shellinstrumentation.RewriteCommands(script, "/unrelated/mekugi-exec", directory, sudoDirectory, item)
					if err != nil {
						t.Fatal(err)
					}
					commands := []execCommandInput{{Command: script}}
					if scenario == "captured guarded" {
						commands[0].Command = wrapped
					}
					p := newExecPreviewTrack(hub, "main", "turn", commands)
					defer p.close()
					key := [3]string{"main", "turn", "command"}
					hub.start(key, "/bin/bash -lc "+quoteShellWord(wrapped))
					if _, _, ok := hub.claim(t.Context(), segmenttransport.Message{Thread: "main", Script: wrapped}); !ok {
						t.Fatal("raw report ownership failed")
					}
					hub.completed(key)
					if scenario == "pending duplicate" {
						hub.start([3]string{"main", "turn", "pending"}, "/bin/bash -lc "+quoteShellWord(original))
					}
					settled := p.state().settled
					if settled != (scenario == "current" || scenario == "item-bound" || scenario == "captured guarded") {
						t.Fatalf("preview settled = %v", settled)
					}
				})
			}
		})
	}
}

func TestAppServerTrackedEditFailureAndSkip(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	script := "sed -i 's/a/b/' missing && sed -i 's/b/c/' skipped"
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(2)}, segmenttransport.Message{Type: segmenttransport.Done, Code: new(2)}, segmenttransport.Message{Type: segmenttransport.Closed})
	report.conn.Close()
	item["status"], item["exitCode"] = "failed", 2
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	shown := awaitMain(t, u, "via sed · skipped")
	if strings.Contains(shown, "requested") || strings.Contains(shown, "completed") || !strings.Contains(shown, "failed") || !strings.Contains(shown, "exit 2") {
		t.Fatalf("failed/skipped edits mislabeled:\n%s", shown)
	}
}

func TestAppServerTrackedSkippedEditBeforeHostExit(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	script := "false && sed -i 's/a/b/' skipped; go test ./..."
	tracking := newExecPreviewTrack(hub.execTrackHub, "main", "turn", []execCommandInput{{Command: script}})
	defer tracking.close()
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(1)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 2})
	awaitMain(t, u, "via sed · skipped")
	shown := awaitMain(t, u, "Running")
	if !strings.Contains(shown, "Running go test") || strings.Contains(shown, "requested") || strings.Contains(shown, "skipped · skipped") {
		t.Fatalf("skipped edit waited for host exit:\n%s", shown)
	}
	if state := tracking.state(); !state.tracked || !state.settled {
		t.Fatalf("skipped edit kept its preview live: tracked=%v settled=%v", state.tracked, state.settled)
	}
}

func TestExecTrackRapidSegmentsKeepOwnOutput(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	var script strings.Builder
	for i := range 128 {
		fmt.Fprintf(&script, "printf '%d\\n';\n", i)
	}
	key := [3]string{"thread", "turn", "rapid"}
	shell.hub.start(key, "/bin/bash -lc "+quoteShellWord(script.String()))
	result := runExecTrackShell(t, shell.env, script.String())
	view := shell.awaitView(t, key)
	if result.code != 0 || !view.complete || len(view.segments) != 128 {
		t.Fatalf("rapid list incomplete: code=%d complete=%v segments=%d", result.code, view.complete, len(view.segments))
	}
	for i, segment := range view.segments {
		if len(segment.tail) != 1 || segment.tail[0] != fmt.Sprint(i) {
			t.Fatalf("segment %d borrowed or lost output: %q", i, segment.tail)
		}
	}
}

func TestExecTrackGroupedCodeModePreviewRetainsCompletedCommands(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "before\n")
	first := "cat > source.go <<'EOF'\nmiddle\nEOF\nprintf first"
	second := "cat > source.go <<'EOF'\nafter\nEOF\ngo test ./..."
	source := fmt.Sprintf("await tools.exec_command({cmd:%q,workdir:%q}); await tools.exec_command({cmd:%q,workdir:%q});", first, workspace, second, workspace)
	commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
	observation, ok := captureExecObservation(commands, dynamic, true, execCaptureEnv{previewOnly: true, directory: workspace})
	if !ok || observation == nil || len(commands) != 2 || len(execPreviewExpected(t.Context(), *observation)) != 0 {
		t.Fatal("fixture must capture two sequential edits in one exec cell")
	}
	auto, stop := newAutoLiveDiff(t.Context())
	defer stop()
	auto.enabled.Store(true)
	auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := auto.events.subscribe()
	proxy := &mekugiProxy{execTrack: hub.execTrackHub, execWindows: &execWindowRegistry{tracker: hub.execTrackHub}, autoLiveDiff: auto}
	transform := &mekugiResponseTransform{proxy: proxy, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
	transform.openExecWindow("cell", observation, nil)
	defer proxy.execWindows.close("cell")
	firstItem := map[string]any{"id": "first", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(first), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": firstItem})
	firstReport := dialExecTrackReport(t, hub, first)
	writeTestFile(t, path, "middle\n")
	firstReport.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1}, segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed})
	firstReport.conn.Close()
	firstItem["status"], firstItem["exitCode"] = "completed", 0
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": firstItem})
	deadline := time.Now().Add(3 * time.Second)
	for hub.tracking([3]string{"main", "turn", "first"}) {
		u.flushStreamOutput()
		if time.Now().After(deadline) {
			t.Fatal("Activity did not retire the first report")
		}
		time.Sleep(5 * time.Millisecond)
	}
	secondItem := map[string]any{"id": "second", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(second), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": secondItem})
	secondReport := dialExecTrackReport(t, hub, second)
	writeTestFile(t, path, "after\n")
	secondReport.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	for {
		select {
		case <-sub.previewReady:
			for _, event := range auto.events.takePreviews(sub) {
				if event.Preview == nil || !event.Preview.Complete || event.Preview.EditReceipt != "" {
					continue
				}
				if len(event.Preview.Files) != 1 || !strings.Contains(event.Preview.Files[0].Diff, "-before") || !strings.Contains(event.Preview.Files[0].Diff, "+after") {
					t.Fatalf("grouped preview lost the captured baseline: %+v", event.Preview)
				}
				if !hub.tracking([3]string{"main", "turn", "second"}) || !proxy.execWindows.find("cell").closed.IsZero() {
					t.Fatal("preview completion ended the host command or cell")
				}
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("grouped preview waited for the second command's tests")
		}
	}
}

func TestExecTrackCodeModeNativeEditEndsBeforeSiblingTest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ script, shell string }{
		{"cat > source.go <<'EOF'\npackage p; var B=2\nEOF", "bash"},
		{"tee source.go <<'EOF'\npackage p; var B=2\nEOF\nprintf '// observed suffix\\n' >> source.go", "sh"},
		{"python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('A=1', 'B=2')\nopen(p,'w').write(s)\nPY", "bash"},
	} {
		t.Run(tc.shell+"/"+strings.Fields(tc.script)[0], func(t *testing.T) {
			script, shell := tc.script, tc.shell
			u, hub := newTrackedAppServerUI(t)
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.go")
			writeTestFile(t, path, "package p; var A=1\n")
			// Two nested calls in one cell with no shell segment report.
			// Native completion ends only the edit; the sibling test stays pending.
			source := fmt.Sprintf("await tools.exec_command({cmd:%q,workdir:%q,shell:%q,login:false}); await tools.exec_command({cmd:'go test ./...',workdir:%q});", script, workspace, "/bin/"+shell, workspace)
			commands, dynamic := stockLiteralExecCommands(source, workspace, shell)
			observation, ok := captureExecObservation(commands, dynamic, true, execCaptureEnv{previewOnly: true, directory: workspace})
			if !ok || observation == nil || len(commands) != 2 {
				t.Fatal("missing grouped edit observation")
			}
			auto, stop := newAutoLiveDiff(t.Context())
			defer stop()
			auto.enabled.Store(true)
			auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := auto.events.subscribe()
			proxy := &mekugiProxy{execTrack: hub.execTrackHub, execWindows: &execWindowRegistry{tracker: hub.execTrackHub}, autoLiveDiff: auto}
			transform := &mekugiResponseTransform{proxy: proxy, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
			transform.openExecWindow("cell", observation, nil)
			defer proxy.execWindows.close("cell")
			item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/tmp/vcs-guard/" + shell + " -c " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			cmd := exec.CommandContext(t.Context(), execTrackShellExecutable(t, shell), "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+execTrackPath(), "BASH_ENV=")
			cmd.Dir = workspace
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("edit failed: %v: %s", err, out)
			}
			if shell == "sh" {
				preview := waitExecScopePreview(t, auto.events, func(p diffview.Preview) bool { return len(p.Files) > 0 && !p.Complete })
				u.shell.preview(preview)
				revealNativeDock(u.shell)
				if u.shell.liveDock.Live() == 0 {
					t.Fatal("source preview did not open before native completion")
				}
			}
			item["status"], item["exitCode"] = "completed", 0
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "test", "type": "commandExecution", "command": "/bin/bash -lc 'go test ./...'", "status": "inProgress"}})
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			streamDone, receiptDone := false, false
			for {
				select {
				case <-sub.previewReady:
					for _, event := range auto.events.takePreviews(sub) {
						if event.Preview != nil && event.Preview.Complete {
							wantFiles := 1
							if len(event.Preview.Files) != wantFiles || event.Preview.Footer != "observed edit" {
								t.Fatalf("completion lost actual edit: %+v", event.Preview)
							}
							if !proxy.execWindows.find("cell").closed.IsZero() || u.session.commands[[3]string{"main", "turn", "test"}] == nil {
								t.Fatal("edit completion ended enclosing cell or sibling test")
							}
							u.shell.preview(*event.Preview)
							streamDone = streamDone || event.Preview.EditReceipt == ""
							receiptDone = receiptDone || event.Preview.EditReceipt != ""
							if !streamDone || !receiptDone {
								continue
							}
							u.shell.animating(time.Now().Add(nativeDockMinimum + time.Millisecond))
							screen := vt.NewEmulator(100, 30)
							defer screen.Close()
							if err := u.paint(screen, 100, 30); err != nil {
								t.Fatal(err)
							}
							if len(u.shell.liveDock.Order) != 0 || strings.Contains(screen.String(), "LIVE ·") {
								t.Fatal("completed native command retained the live dock")
							}
							for _, view := range []*liveActivityView{u.view, u.agents} {
								for _, entry := range view.entries {
									if entry.native != nil && entry.native.item == "edit" {
										if !u.shell.openActivityEdit(view, entry.Seq, "source.go") || !strings.Contains(u.shell.output.pages[0].Code, "+package p; var B=2") {
											t.Fatal("completed shell edit waited for the next cell or sibling exit to become clickable")
										}
										u.shell.outputKey("\x1b")
									}
								}
							}
							return
						}
					}
				case <-timer.C:
					t.Fatal("nested edit preview waited for later tests in the same cell")
				}
			}
		})
	}
}

func TestExecTrackStreamableEditFinishesBeforeFormatter(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "before\n")
	script := "cat > source.go <<'EOF'\npackage p; var A=1\nEOF\ngofmt -w source.go; go test ./..."
	observation, ok := captureExecObservation([]execCommandInput{{Command: script, Shell: "bash", Workdir: workspace}}, false, false, execCaptureEnv{directory: workspace})
	if !ok || observation == nil {
		t.Fatal("missing source capture")
	}
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := broker.subscribe()
	registry := &execWindowRegistry{tracker: hub.execTrackHub}
	registry.open(&execWindow{ref: "edit", thread: "main", turn: "turn", roots: []string{workspace}})
	defer registry.close("edit")
	registry.preview("edit", *observation, broker, workspace, "main", "/root", nil)
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	writeTestFile(t, path, "package p; var A=1\n")
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p diffview.Preview) bool { return p.Complete })
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+package p; var A=1") || !registry.find("edit").closed.IsZero() {
		t.Fatalf("stream completion changed content or host lifecycle: %+v", preview)
	}
}

func TestExecTrackPreviewDoesNotAdoptOlderInvocation(t *testing.T) {
	t.Parallel()
	for _, lateReport := range []bool{false, true} {
		t.Run(fmt.Sprintf("late_report_%v", lateReport), func(t *testing.T) {
			u, hub := newTrackedAppServerUI(t)
			script := "cat > source.go <<'EOF'\nafter\nEOF\ngo test ./..."
			oldItem := map[string]any{"id": "old", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": oldItem})
			reportOld := func() {
				report := dialExecTrackReport(t, hub, script)
				report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
				awaitMain(t, u, "Running go test ./...")
			}
			if !lateReport {
				reportOld()
			}
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.go")
			writeTestFile(t, path, "before\n")
			observation, ok := captureExecObservation([]execCommandInput{{Command: script, Shell: "bash", Workdir: workspace}}, false, false, execCaptureEnv{previewOnly: true, directory: workspace})
			if !ok || observation == nil {
				t.Fatal("missing capture")
			}
			auto, stop := newAutoLiveDiff(t.Context())
			defer stop()
			auto.enabled.Store(true)
			auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := auto.events.subscribe()
			proxy := &mekugiProxy{execTrack: hub.execTrackHub, execWindows: &execWindowRegistry{tracker: hub.execTrackHub}, autoLiveDiff: auto}
			transform := &mekugiResponseTransform{proxy: proxy, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
			transform.openExecWindow("new", observation, nil)
			defer proxy.execWindows.close("new")
			if lateReport {
				reportOld()
			}
			// An independent file change makes the first polling frame visible.
			// It must remain provisional, not settle from the older invocation.
			writeTestFile(t, path, "intermediate\n")
			waitPreview := func(complete bool) {
				t.Helper()
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				for {
					select {
					case <-sub.previewReady:
						for _, event := range auto.events.takePreviews(sub) {
							if event.Preview == nil || len(event.Preview.Files) == 0 {
								continue
							}
							if !complete && event.Preview.Complete {
								t.Fatal("older invocation completed the new preview")
							}
							if event.Preview.Complete == complete {
								return
							}
						}
					case <-timer.C:
						t.Fatalf("missing preview complete=%v", complete)
					}
				}
			}
			waitPreview(false)
			newItem := map[string]any{"id": "new", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": newItem})
			report := dialExecTrackReport(t, hub, script)
			writeTestFile(t, path, "after\n")
			report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
			waitPreview(true)
		})
	}
}

func TestExecTrackDeclinesAmbiguousHostIdentity(t *testing.T) {
	t.Parallel()
	const script = "sed -i 's/a/b/' a; go test ./..."
	hub := &execTrackHub{changed: make(chan struct{}), tracks: make(map[[3]string]*execTrack)}
	for _, id := range []string{"one", "two"} {
		hub.start([3]string{"main", "turn", id}, "/bin/bash -lc "+quoteShellWord(script))
	}
	for range 2 {
		if _, _, ok := hub.claim(t.Context(), segmenttransport.Message{Thread: "main", Script: script}); ok {
			t.Fatal("ambiguous identical scripts borrowed another host invocation's identity")
		}
	}
	if len(hub.tracks) != 0 || len(hub.started) != 2 {
		t.Fatal("declining observation changed host command ownership")
	}
}

func TestExecTrackSiblingWindowsCannotShareFutureReport(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	workspace := t.TempDir()
	auto, stop := newAutoLiveDiff(t.Context())
	defer stop()
	auto.enabled.Store(true)
	auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := auto.events.subscribe()
	proxy := &mekugiProxy{execTrack: hub.execTrackHub, execWindows: &execWindowRegistry{tracker: hub.execTrackHub}, autoLiveDiff: auto}
	transform := &mekugiResponseTransform{proxy: proxy, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
	script := "cat > source.go <<'EOF'\nafter\nEOF\ngo test ./..."
	for _, id := range []string{"one", "two"} {
		directory := filepath.Join(workspace, id)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(directory, "source.go"), "before\n")
		observation, ok := captureExecObservation([]execCommandInput{{Command: script, Shell: "bash", Workdir: directory}}, false, false, execCaptureEnv{previewOnly: true, directory: workspace})
		if !ok || observation == nil {
			t.Fatal("missing capture")
		}
		transform.openExecWindow(id, observation, nil)
		defer proxy.execWindows.close(id)
	}
	item := map[string]any{"id": "one", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	awaitMain(t, u, "Running go test ./...")
	// The first call edited one file; an unrelated change makes the sibling
	// watch visible too. Neither can acquire completion from this report.
	writeTestFile(t, filepath.Join(workspace, "one", "source.go"), "after\n")
	writeTestFile(t, filepath.Join(workspace, "two", "source.go"), "unrelated\n")
	seen := make(map[string]bool)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for len(seen) != 2 {
		select {
		case <-sub.previewReady:
			for _, event := range auto.events.takePreviews(sub) {
				if event.Preview == nil || len(event.Preview.Files) == 0 {
					continue
				}
				if event.Preview.Complete {
					t.Fatalf("sibling window borrowed completion: %s", event.Preview.ID)
				}
				seen[event.Preview.ID] = true
			}
		case <-timer.C:
			t.Fatal("sibling preview retired before its own command")
		}
	}
}
