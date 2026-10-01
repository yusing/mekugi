package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestResolvedStockEditsPreserveCreationAndCrossAgentNet(t *testing.T) {
	f := newMChangesSliceFixture(t, "resolved-stock")
	proxy := &mekugiProxy{replayStore: f.store}
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", f.workspace}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	git("init", "--quiet")
	writeTestFile(t, filepath.Join(f.workspace, "existing.txt"), "before\n")
	git("add", "existing.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "baseline")

	// This is the reported producer shape: a generated patch, not a literal
	// string argument. No evaluator runs during observation.
	source := "const patch = rows.join('\\n'); await tools.apply_patch(patch); const args = getCommand(); await tools.exec_command(args);"
	if !stockDynamicPatchInputs(source, 0) {
		t.Fatal("constructed patch did not request a pre-cell baseline")
	}
	baseline := captureResolvedBaseline(f.workspace, "bash")
	var patch strings.Builder
	patch.WriteString("*** Begin Patch\n")
	for i := range 35 {
		fmt.Fprintf(&patch, "*** Add File: testdata/snapshots/fixture-%02d.txt\n+initial\n", i)
	}
	patch.WriteString("*** End Patch\n")
	command := "printf shell > shell.txt"
	history := mekugiHistory{ToolName: "exec", ExecutingThread: f.thread, ResolvedBaseline: baseline,
		nativeCell: &nativeTraceCell{ended: true, tools: []*nativeTraceTool{
			{nativeToolResult: nativeToolResult{CallID: "patch", Tool: applyPatchToolName, Status: "completed"}, input: patch.String(), terminal: true},
			{nativeToolResult: nativeToolResult{CallID: "command", Tool: nativeExecCommandToolName, Status: "completed", ExitCode: new(0)}, command: execCommandInput{Command: command}, terminal: true},
		}},
	}
	for i := range 35 {
		writeTestFile(t, filepath.Join(f.workspace, fmt.Sprintf("testdata/snapshots/fixture-%02d.txt", i)), "initial\n")
	}
	writeTestFile(t, filepath.Join(f.workspace, "shell.txt"), "shell")
	// A concurrent writer outside the resolved scopes must not be acquired.
	writeTestFile(t, filepath.Join(f.workspace, "unrelated.txt"), "another writer\n")
	resolveStockEdits(&history, f.workspace)
	output := mustMarshalJSON([]any{map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"}})
	if err := proxy.finalizeNativePatches(f.ctx, f.workspace, f.thread, "cell", history, output); err != nil {
		t.Fatal(err)
	}
	if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "cell", history: history, output: output}}); err != nil {
		t.Fatal(err)
	}
	first, found, err := f.store.lookup(f.ctx, f.workspace, nativePatchDerivedCallID("cell", 0))
	if err != nil || !found || len(first.ReviewFiles) != 35 {
		t.Fatalf("35-file initial creation missing: %+v, %v", first, err)
	}
	for _, file := range first.ReviewFiles {
		if file.BeforePath != "" || file.Incomplete != "" {
			t.Fatalf("initial creation baseline lost: %+v", file)
		}
	}
	shell, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("cell", true))
	if err != nil || !found || shell.ExecOutcome.Coverage != execCoverageExact || len(shell.ReviewFiles) != 1 {
		t.Fatalf("resolved shell write missing or borrowed unrelated file: %+v, %v", shell, err)
	}
	ids := []string{first.ChangeID, shell.ChangeID}
	// A second agent modifies the newly created snapshot, an existing file,
	// and the shell creation. Capture their real pre-write contents.
	secondObservation := observeTestCommand(t, f.workspace, "printf final > testdata/snapshots/fixture-00.txt; printf after > existing.txt; printf final > shell.txt")
	for _, name := range []string{"testdata/snapshots/fixture-00.txt", "existing.txt", "shell.txt"} {
		writeTestFile(t, filepath.Join(f.workspace, name), "final\nextra\n")
	}
	second := mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: "delegate", ExecObservation: secondObservation}
	if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "delegate-write", history: second, output: mustMarshalJSON(map[string]any{"exit_code": 0, "output": ""})}}); err != nil {
		t.Fatal(err)
	}
	delegate, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("delegate-write", false))
	if err != nil || !found || delegate.ExecutingThread != "delegate" {
		t.Fatalf("delegate ownership missing: %+v, %v", delegate, err)
	}
	ids = append(ids, delegate.ChangeID)
	// Git is only a same-scope diagnostic. Composition itself reads captures,
	// not Git or the live filesystem, and must retain the original creations.
	git("add", "existing.txt", "shell.txt", "testdata/snapshots")
	want := strings.Split(strings.TrimSpace(git("diff", "--cached", "--numstat")), "\n")
	slices.Sort(want)
	summary, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{ids[2], ids[0], ids[1]}, view: "summary"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for row := range strings.SplitSeq(strings.TrimSpace(summary), "\n") {
		_, stat, ok := strings.Cut(row, "\t")
		if !ok {
			t.Fatalf("invalid summary row: %q", row)
		}
		got = append(got, stat)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("captured composed numstat != same-scope Git:\ngot %v\nwant %v", got, want)
	}
	// Remove the workspace bytes: --net must still be backed only by durable
	// captured evidence, including the delegate and initial creation.
	if err := os.Remove(filepath.Join(f.workspace, "shell.txt")); err != nil {
		t.Fatal(err)
	}
	net, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: ids, view: "net"})
	if err != nil || strings.Contains(net, "unrelated.txt") || strings.Contains(net, "-initial") || strings.Contains(net, "-shell") || !strings.Contains(net, "+extra") {
		t.Fatalf("net provenance/composition: %q, %v", net, err)
	}
}

