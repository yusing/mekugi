package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestSupportedSnapshotGeneratorRetainsInitialCreation(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "snapshot-generator")
	pythonName, pythonBinary := interpreterForTest("python3", "python")
	if pythonBinary == "" {
		t.Skip("Python runtime unavailable")
	}
	command := pythonName + " <<'PY'\nfrom pathlib import Path\nout = Path('testdata/snapshots')\nout.mkdir(parents=True, exist_ok=True)\nfixtures = {'new.txt': 'first\\n', 'other.txt': 'body\\n'}\nfor name, body in fixtures.items():\n    (out / name).write_text(body)\nPY"
	observation := observeTestCommand(t, f.workspace, command)
	if _, err := os.Stat(filepath.Join(f.workspace, "testdata")); !os.IsNotExist(err) {
		t.Fatal("capture executed the generator")
	}
	host := exec.Command("bash", "-c", command)
	host.Dir = f.workspace
	if output, err := host.CombinedOutput(); err != nil {
		t.Fatalf("host generator: %v: %s", err, output)
	}
	proxy := &mekugiProxy{replayStore: f.store}
	history := mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: "generator-agent", ExecObservation: observation}
	if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "generate", history: history, output: mustMarshalJSON(map[string]any{"exit_code": 0})}}); err != nil {
		t.Fatal(err)
	}
	record, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("generate", false))
	if err != nil || !found || len(record.ReviewFiles) != 2 || record.ChangeID == "" {
		t.Fatalf("generator capture missing: %+v, %v", record, err)
	}
	for _, file := range record.ReviewFiles {
		if file.BeforePath != "" || file.Incomplete != "" {
			t.Fatalf("generator creation baseline missing: %+v", file)
		}
	}
	net, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{record.ChangeID}, view: "net"})
	if err != nil || !strings.Contains(net, "+first") || !strings.Contains(net, "+body") {
		t.Fatalf("generator net: %q, %v", net, err)
	}
}

func TestPythonTemporaryWriteAndRestoreRetainsIncompleteHistory(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "write-restore")
	pythonName, pythonBinary := interpreterForTest("python3", "python")
	if pythonBinary == "" {
		t.Skip("Python runtime unavailable")
	}
	path := filepath.Join(f.workspace, "target.go")
	current := "package fixture\n// current\n"
	writeTestFile(t, path, current)
	// The screenshot's producer shape, in an isolated fixture. The nested
	// command checks the temporary bytes while finally restores the original.
	command := pythonName + " <<'PY'\nimport pathlib, subprocess\npath = pathlib.Path('target.go')\ncurrent = path.read_text()\nreplacement = current.replace('current', 'baseline')\ntry:\n    path.write_text(replacement)\n    subprocess.run(['python3', '-c', \"from pathlib import Path; assert 'baseline' in Path('target.go').read_text()\"], check=True)\nfinally:\n    path.write_text(current)\nPY"
	observation := observeTestCommand(t, f.workspace, command)
	if !observation.RepeatedPaths {
		t.Fatal("temporary write-and-restore scope not identified")
	}
	before, err := os.ReadFile(path)
	if err != nil || string(before) != current {
		t.Fatal("observation executed the Python script")
	}
	host := exec.Command("bash", "-c", command)
	host.Dir = f.workspace
	if output, err := host.CombinedOutput(); err != nil {
		t.Fatalf("host: %v: %s", err, output)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != current {
		t.Fatal("fixture did not restore its final bytes")
	}
	proxy := &mekugiProxy{replayStore: f.store}
	history := mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread, ExecObservation: observation}
	output := mustMarshalJSON("Wall time: 0.01 seconds\nProcess exited with code 0\nFinal output:\n")
	if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "temporary", history: history, output: output}}); err != nil {
		t.Fatal(err)
	}
	record, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("temporary", false))
	if err != nil || !found || record.ChangeID != "" || len(record.ReviewFiles) != 0 {
		t.Fatalf("attempt vanished or fabricated intermediate diff: %+v, %v", record, err)
	}
	if record.ExecOutcome.Coverage != execCoveragePartial || !strings.Contains(record.ExecOutcome.ScopeReason, "intermediate writes unobserved") {
		t.Fatal("temporary edit uncertainty was not retained in command diagnostics")
	}

}

