package launch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestUISnapshotCreatedScriptCapture(t *testing.T) {
	for _, together := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-call=%v", together), func(t *testing.T) {
			if _, err := exec.LookPath("python3"); err != nil {
				t.Skip("Python runtime unavailable")
			}
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			trace := newNativeTraceFixture(t)
			proxy.nativeTrace = nativetrace.NewWithNotice(trace.root, nil)
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			runExecVCSTestGit(t, workspace, "init", "-q")
			writeTestFile(t, filepath.Join(workspace, ".gitignore"), "target.txt\n")
			target := filepath.Join(workspace, "target.txt")
			writeTestFile(t, target, "before\n")
			script := "kernel.py"
			create := "cat > " + commandargs.QuoteArgument(script) + " <<'PY'\nfrom pathlib import Path\np = Path('target.txt')\np.write_text(p.read_text() + 'after\\n')\nPY"
			run := "python3 " + commandargs.QuoteArgument(script)
			commands := []string{create, run}
			if together {
				commands = []string{create + "\n" + run}
			}
			ui := newAppServerSessionTestUI(t, workspace)
			ui.Thread = "thread-1"
			ui.startSession(ui.Thread, workspace)
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			ui.clock = func() time.Time { return now }
			ui.shell.Transcript.Clock, ui.shell.Agents.Clock = ui.clock, ui.clock
			ui.audience = proxy.activity
			ui.audience.Attach(ui.Thread)
			var items []binding.ThreadItem
			var input []any
			var changeID string
			for i, command := range commands {
				cell, host := fmt.Sprintf("cell-%d", i), fmt.Sprintf("host-%d", i)
				arguments := string(responses.MustMarshalJSON(map[string]any{"cmd": command, "workdir": workspace, "shell": "/bin/bash"}))
				source := "text(await tools.exec_command(" + arguments + "));"
				call := map[string]any{"type": "custom_tool_call", "id": cell + "-item", "call_id": cell, "name": "exec", "input": source, "status": "completed"}
				if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{call}})); err != nil {
					t.Fatal(err)
				}
				retained, found, err := testProxyStore(proxy).lookup(t.Context(), workspace, cell)
				if err != nil || !found || retained.CarrierPayload != source {
					t.Fatal("stock source was not preserved")
				}
				trace.start(ui.Thread, "runtime-"+cell, cell, source)
				trace.tool(ui.Thread, "runtime-"+cell, host, "exec_command", arguments)
				cmd := exec.Command(execTrackShellExecutable(t, "bash"), "--noprofile", "--norc", "-c", command)
				cmd.Dir = workspace
				cmd.Env = slices.DeleteFunc(os.Environ(), func(value string) bool {
					return strings.HasPrefix(value, "BASH_ENV=") || strings.HasPrefix(value, "MEKUGI_EXEC_TRACK=")
				})
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("host execution: %v: %s", err, output)
				}
				trace.result(ui.Thread, host, "completed", map[string]any{"exit_code": 0})
				trace.end(ui.Thread, "runtime-"+cell)
				input = append(input, call, map[string]any{"type": "custom_tool_call_output", "call_id": cell, "output": "Script completed\nWall time 0.1 seconds\nOutput:\n"})
				request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, input), "tools": mustTestJSON(t, testExecResponsesTools())}}
				transform, err = proxy.prepareRequest(t.Context(), &request, "session-1", ui.Thread, codexresponses.TurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
				if err != nil {
					t.Fatal(err)
				}
				defer transform.Close()
				record, found, err := testProxyStore(proxy).lookup(t.Context(), workspace, legacycall.ExecCallID(cell, true))
				if err != nil || !found || record.ChangeID == "" || len(record.HostResults) != 1 || record.HostResults[0].CallID != host {
					t.Fatalf("captured host identity: found=%v err=%v record=%+v", found, err, record)
				}
				changeID = record.ChangeID
				if i == len(commands)-1 {
					foundTarget := false
					for _, file := range record.ReviewFiles {
						if file.AfterPath == target && strings.Contains(file.Diff, "\n+after\n") && file.Incomplete == "" {
							foundTarget = true
						}
					}
					if !foundTarget {
						t.Fatalf("script target was not captured: %+v; observation=%+v outcome=%+v", record.ReviewFiles, record.ExecObservation, record.ExecOutcome)
					}
				}
				item := binding.ThreadItem{ID: host, Type: "commandExecution", Command: command, ExitCode: new(0)}
				items = append(items, item)
				appServerTestNotify(t, ui, "item/completed", map[string]any{"threadId": ui.Thread, "turnId": "turn", "item": item})
				applyObservedActivity(&ui.attached, ui.shell, &ui.Client, &ui.streams, ui.usage, ui.review, &ui.approvals, &ui.pendingEditReceipts, ui.clock, ui.restoring, ui.audience)
			}
			store, err := openMekugiReplayStore(testProxyStore(proxy).directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.snapshots.Close)
			ctx, release, err := store.beginSession(t.Context(), ui.Thread, "created-script-review")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			text, err := store.changes.Read(ctx, store.scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{changeID}, View: "net", MaxTokens: 4000})
			if err != nil || !strings.Contains(text, "+after") || !strings.Contains(text, "target.txt") {
				t.Fatalf("retained script target: %q, %v", text, err)
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "before\nafter\n" {
				t.Fatalf("observation repeated the script: %q, %v", data, err)
			}
			if len(ui.attached.source.Entries) != len(items) {
				t.Fatalf("receipt duplicated host identities: %+v", ui.attached.source.Entries)
			}
			if err := os.Remove(filepath.Join(workspace, script)); err != nil {
				t.Fatal(err)
			}
			request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, input), "tools": mustTestJSON(t, testExecResponsesTools())}}
			replayed, err := proxy.prepareRequest(t.Context(), &request, "session-1", ui.Thread, codexresponses.TurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer replayed.Close()
			again, found, err := testProxyStore(proxy).lookup(t.Context(), workspace, legacycall.ExecCallID(fmt.Sprintf("cell-%d", len(commands)-1), true))
			if err != nil || !found || again.ChangeID != changeID {
				t.Fatalf("replay replaced captured identity: %+v, %v", again, err)
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "before\nafter\n" {
				t.Fatalf("replay repeated the script: %q, %v", data, err)
			}
			view := ui.testActivity(ui.shell.Transcript)
			view.testPace(now.Add(time.Hour))
			lines := view.RenderFeed(100, 30).Lines
			if rendered := strings.Join(lines, "\n"); !strings.Contains(rendered, "target.txt") || strings.Contains(rendered, "requested") {
				t.Fatalf("confirmed target did not replace intent: %s", rendered)
			}
			// Late segment reports enrich the exact completed host identity.
			for _, entry := range slices.Clone(view.Entries) {
				native := *entry.Source.Native
				parts, ok := shellinstrumentation.Split(native.Command)
				if !ok {
					t.Fatal("fixture command cannot be tracked")
				}
				for _, part := range parts {
					native.Segments = append(native.Segments, sessionpresentation.ActivitySegment{Source: part.Source, Text: execSegmentText(part.Source)})
				}
				native.CapturedEdit = nil
				update := entry.Source
				update.Native = &native
				view.SettleCommandReport(update, true)
			}
			for _, entry := range view.Entries {
				if len(entry.Source.Native.Segments) == 0 {
					t.Fatal("late segment report was not consumed")
				}
			}
			view.testPace(now.Add(time.Hour))
			lines = view.RenderFeed(100, 30).Lines
			if rendered := strings.Join(lines, "\n"); !strings.Contains(rendered, "target.txt") || strings.Contains(rendered, "requested") {
				t.Fatalf("segment refresh restored stale intent: %s", rendered)
			}
			assertNativeUISnapshot(t, fmt.Sprintf("created-script-same-call-%v", together), lines)
			snapshot, err := store.liveDiffSnapshot(ctx, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {ui.Thread: true}}})
			if err != nil {
				t.Fatal(err)
			}
			restored := newAppServerSessionTestUI(t, workspace)
			restored.Thread = ui.Thread
			restored.startSession(ui.Thread, workspace)
			restored.review.Projection.Data = snapshot
			publishFixtureReview(t, restored)
			restored.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: items}})
			restoredView := restored.testActivity(restored.shell.Transcript)
			restoredView.Expansion = 1
			restoredView.testPace(now.Add(time.Hour))
			if rendered := strings.Join(restoredView.RenderFeed(100, 30).Lines, "\n"); !strings.Contains(rendered, "target.txt") || strings.Contains(rendered, "requested") || len(restored.attached.source.Entries) != len(items) {
				t.Fatalf("restored receipt lost exact host edits: %s", rendered)
			}
		})
	}
}