func TestResolvedStockEditsDoNotInventMissingBaselines(t *testing.T) {
	workspace := t.TempDir()
	b := captureResolvedBaseline(workspace, "bash")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, outside, "after\n")
	patch := "*** Begin Patch\n*** Add File: " + outside + "\n+after\n*** End Patch\n"
	history := mekugiHistory{ResolvedBaseline: b, nativeCell: &nativeTraceCell{ended: true, tools: []*nativeTraceTool{{nativeToolResult: nativeToolResult{Tool: applyPatchToolName}, input: patch, terminal: true}}}}
	resolveStockEdits(&history, workspace)
	reviews, complete := nativePatchReview(history.NativePatches[0].Files)
	if complete || len(reviews) != 1 || reviews[0].Incomplete == "" {
		t.Fatalf("post-edit bytes became a baseline: %+v, complete=%v", reviews, complete)
	}
	history = mekugiHistory{ResolvedBaseline: b}
	resolveStockEdits(&history, workspace)
	_, complete, coverage, _ := reconcileExecObservation(*history.ExecObservation, execReconcileEnv{})
	if complete || coverage != execCoveragePartial {
		t.Fatal("missing resolved host evidence became an exact no-op")
	}
}

func TestResolvedRemoteCommandsCannotAcquireLocalEvidence(t *testing.T) {
	for _, concurrentWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrentWrite=%v", concurrentWrite), func(t *testing.T) {
			f := newMChangesSliceFixture(t, "remote-stock")
			path := filepath.Join(f.workspace, "shared.txt")
			writeTestFile(t, path, "local before\n")
			command := "printf remote > shared.txt"
			arguments := string(mustMarshalJSON(map[string]any{"cmd": command, "environment_id": "remote"}))
			source := "await tools.exec_command(" + arguments + ");"
			commands, dynamic := stockLiteralExecCommands(source, f.workspace, "bash")
			if len(commands) != 0 || !dynamic {
				t.Fatal("remote call must not have a literal local observation")
			}
			observation, _ := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: f.workspace})
			history := mekugiHistory{ToolName: "exec", ExecutingThread: f.thread, ExecObservation: observation,
				ResolvedBaseline: captureResolvedBaseline(f.workspace, "bash")}
			trace := newNativeTraceFixture(t)
			trace.start(f.thread, "runtime", "remote-cell", source)
			trace.tool(f.thread, "runtime", "remote-command", nativeExecCommandToolName, arguments)
			trace.result(f.thread, "remote-command", "completed", map[string]any{"exit_code": 0})
			trace.end(f.thread, "runtime")
			history.nativeCell = (&nativeToolTrace{directory: trace.root}).readCell(f.thread, "remote-cell", source)
			if history.nativeCell == nil || string(history.nativeCell.tools[0].environmentID) != `"remote"` {
				t.Fatal("native trace lost environment selection")
			}
			if _, ok := history.nativeCell.commands(observedCommands(execCommandInput{Command: command}), f.workspace); ok {
				t.Fatal("remote host call confirmed a matching local command")
			}
			if concurrentWrite {
				writeTestFile(t, path, "unrelated local writer\n")
			}
			resolveStockEdits(&history, f.workspace)
			if len(history.ExecObservation.Files) != 0 || len(history.ExecObservation.Commands) != 0 {
				t.Fatal("remote call consumed local scope or baseline")
			}
			proxy := &mekugiProxy{replayStore: f.store}
			output := mustMarshalJSON("Script completed\nWall time 0.1 seconds\nOutput:\n")
			if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "remote-cell", history: history, output: output}}); err != nil {
				t.Fatal(err)
			}
			record, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("remote-cell", true))
			if err != nil || !found || record.ChangeID == "" || record.ExecOutcome.Coverage != execCoveragePartial || len(record.ReviewFiles) != 0 {
				t.Fatalf("remote call claimed local evidence or authoritative zero: %+v, %v", record, err)
			}
			if _, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{record.ChangeID}, view: "net"}); err == nil {
				t.Fatal("remote call established a complete local net")
			}
		})
	}
}