func TestMChangesSummaryComposesManagedAndDirectEdits(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "summary-composition")
	path := filepath.Join(f.workspace, "file.txt")
	for i, origin := range []string{"gofmt", "", "gofmt"} {
		before, after := "", "one\n"
		if i == 1 {
			before, after = "one\n", "two\nextra\n"
		}
		if i == 2 {
			before, after = "two\nextra\n", "final\n"
		}
		correlation := fmt.Sprint(i)
		id := f.reserve(t, f.thread, correlation)
		file := mekugi.RenderReviewFile(path, path, before, after)
		if i == 0 {
			file = mekugi.RenderReviewFile("", path, before, after)
		}
		file.Origin = origin
		f.publish(t, id, correlation, correlation, mekugiHistory{ReviewFiles: []mekugi.ReviewFile{file}})
	}
	summary, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, mine: true, view: "summary"})
	if err != nil || summary != "M\t2\t1\tfile.txt\n" {
		t.Fatalf("summary summed attempts or split ownership: %q, %v", summary, err)
	}
}

func TestMChangesReadContextDistinguishesMissingEmptyAndPathScope(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "read-context")
	id := f.reserve(t, "worker", "worker-effect")
	f.publish(t, id, "worker-effect", "worker-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", "old\n", "new\n")}})
	output, stderr, status := f.run(t, "mchanges "+id+" --net")
	if status != 0 || stderr != "" || !strings.Contains(output, "+new") {
		t.Fatalf("explicit worker capture unreadable: %q, %q, %d", output, stderr, status)
	}
	output, stderr, status = f.run(t, "mchanges --mine --net")
	if status != 0 || stderr != "" || !strings.Contains(output, "no captures selected") || !strings.Contains(output, f.workspace) {
		t.Fatalf("own-thread empty selection ambiguous: %q, %q, %d", output, stderr, status)
	}
	output, stderr, status = f.run(t, "mchanges "+id+" --net -- missing.txt")
	if status != 0 || stderr != "" || !strings.Contains(output, "no captured files match") {
		t.Fatalf("path mismatch became empty net: %q, %q, %d", output, stderr, status)
	}
	output, stderr, status = f.run(t, "mchanges "+id+" --net --workspace "+t.TempDir())
	if status == 0 || !strings.Contains(stderr, "check --workspace") || strings.Contains(output, "no net changes") {
		t.Fatalf("unavailable selection became empty net: %q, %q, %d", output, stderr, status)
	}
	gap := f.reserve(t, f.thread, "gap")
	f.publish(t, gap, "gap", "gap-call", mekugiHistory{ExecOutcome: &execOutcome{Coverage: execCoveragePartial, ScopeReason: "host inputs missing"}})
	output, stderr, status = f.run(t, "mchanges "+gap+" --net")
	if status == 0 || !strings.Contains(stderr, "partial captured effects") || strings.Contains(output, "no net changes") {
		t.Fatalf("missing evidence became empty successful net: %q, %q, %d", output, stderr, status)
	}
	output, stderr, status = f.run(t, "mchanges --list "+gap)
	if status != 0 || stderr != "" || !strings.Contains(output, "?") {
		t.Fatalf("gap ID disappeared from inventory: %q, %q, %d", output, stderr, status)
	}
}

