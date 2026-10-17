package router

import (
	"bufio"
	"context"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

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

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

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
			previewProxy := newManagedMekugiProxy(t)
			var snapshots *changecapture.Workspaces
			if opaque {
				attachTestReplayStore(t, previewProxy)
				snapshots = previewProxy.snapshots
			}
			observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: script, Workdir: workspace, Shell: "bash"}}, false, false, changecapture.Options{PreviewOnly: !opaque, Directory: workspace, Workspaces: snapshots})
			if !ok || observation == nil {
				t.Fatal("missing observation")
			}
			if opaque {
				if observation.Tree == "" {
					t.Fatal("workspace baseline unavailable")
				}
				if files, _ := previewmodel.ProjectCaptured(t.Context(), *observation); len(files) != 0 {
					t.Fatal("regression must bypass source prediction")
				}
			} else if len(observation.Files) == 0 {
				t.Fatal("missing edit capture")
			}

			if strings.Contains(edit, "git --version") {
				var err error
				script, err = shellinstrumentation.RewriteCommands(script, "/private/mekugi-exec", filepath.Join(shell.root, shellinstrumentation.VCSDirectory), "", "edit")
				if err != nil {
					t.Fatal(err)
				}
			}
			if delayedUI {
				script = shellinstrumentation.ShScript(filepath.Join(shell.root, "exec-track.sh"), script)
			}
			u := newAppServerSessionTestUI(t, workspace)
			u.execTrack = shell.hub.Observer
			shell.hub.ui = u

			defer terminalTest(u).Diff.Close()
			u.attached.presentation.Status.Text = "Working"
			item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			broker := newLivePreview(ctx)
			setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := testReview(broker).Subscribe()
			registry := &executionobservation.Windows{Reports: shell.hub.Observer}
			previewProxy.execWindows, previewProxy.review = registry, testReview(broker)
			startObservedExecTest(t, previewProxy, workspace, "main", "turn", "edit", observedCommandsSource(t, observation.Commands))
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
				u.preview(previewmodel.Preview{ID: "running:edit", Workspace: workspace, Thread: "main", Caller: "/root", Status: previewmodel.PreviewRunning, Input: "observing edit"}, false)
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
				if !shell.hub.Tracking([3]string{"main", "turn", "edit"}) {
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
			var completed *previewmodel.Preview
			streamDone := opaque
			for completed == nil || !streamDone {
				select {
				case <-sub.Ready():
					for _, event := range testReview(broker).TakePreviews(sub) {
						if event.Preview != nil {
							u.preview(*event.Preview, false)
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
				for _, block := range u.testActivity(u.shell.Transcript).blocks(0) {
					if _, _, _, _, ok := activityui.EditStat(block.Label); ok {
						rows = append(rows, u.shell.Transcript.Painter.Block(block, 120)...)
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
			terminalTest(u).Live.Tick(time.Now().Add(previewview.CompletionHold + time.Second))
			if len(terminalTest(u).Live.Order) != 0 || u.attached.presentation.Status.Text != "Working" {
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
			finishPacing(u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents))
			if err := u.testPaint(slave, 140, 40); err != nil {
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
	if strings.Contains(shown, "requested") || !strings.Contains(shown, "via sed · ran") || u.attached.session.Commands()[[3]string{"main", "turn", "edit"}] == nil {
		t.Fatalf("segment completion changed host lifecycle:\n%s", shown)
	}
	// A zero exit also describes a no-op. Without file evidence, show the
	// command outcome but do not invent a confirmed edit or applied counts.
	for _, block := range u.testActivity(u.shell.Transcript).blocks(0) {
		if block.Verb == "Edit" {
			if _, _, _, _, counts := activityui.EditStat(block.Label); counts || block.GroupHeader == "Edited" || block.EditOutcome != "ran" {
				t.Fatalf("command completion confirmed a file change: %+v", block)
			}
		}
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
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(1)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 2})
	awaitMain(t, u, "via sed · skipped")
	shown := awaitMain(t, u, "Running")
	if !strings.Contains(shown, "Running go test") || strings.Contains(shown, "requested") || strings.Contains(shown, "skipped · skipped") {
		t.Fatalf("skipped edit waited for host exit:\n%s", shown)
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
		if len(segment.Tail) != 1 || segment.Tail[0] != fmt.Sprint(i) {
			t.Fatalf("segment %d borrowed or lost output: %q", i, segment.Tail)
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
	commands, dynamic := commandanalysis.LiteralExecCommands(source, workspace, "bash")
	observation, ok := changecapture.Capture(t.Context(), commands, dynamic, true, changecapture.Options{PreviewOnly: true, Directory: workspace})
	if !ok || observation == nil || len(commands) != 2 || len(previewmodel.Expected(t.Context(), *observation)) != 0 {
		t.Fatal("fixture must capture two sequential edits in one exec cell")
	}
	auto := newLivePreview(t.Context())
	defer auto.Close()
	auto.Enable()
	setLiveScope(auto, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := testReview(auto).Subscribe()
	proxy := &observationFixture{execWindows: &executionobservation.Windows{Reports: hub.Observer}, review: testReview(auto)}
	startObservedExecTest(t, proxy, workspace, "main", "turn", "cell", source)
	firstItem := map[string]any{"id": "first", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(first), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": firstItem})
	firstReport := dialExecTrackReport(t, hub, first)
	writeTestFile(t, path, "middle\n")
	firstReport.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1}, segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed})
	firstReport.conn.Close()
	firstItem["status"], firstItem["exitCode"] = "completed", 0
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": firstItem})
	deadline := time.Now().Add(3 * time.Second)
	for hub.Tracking([3]string{"main", "turn", "first"}) {
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
	var previews []string
	for {
		select {
		case <-sub.Ready():
			for _, event := range testReview(auto).TakePreviews(sub) {
				if event.Preview != nil {
					previews = append(previews, fmt.Sprintf("id=%s complete=%v receipt=%q files=%d footer=%s", event.Preview.ID, event.Preview.Complete, event.Preview.EditReceipt, len(event.Preview.Files), event.Preview.Footer))
				}
				if event.Preview == nil || !event.Preview.Complete || event.Preview.EditReceipt != "" {
					continue
				}
				if len(event.Preview.Files) != 1 || !strings.Contains(event.Preview.Files[0].Diff, "-before") || !strings.Contains(event.Preview.Files[0].Diff, "+after") {
					t.Fatalf("grouped preview lost the captured baseline: %+v", event.Preview)
				}
				if !hub.Tracking([3]string{"main", "turn", "second"}) {
					t.Fatal("preview completion ended the host command or cell")
				}
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("grouped preview waited for the second command's tests: %v", previews)
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
			commands, dynamic := commandanalysis.LiteralExecCommands(source, workspace, shell)
			observation, ok := changecapture.Capture(t.Context(), commands, dynamic, true, changecapture.Options{PreviewOnly: true, Directory: workspace})
			if !ok || observation == nil || len(commands) != 2 {
				t.Fatal("missing grouped edit observation")
			}
			auto := newLivePreview(t.Context())
			defer auto.Close()
			auto.Enable()
			setLiveScope(auto, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := testReview(auto).Subscribe()
			proxy := &observationFixture{execWindows: &executionobservation.Windows{Reports: hub.Observer}, review: testReview(auto)}
			startObservedExecTest(t, proxy, workspace, "main", "turn", "cell", source)
			item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/tmp/vcs-guard/" + shell + " -c " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			cmd := exec.CommandContext(t.Context(), execTrackShellExecutable(t, shell), "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+execTrackPath(), "BASH_ENV=")
			cmd.Dir = workspace
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("edit failed: %v: %s", err, out)
			}
			if shell == "sh" {
				preview := waitExecScopePreview(t, auto, func(p previewmodel.Preview) bool { return len(p.Files) > 0 && !p.Complete })
				u.preview(preview, false)
				revealNativeDock(terminalTest(u))
				if terminalTest(u).Live.Live() == 0 {
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
				case <-sub.Ready():
					for _, event := range testReview(auto).TakePreviews(sub) {
						if event.Preview != nil && event.Preview.Complete {
							wantFiles := 1
							if len(event.Preview.Files) != wantFiles || event.Preview.Footer != "observed edit" {
								t.Fatalf("completion lost actual edit: %+v", event.Preview)
							}
							if u.attached.session.Commands()[[3]string{"main", "turn", "test"}] == nil {
								t.Fatal("edit completion ended enclosing cell or sibling test")
							}
							u.preview(*event.Preview, false)
							streamDone = streamDone || event.Preview.EditReceipt == ""
							receiptDone = receiptDone || event.Preview.EditReceipt != ""
							if !streamDone || !receiptDone {
								continue
							}
							terminalTest(u).Live.Tick(time.Now().Add(previewview.CompletionHold + time.Millisecond))
							screen := vt.NewEmulator(100, 30)
							defer screen.Close()
							if err := u.testPaint(screen, 100, 30); err != nil {
								t.Fatal(err)
							}
							if len(terminalTest(u).Live.Order) != 0 || strings.Contains(screen.String(), "LIVE ·") {
								t.Fatal("completed native command retained the live dock")
							}
							for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
								for _, entry := range view.Entries {
									if entry.Source.Native != nil && entry.Source.Native.Item == "edit" {
										if !terminalTest(u).openActivityEdit(view.bind(terminalTest(u)), entry.Source.Seq, "source.go") || !strings.Contains(terminalTest(u).Dialog.Pages[0].Code, "+package p; var B=2") {
											t.Fatal("completed shell edit waited for the next cell or sibling exit to become clickable")
										}
										terminalTest(u).outputKey("\x1b")
									}
								}
							}
							return
						}
					}
				case <-timer.C:
					t.Fatalf("nested edit preview waited for later tests in the same cell: source=%v receipt=%v current=%+v", streamDone, receiptDone, auto.Current())
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
	observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: script, Shell: "bash", Workdir: workspace}}, false, false, changecapture.Options{Directory: workspace})
	if !ok || observation == nil {
		t.Fatal("missing source capture")
	}
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := testReview(broker).Subscribe()
	registry := &executionobservation.Windows{Reports: hub.Observer}
	startObservedExecTest(t, &observationFixture{execWindows: registry, review: testReview(broker)}, workspace, "main", "turn", "edit", observedCommandsSource(t, observation.Commands))
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	writeTestFile(t, path, "package p; var A=1\n")
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p previewmodel.Preview) bool { return p.Complete })
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+package p; var A=1") {
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
			observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: script, Shell: "bash", Workdir: workspace}}, false, false, changecapture.Options{PreviewOnly: true, Directory: workspace})
			if !ok || observation == nil {
				t.Fatal("missing capture")
			}
			auto := newLivePreview(t.Context())
			defer auto.Close()
			auto.Enable()
			setLiveScope(auto, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := testReview(auto).Subscribe()
			proxy := &observationFixture{execWindows: &executionobservation.Windows{Reports: hub.Observer}, review: testReview(auto)}
			startObservedExecTest(t, proxy, workspace, "main", "turn", "new", observedCommandsSource(t, observation.Commands))
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
					case <-sub.Ready():
						for _, event := range testReview(auto).TakePreviews(sub) {
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

func TestExecTrackSiblingWindowsCannotShareFutureReport(t *testing.T) {
	t.Parallel()
	u, hub := newTrackedAppServerUI(t)
	workspace := t.TempDir()
	auto := newLivePreview(t.Context())
	defer auto.Close()
	auto.Enable()
	setLiveScope(auto, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := testReview(auto).Subscribe()
	proxy := &observationFixture{execWindows: &executionobservation.Windows{Reports: hub.Observer}, review: testReview(auto)}
	script := "cat > source.go <<'EOF'\nafter\nEOF\ngo test ./..."
	for _, id := range []string{"one", "two"} {
		directory := filepath.Join(workspace, id)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(directory, "source.go"), "before\n")
		observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: script, Shell: "bash", Workdir: directory}}, false, false, changecapture.Options{PreviewOnly: true, Directory: workspace})
		if !ok || observation == nil {
			t.Fatal("missing capture")
		}
		startObservedExecTest(t, proxy, workspace, "main", "turn", id, observedCommandsSource(t, observation.Commands))
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
		case <-sub.Ready():
			for _, event := range testReview(auto).TakePreviews(sub) {
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