func TestResolvedStockInventoryCannotOverflowSupportedCarrier(t *testing.T) {
	content := strings.Repeat("x", maxNativePatchFileBytes)
	inventory := &execInventory{Root: "/workspace", Entries: map[string]string{"file": "1:1"}, Files: []execFileSnapshot{{Path: "/workspace/file", Content: content}}}
	history := mekugiHistory{ResolvedBaseline: &resolvedStockBaseline{Root: "/workspace", Inventory: inventory}}
	for i := range 3 {
		history.NativePatches = append(history.NativePatches, nativePatchObservation{Files: []nativePatchFileSnapshot{{BeforePath: fmt.Sprint(i), Before: content}}})
	}
	boundStockInventory(&history, "/workspace", "cell")
	bounded := history.ResolvedBaseline.Inventory
	if len(inventory.Files) != 1 || len(bounded.Files) != 0 || bounded.Entries["file"] == "" {
		t.Fatalf("inventory content was not dropped on a copy: original=%d bounded=%+v", len(inventory.Files), bounded)
	}
	// The result record may carry the one inventory for both consumers.
	history.ExecObservation = &execObservation{Inventory: bounded}
	if size := len(mustMarshalJSON(replayRecord{Version: 1, Workspace: "/workspace", CallID: "cell", History: history})); size > maxReplayRecordBytes {
		t.Fatalf("record is %d bytes, over the %d byte bound", size, maxReplayRecordBytes)
	}
}

func TestSupportedSnapshotGeneratorRetainsInitialCreation(t *testing.T) {
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
	if err != nil || !found || record.ChangeID == "" || len(record.ReviewFiles) != 0 {
		t.Fatalf("attempt vanished or fabricated intermediate diff: %+v, %v", record, err)
	}
	detail, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{record.ChangeID}, view: "history"})
	if err != nil || !strings.Contains(detail, "path.write_text(replacement)") || !strings.Contains(detail, "path.write_text(current)") || !strings.Contains(detail, "intermediate writes unobserved") {
		t.Fatalf("temporary edit attempt hidden: %q, %v", detail, err)
	}
	net, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{record.ChangeID}, view: "net"})
	if err == nil || strings.Contains(net, "no net changes") {
		t.Fatalf("unobserved intermediate effects claimed complete: %q, %v", net, err)
	}
}

func TestMChangesSummaryComposesManagedAndDirectEdits(t *testing.T) {
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
	if err != nil || summary != "A\t1\t0\tfile.txt\n" {
		t.Fatalf("summary summed attempts or split ownership: %q, %v", summary, err)
	}
}