func TestResolvedRepeatedStockCallsRetainEveryOutcome(t *testing.T) {
	t.Parallel()
	for _, literalLoop := range []bool{false, true} {
		t.Run(fmt.Sprintf("literalLoop=%v", literalLoop), func(t *testing.T) {
			f := newMChangesSliceFixture(t, "repeated-stock")
			patch := "*** Begin Patch\n*** Add File: repeated-patch.txt\n+after\n*** End Patch\n"
			command := "printf after > repeated-command.txt"
			source := "await tools.apply_patch(" + string(mustMarshalJSON(patch)) + "); const patch = buildPatch(); await tools.apply_patch(patch); " +
				"await tools.exec_command({cmd: " + string(mustMarshalJSON(command)) + "}); const args = getCommand(); await tools.exec_command(args);"
			if literalLoop {
				source = "for (let i = 0; i < 2; i++) await tools.apply_patch(" + string(mustMarshalJSON(patch)) + "); " +
					"for (let i = 0; i < 2; i++) await tools.exec_command({cmd: " + string(mustMarshalJSON(command)) + "});"
			}
			commands, dynamic := stockLiteralExecCommands(source, f.workspace, "bash")
			observation, _ := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: f.workspace})
			history := mekugiHistory{ToolName: "exec", ExecutingThread: f.thread,
				NativePatches: nativePatchesInCall("exec", source, f.workspace), ExecObservation: observation}
			if !literalLoop {
				// Dynamic inputs do not acquire a workspace baseline.
			}
			if literalLoop {
				if dynamic {
					t.Fatal("literal loop must not request a dynamic inventory")
				}
			}
			if len(history.NativePatches) != 1 || len(commands) != 1 || dynamic == literalLoop {
				t.Fatal("fixture must pre-capture only one occurrence of each call")
			}
			trace := newNativeTraceFixture(t)
			trace.start(f.thread, "runtime", "repeated-cell", source)
			trace.tool(f.thread, "runtime", "patch-first", applyPatchToolName, patch)
			trace.result(f.thread, "patch-first", "completed", map[string]any{})
			trace.tool(f.thread, "runtime", "patch-second", applyPatchToolName, patch)
			trace.result(f.thread, "patch-second", "failed", nil)
			trace.tool(f.thread, "runtime", "command-first", nativeExecCommandToolName, string(mustMarshalJSON(map[string]any{"cmd": command})))
			trace.result(f.thread, "command-first", "completed", map[string]any{"exit_code": 0})
			trace.tool(f.thread, "runtime", "command-second", nativeExecCommandToolName, string(mustMarshalJSON(map[string]any{"cmd": command})))
			trace.result(f.thread, "command-second", "completed", map[string]any{"exit_code": 7})
			trace.end(f.thread, "runtime")
			history.nativeCell = (&nativeToolTrace{directory: trace.root}).readCell(f.thread, "repeated-cell", source)
			writeTestFile(t, filepath.Join(f.workspace, "repeated-patch.txt"), "after\n")
			writeTestFile(t, filepath.Join(f.workspace, "repeated-command.txt"), "after")
			resolveStockEdits(&history, f.workspace)
			if len(history.NativePatches) != 2 || len(history.ExecObservation.Commands) != 2 {
				t.Fatalf("resolved duplicate attempt lost: patches=%d commands=%d", len(history.NativePatches), len(history.ExecObservation.Commands))
			}
			proxy := &mekugiProxy{replayStore: f.store}
			output := mustMarshalJSON("Script completed\nWall time 0.1 seconds\nOutput:\n")
			if err := proxy.finalizeNativePatches(f.ctx, f.workspace, f.thread, "repeated-cell", history, output); err != nil {
				t.Fatal(err)
			}
			if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "repeated-cell", history: history, output: output}}); err != nil {
				t.Fatal(err)
			}
			for index, want := range []string{"patch-first", "patch-second"} {
				record, found, err := f.store.lookup(f.ctx, f.workspace, nativePatchDerivedCallID("repeated-cell", index))
				if err != nil || !found || len(record.HostResults) != 1 || record.HostResults[0].CallID != want {
					t.Fatalf("patch occurrence %d lost host outcome: %+v, %v", index, record, err)
				}
				if index == 0 && (len(record.ReviewFiles) != 1 || record.ReviewFiles[0].BeforePath != "") {
					t.Fatal("initial creation provenance lost")
				}
				if index == 1 && (len(record.ReviewFiles) != 0 || record.TranslationError == "" || record.HostResults[0].Status != "failed") {
					t.Fatal("duplicate failure borrowed cell-window effects or lost failure")
				}
			}
			record, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("repeated-cell", true))
			if err != nil || !found || len(record.HostResults) != 2 || record.HostResults[0].CallID != "command-first" ||
				record.HostResults[1].CallID != "command-second" || record.HostResults[1].ExitCode == nil || *record.HostResults[1].ExitCode != 7 ||
				strings.Count(record.Script, command) != 2 || len(record.ReviewFiles) != 1 || record.ExecOutcome.Coverage != execCoverageExact {
				t.Fatalf("command occurrences lost outcomes or duplicated scoped effects: %+v, %v", record, err)
			}
		})
	}
}

