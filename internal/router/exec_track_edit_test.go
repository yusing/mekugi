package router

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestExecTrackEditCompletesBeforeFollowingCommand(t *testing.T) {
	for _, edit := range []string{"gofmt -w source.go", "cat > source.go <<'EOF'\nafter\nEOF", "sed -i 's/before/after/' source.go", "cp image.png source.go"} {
		t.Run(filepath.Base(strings.Fields(edit)[0]), func(t *testing.T) {
			shell := newExecTrackShell(t)
			workspace := t.TempDir()
			before, after, completedText := "before\n", "+after", "completed"
			if filepath.Base(strings.Fields(edit)[0]) == "gofmt" {
				before, after, completedText = "package p; var A=1\n", "+var A = 1", "Ran"
			}
			binaryCopy := strings.HasPrefix(edit, "cp ")
			if binaryCopy {
				writeTestFile(t, filepath.Join(workspace, "image.png"), "\x89PNG\r\n\x1a\n\x00binary image")
				completedText = "Ran"
			}
			writeTestFile(t, filepath.Join(workspace, "source.go"), before)
			// The following command cannot exit until the test has inspected a
			// real terminal frame and the preview's completed filesystem view.
			script := edit + "\nprintf 'test running\\n'; read -r answer"
			observation, ok := captureExecObservation([]execCommandInput{{Command: script, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
			if !ok || observation == nil || len(observation.Files) == 0 {
				t.Fatal("missing edit capture")
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
			registry.preview("edit", *observation, broker, workspace, "main", "/root")
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			// Seed the same running dock before execution so completion must
			// actively retire it, rather than simply avoid opening a new card.
			u.shell.preview(diffview.Preview{ID: "running:edit", Workspace: workspace, Thread: "main", Caller: "/root", Status: diffview.PreviewRunning, Input: "observing edit"})
			cmd := exec.CommandContext(ctx, "bash", "-lc", script)
			cmd.Dir, cmd.Env = workspace, shell.env
			if strings.HasPrefix(edit, "gofmt") {
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
			awaitMain(t, u, completedText)
			var completed *diffview.Preview
			for completed == nil {
				select {
				case <-sub.previewReady:
					for _, event := range broker.takePreviews(sub) {
						if event.Preview != nil {
							u.shell.preview(*event.Preview)
							if event.Preview.Complete {
								completed = event.Preview
							}
						}
					}
				case <-ctx.Done():
					t.Fatal("edit preview waited for the following command")
				}
			}
			if len(completed.Files) != 1 || completed.Footer != "observed edit" {
				t.Fatalf("completion lacks actual observed edit: %+v", completed)
			}
			if binaryCopy && !completed.Files[0].Binary || !binaryCopy && !strings.Contains(completed.Files[0].Diff, after) {
				t.Fatalf("completion lost copied content: %+v", completed.Files)
			}
			awaitMain(t, u, "Running")
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
				if strings.Contains(shown, "requested") || strings.Contains(shown, "LIVE ·") || !strings.Contains(shown, completedText) || !strings.Contains(shown, "Running") {
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

func TestExecPreviewTrackIdentityAndLifecycle(t *testing.T) {
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
			tracked, settled, _ := tracking.state()
			if tracked != tc.wantTracked || settled != tc.wantSettled {
				t.Fatalf("state = %v, %v", tracked, settled)
			}
		})
	}
}

func TestAppServerTrackedEditFailureAndSkip(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	script := "sed -i 's/a/b/' missing && sed -i 's/b/c/' skipped"
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(2)}, execsegment.Message{Type: execsegment.Done, Code: new(2)})
	report.conn.Close()
	item["status"], item["exitCode"] = "failed", 2
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	shown := awaitMain(t, u, "via sed · skipped")
	if strings.Contains(shown, "requested") || strings.Contains(shown, "completed") || !strings.Contains(shown, "failed") || !strings.Contains(shown, "exit 2") {
		t.Fatalf("failed/skipped edits mislabeled:\n%s", shown)
	}
}

func TestAppServerTrackedSkippedEditBeforeHostExit(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	script := "false && sed -i 's/a/b/' skipped; go test ./..."
	tracking := newExecPreviewTrack(hub, "main", "turn", []execCommandInput{{Command: script}})
	defer tracking.close()
	item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(1)}, execsegment.Message{Type: execsegment.Begin, Index: 2})
	awaitMain(t, u, "via sed · skipped")
	shown := awaitMain(t, u, "Running")
	if !strings.Contains(shown, "Running go test") || strings.Contains(shown, "requested") || strings.Contains(shown, "skipped · skipped") {
		t.Fatalf("skipped edit waited for host exit:\n%s", shown)
	}
	if tracked, settled, _ := tracking.state(); !tracked || !settled {
		t.Fatalf("skipped edit kept its preview live: tracked=%v settled=%v", tracked, settled)
	}
}

func TestExecTrackRapidSegmentsKeepOwnOutput(t *testing.T) {
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
	u, hub := newTrackedAppServerUI(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "before\n")
	first := "sed -i 's/before/middle/' source.go; printf first"
	second := "sed -i 's/middle/after/' source.go; go test ./..."
	source := fmt.Sprintf("await tools.exec_command({cmd:%q,workdir:%q}); await tools.exec_command({cmd:%q,workdir:%q});", first, workspace, second, workspace)
	commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
	observation, ok := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: workspace})
	if !ok || observation == nil || len(commands) != 2 || len(execPreviewExpected(t.Context(), *observation)) != 0 {
		t.Fatal("fixture must capture two unprojectable edits in one Code Mode cell")
	}
	auto, stop := newAutoLiveDiff(t.Context(), "")
	defer stop()
	auto.enabled.Store(true)
	auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := auto.events.subscribe()
	proxy := &mekugiProxy{execTrack: hub, execWindows: &execWindowRegistry{tracker: hub}, autoLiveDiff: auto}
	transform := &mekugiResponseTransform{proxy: proxy, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
	transform.openExecWindow("cell", observation, nil)
	defer proxy.execWindows.close("cell")
	firstItem := map[string]any{"id": "first", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(first), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": firstItem})
	firstReport := dialExecTrackReport(t, hub, first)
	writeTestFile(t, path, "middle\n")
	firstReport.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)}, execsegment.Message{Type: execsegment.Begin, Index: 1}, execsegment.Message{Type: execsegment.End, Index: 1, Code: new(0)}, execsegment.Message{Type: execsegment.Done, Code: new(0)})
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
	secondReport.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)}, execsegment.Message{Type: execsegment.Begin, Index: 1})
	for {
		select {
		case <-sub.previewReady:
			for _, event := range auto.events.takePreviews(sub) {
				if event.Preview == nil || !event.Preview.Complete {
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
	for _, script := range []string{"gofmt -w source.go", "sed -i 's/A/B/' source.go", "cp", "install"} {
		t.Run(strings.Fields(script)[0], func(t *testing.T) {
			u, hub := newTrackedAppServerUI(t)
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.go")
			binaryCopy := script == "cp" || script == "install"
			if binaryCopy {
				// The session's stuck card copied a PNG outside its workspace.
				// Binary copies have no projected text edit to end the watcher.
				original := filepath.Join(t.TempDir(), "image.png")
				path = filepath.Join(t.TempDir(), "stall-question.png")
				writeTestFile(t, original, "\x89PNG\r\n\x1a\n\x00binary image")
				script += " " + quoteShellWord(original) + " " + quoteShellWord(path)
			} else {
				writeTestFile(t, path, "package p; var A=1\n")
			}
			// Two nested calls in one cell. The edit is a single command, so
			// there is no shell segment report. The sibling test stays pending.
			source := fmt.Sprintf("await tools.exec_command({cmd:%q,workdir:%q}); await tools.exec_command({cmd:'go test ./...',workdir:%q});", script, workspace, workspace)
			commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
			observation, ok := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: workspace})
			if !ok || observation == nil || len(commands) != 2 {
				t.Fatal("missing grouped edit observation")
			}
			auto, stop := newAutoLiveDiff(t.Context(), "")
			defer stop()
			auto.enabled.Store(true)
			auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := auto.events.subscribe()
			proxy := &mekugiProxy{execTrack: hub, execWindows: &execWindowRegistry{tracker: hub}, autoLiveDiff: auto}
			transform := &mekugiResponseTransform{proxy: proxy, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
			transform.openExecWindow("cell", observation, nil)
			defer proxy.execWindows.close("cell")
			item := map[string]any{"id": "edit", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			cmd := exec.CommandContext(t.Context(), "env", "-u", "BASH_ENV", "bash", "-c", script)
			cmd.Dir = workspace
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("edit failed: %v: %s", err, out)
			}
			item["status"], item["exitCode"] = "completed", 0
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "test", "type": "commandExecution", "command": "/bin/bash -lc 'go test ./...'", "status": "inProgress"}})
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-sub.previewReady:
					for _, event := range auto.events.takePreviews(sub) {
						if event.Preview != nil && event.Preview.Complete {
							if len(event.Preview.Files) != 1 || event.Preview.Footer != "observed edit" {
								t.Fatalf("completion lost actual edit: %+v", event.Preview)
							}
							if binaryCopy && (!event.Preview.Files[0].Binary || event.Preview.Files[0].AfterPath != path) {
								t.Fatalf("completion lost binary copy: %+v", event.Preview.Files)
							}
							if !proxy.execWindows.find("cell").closed.IsZero() || u.session.commands[[3]string{"main", "turn", "test"}] == nil {
								t.Fatal("edit completion ended enclosing cell or sibling test")
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

func TestExecTrackPreviewDoesNotAdoptOlderInvocation(t *testing.T) {
	for _, lateReport := range []bool{false, true} {
		t.Run(fmt.Sprintf("late_report_%v", lateReport), func(t *testing.T) {
			u, hub := newTrackedAppServerUI(t)
			script := "sed -i 's/before/after/' source.go; go test ./..."
			oldItem := map[string]any{"id": "old", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": oldItem})
			reportOld := func() {
				report := dialExecTrackReport(t, hub, script)
				report.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)}, execsegment.Message{Type: execsegment.Begin, Index: 1})
				awaitMain(t, u, "completed")
			}
			if !lateReport {
				reportOld()
			}
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.go")
			writeTestFile(t, path, "before\n")
			observation, ok := captureExecObservation([]execCommandInput{{Command: script, Shell: "bash", Workdir: workspace}}, false, false, execCaptureEnv{directory: workspace})
			if !ok || observation == nil {
				t.Fatal("missing capture")
			}
			auto, stop := newAutoLiveDiff(t.Context(), "")
			defer stop()
			auto.enabled.Store(true)
			auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := auto.events.subscribe()
			proxy := &mekugiProxy{execTrack: hub, execWindows: &execWindowRegistry{tracker: hub}, autoLiveDiff: auto}
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
			report.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)}, execsegment.Message{Type: execsegment.Begin, Index: 1})
			waitPreview(true)
		})
	}
}

func TestExecTrackDeclinesAmbiguousHostIdentity(t *testing.T) {
	const script = "sed -i 's/a/b/' a; go test ./..."
	hub := &execTrackHub{changed: make(chan struct{}), tracks: make(map[[3]string]*execTrack)}
	for _, id := range []string{"one", "two"} {
		hub.start([3]string{"main", "turn", id}, "/bin/bash -lc "+quoteShellWord(script))
	}
	for range 2 {
		if _, _, ok := hub.claim(t.Context(), execsegment.Message{Thread: "main", Script: script}); ok {
			t.Fatal("ambiguous identical scripts borrowed another host invocation's identity")
		}
	}
	if len(hub.tracks) != 0 || len(hub.started) != 2 {
		t.Fatal("declining observation changed host command ownership")
	}
}

func TestExecTrackSiblingWindowsCannotShareFutureReport(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	workspace := t.TempDir()
	auto, stop := newAutoLiveDiff(t.Context(), "")
	defer stop()
	auto.enabled.Store(true)
	auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := auto.events.subscribe()
	proxy := &mekugiProxy{execTrack: hub, execWindows: &execWindowRegistry{tracker: hub}, autoLiveDiff: auto}
	transform := &mekugiResponseTransform{proxy: proxy, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
	script := "sed -i 's/before/after/' source.go; go test ./..."
	for _, id := range []string{"one", "two"} {
		directory := filepath.Join(workspace, id)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(directory, "source.go"), "before\n")
		observation, ok := captureExecObservation([]execCommandInput{{Command: script, Shell: "bash", Workdir: directory}}, false, false, execCaptureEnv{directory: workspace})
		if !ok || observation == nil {
			t.Fatal("missing capture")
		}
		transform.openExecWindow(id, observation, nil)
		defer proxy.execWindows.close(id)
	}
	item := map[string]any{"id": "one", "type": "commandExecution", "command": "/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(execsegment.Message{Type: execsegment.Begin, Index: 0}, execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)}, execsegment.Message{Type: execsegment.Begin, Index: 1})
	awaitMain(t, u, "completed")
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