func TestMChangesReadContextDistinguishesMissingEmptyAndPathScope(t *testing.T) {
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

func TestResolvedStdinWritersRemainIncomplete(t *testing.T) {
	for _, chars := range []string{"", "printf changed > file.txt\n"} {
		t.Run(fmt.Sprintf("poll=%v", chars == ""), func(t *testing.T) {
			f := newMChangesSliceFixture(t, "resolved-stdin")
			trace := newNativeTraceFixture(t)
			source := "await tools.write_stdin({session_id: 1, chars: " + string(mustMarshalJSON(chars)) + "});"
			trace.start(f.thread, "runtime", "stdin-cell", source)
			trace.tool(f.thread, "runtime", "stdin", "write_stdin", string(mustMarshalJSON(map[string]any{"session_id": 1, "chars": chars})))
			trace.result(f.thread, "stdin", "completed", map[string]any{})
			trace.end(f.thread, "runtime")
			commands, dynamic := stockLiteralExecCommands(source, f.workspace, "bash")
			observation, _ := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: f.workspace})
			history := mekugiHistory{ToolName: "exec", ExecutingThread: f.thread, ResolvedBaseline: captureResolvedBaseline(f.workspace, "bash"), ExecObservation: observation,
				nativeCell: (&nativeToolTrace{directory: trace.root}).readCell(f.thread, "stdin-cell", source)}
			resolveStockEdits(&history, f.workspace)
			proxy := &mekugiProxy{replayStore: f.store}
			output := mustMarshalJSON("Script completed\nWall time 0.1 seconds\nOutput:\n")
			if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "stdin-cell", history: history, output: output}}); err != nil {
				t.Fatal(err)
			}
			record, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("stdin-cell", true))
			if err != nil || !found {
				t.Fatalf("stdin attempt missing: %+v, %v", record, err)
			}
			if chars == "" {
				if record.ChangeID != "" || record.ExecOutcome.Coverage != execCoverageExact {
					t.Fatal("poll became an incomplete writer")
				}
			} else {
				if record.ChangeID == "" || record.ExecOutcome.Coverage != execCoveragePartial {
					t.Fatal("stdin writer became exact no-op")
				}
				if _, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{record.ChangeID}, view: "net"}); err == nil {
					t.Fatal("stdin writer allowed complete --net")
				}
			}
		})
	}
}

func TestResolvedCommandKeepsPreCapturedProviderScope(t *testing.T) {
	workspace := t.TempDir()
	pythonName, _ := interpreterForTest("python3", "python")
	command := pythonName + " -c 'from pathlib import Path; Path(\"snapshot.txt\").write_text(\"snapshot\\n\")'"
	source := "await tools.exec_command({cmd: " + string(mustMarshalJSON(command)) + "}); const args = getCommand(); await tools.exec_command(args);"
	commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
	observation, _ := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: workspace})
	if observation.LiteralClass != execScoped.String() {
		t.Fatalf("provider scope not pre-captured: %+v", observation)
	}
	baseline := captureResolvedBaseline(workspace, "bash")
	history := mekugiHistory{ResolvedBaseline: baseline, ExecObservation: observation, nativeCell: &nativeTraceCell{ended: true, tools: []*nativeTraceTool{
		{nativeToolResult: nativeToolResult{Tool: nativeExecCommandToolName}, command: commands[0], terminal: true},
		{nativeToolResult: nativeToolResult{Tool: nativeExecCommandToolName}, command: execCommandInput{Command: "printf dynamic > dynamic.txt"}, terminal: true},
	}}}
	writeTestFile(t, filepath.Join(workspace, "snapshot.txt"), "snapshot\n")
	writeTestFile(t, filepath.Join(workspace, "dynamic.txt"), "dynamic")
	resolveStockEdits(&history, workspace)
	reviews, complete, coverage, _ := reconcileExecObservation(*history.ExecObservation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || len(reviews) != 2 {
		t.Fatalf("mixed cell lost pre-captured provider evidence: %+v, %v, %s", reviews, complete, coverage)
	}
}

func TestResolvedRepeatedStockCallsRetainEveryOutcome(t *testing.T) {
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
				history.ResolvedBaseline = captureResolvedBaseline(f.workspace, "bash")
			}
			if literalLoop {
				if dynamic || stockDynamicPatchInputs(source, len(history.NativePatches)) {
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
		len(reviews) != 1 || !complete || coverage != execCoverageExact {
		t.Fatalf("late provider state erased repeated-write uncertainty: %+v, complete=%v coverage=%s", history.ExecObservation, complete, coverage)
	}
}

func TestMChangesFilteredZeroNetIsNotPathMismatch(t *testing.T) {
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