func TestResolvedLiteralReadLoopsDoNotInventWriters(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "file.txt"), "unchanged")
	writer := "printf unchanged > file.txt"
	reader := "cat file.txt"
	source := "await tools.exec_command({cmd: " + string(mustMarshalJSON(writer)) + "}); " +
		"for (let i = 0; i < 2; i++) await tools.exec_command({cmd: " + string(mustMarshalJSON(reader)) + "});"
	commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
	if dynamic || len(commands) != 2 {
		t.Fatal("fixture must have only literal call sites")
	}
	observation, _ := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: workspace})
	trace := newNativeTraceFixture(t)
	trace.start("author", "runtime", "read-loop", source)
	for index, command := range []string{writer, reader, reader} {
		id := fmt.Sprint(index)
		trace.tool("author", "runtime", id, nativeExecCommandToolName, string(mustMarshalJSON(map[string]any{"cmd": command})))
		trace.result("author", id, "completed", map[string]any{"exit_code": 0})
	}
	trace.end("author", "runtime")
	history := mekugiHistory{ExecObservation: observation, nativeCell: (&nativeToolTrace{directory: trace.root}).readCell("author", "read-loop", source)}
	resolveStockEdits(&history, workspace)
	reviews, complete, coverage, _ := reconcileExecObservation(*history.ExecObservation, execReconcileEnv{})
	results, confirmed := history.nativeCell.commands(&history, workspace)
	if !complete || coverage != execCoverageExact || len(reviews) != 0 || history.ExecObservation.RepeatedPaths || !confirmed || len(results) != 3 {
		t.Fatalf("read loop became an incomplete writer: %+v, complete=%v coverage=%s results=%v", history.ExecObservation, complete, coverage, results)
	}
}

func TestResolvedLiteralProviderLoopsReusePreCallClassification(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	python, _ := interpreterForTest("python3", "python")
	writeTestFile(t, filepath.Join(workspace, "file.txt"), "unchanged")
	script := filepath.Join(workspace, "script.py")
	writeTestFile(t, script, "from pathlib import Path\nPath('file.txt').write_text('temporary')\n")
	command := python + " script.py"
	source := "for (let i = 0; i < 2; i++) await tools.exec_command({cmd: " + string(mustMarshalJSON(command)) + "});"
	commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
	observation, _ := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: workspace})
	if dynamic || len(observation.CommandClasses) != 1 || observation.CommandClasses[0] != execScoped.String() {
		t.Fatal("fixture must pre-capture one scoped literal provider")
	}
	// The provider source changed after capture. Its original classification
	// must be reused; late source cannot explain the prior call occurrences.
	writeTestFile(t, script, "print('read only now')\n")
	history := mekugiHistory{ExecObservation: observation, nativeCell: &nativeTraceCell{ended: true, tools: []*nativeTraceTool{
		{nativeToolResult: nativeToolResult{Tool: nativeExecCommandToolName}, command: commands[0], terminal: true},
		{nativeToolResult: nativeToolResult{Tool: nativeExecCommandToolName}, command: commands[0], terminal: true},
	}}}
	resolveStockEdits(&history, workspace)
	reviews, complete, coverage, _ := reconcileExecObservation(*history.ExecObservation, execReconcileEnv{})
	if len(history.ExecObservation.Commands) != 2 || len(history.ExecObservation.CommandClasses) != 2 ||
		history.ExecObservation.CommandClasses[1] != execScoped.String() || !history.ExecObservation.RepeatedPaths ||
		len(reviews) != 0 || complete || coverage != execCoveragePartial {
		t.Fatalf("late provider state erased repeated-write uncertainty: %+v, complete=%v coverage=%s", history.ExecObservation, complete, coverage)
	}
}

func TestMChangesFilteredZeroNetIsNotPathMismatch(t *testing.T) {
	t.Parallel()
	for _, creation := range []bool{false, true} {
		t.Run(fmt.Sprintf("creation=%v", creation), func(t *testing.T) {
			f := newMChangesSliceFixture(t, "filtered-zero-net")
			path := filepath.Join(f.workspace, "file.txt")
			files := []mekugi.ReviewFile{mekugi.RenderReviewFile(path, path, "old\n", "new\n"), mekugi.RenderReviewFile(path, path, "new\n", "old\n")}
			if creation {
				files = []mekugi.ReviewFile{mekugi.RenderReviewFile("", path, "", "new\n"), mekugi.RenderReviewFile(path, "", "new\n", "")}
			}
			var ids []string
			for i, file := range files {
				call := fmt.Sprint(i)
				id := f.reserve(t, f.thread, call)
				ids = append(ids, id)
				f.publish(t, id, call, call, mekugiHistory{ReviewFiles: []mekugi.ReviewFile{file}})
			}
			output, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: ids, view: "net", paths: []string{"file.txt"}})
			if err != nil || output != "no net changes in selected captured history\n" {
				t.Fatalf("matching zero net misreported: %q, %v", output, err)
			}
		})
	}
}
