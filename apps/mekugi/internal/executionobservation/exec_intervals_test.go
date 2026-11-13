package executionobservation

import (
	"context"
	"crypto/sha256"
	jsonv1 "encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/diffmodel"

	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestLegacyMetadataSurvivesIntervalPublication(t *testing.T) {
	f := newMChangesSliceFixture(t, "legacy-interval")
	p := testProxyWithStore(f.store, &Windows{})
	selected := observationScope(f.ctx, p.Retained)
	path := filepath.Join(f.workspace, "file.txt")
	writeTestFile(t, path, "A\n")
	before := changecapture.ReadFile(path, nil)
	inventory := &legacycall.Inventory{Version: 1, Root: f.workspace,
		Entries:     map[string]string{"file.txt": "old-stamp"},
		Directories: map[string]legacycall.DirectoryStamp{f.workspace: {Exists: true, Complete: true}},
		Files:       []legacycall.File{retainedFile(before)}}
	old := legacycall.History{ToolName: nativeExecCommandToolName, Root: f.workspace, ExecutingThread: f.thread,
		ResolvedBaseline: &legacycall.Baseline{Root: f.workspace, Inventory: inventory},
		ExecObservation:  &legacycall.Observation{Files: []legacycall.File{retainedFile(before)}, Inventory: inventory},
		ExecOutcome:      &legacycall.Outcome{Unswept: "historical coverage diagnostic"}}
	if err := selected.Store.Transaction(f.ctx, nil, func(tx *history.Transaction) error {
		return publishObservation(tx, selected, legacycall.Record{Version: 1, Workspace: f.workspace, CallID: "writer", History: old})
	}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"writer", "patch"} {
		p.Windows.open(&execWindow{ref: ref, group: ref, paths: []string{path}, scope: selected})
	}
	writeTestFile(t, path, "B\n")
	transform := &mekugiResponseTransform{owners: &Preparer{Config: Config{Windows: p.Windows, Changes: p.Changes}}, ctx: f.ctx, directory: f.workspace}
	cut := execIntervalCut{writer: "writer", scope: selected}
	if err := transform.owners.splitExecInterval(f.ctx, f.ctx, f.workspace, "patch", []changecapture.File{changecapture.ReadFile(path, nil)}, cut, false); err != nil {
		t.Fatal(err)
	}
	id := execIntervalID("writer", false, 0)
	check := func(call string, baseline bool) {
		t.Helper()
		value, found, err := lookupObservation(f.ctx, selected, f.workspace, call)
		if err != nil || !found {
			t.Fatalf("lookup %s: %v, %v", call, found, err)
		}
		got := retainedHistory(value)
		if got.ExecObservation == nil || !reflect.DeepEqual(got.ExecObservation.Inventory, inventory) ||
			got.ExecOutcome == nil || got.ExecOutcome.Unswept != old.ExecOutcome.Unswept {
			t.Fatalf("%s lost historical metadata: %+v", call, got)
		}
		if baseline && (got.ResolvedBaseline == nil || !reflect.DeepEqual(got.ResolvedBaseline.Inventory, inventory)) {
			t.Fatalf("%s lost baseline inventory: %+v", call, got.ResolvedBaseline)
		}
	}
	check(id, false)
	writeTestFile(t, path, "C\n")
	continuations, err := prepareExecIntervalContinuations(f.ctx, f.workspace, []execIntervalCut{cut}, []changecapture.File{changecapture.ReadFile(path, nil)})
	if err != nil || len(continuations) != 1 {
		t.Fatalf("prepare continuation: %+v, %v", continuations, err)
	}
	if err := publishObservations(f.ctx, selected, p.Changes, nil, f.workspace, map[string]mekugiHistory{"writer": legacyHistory(old)}, continuations...); err != nil {
		t.Fatal(err)
	}
	check(id+":resume", false)
	check("writer", true)
	got := retainedHistory(legacyHistory(old))
	if !reflect.DeepEqual(got, old) {
		t.Fatalf("historical round trip changed metadata: %+v", got)
	}
	displayID, err := p.Changes.Reserve(f.ctx, selected, f.workspace, f.thread, "legacy-readable")
	if err != nil {
		t.Fatal(err)
	}
	completed := legacyHistory(old)
	completed.ChangeID, completed.CorrelationID = displayID, "legacy-readable"
	completed.ExecOutcome.Status = changecapture.StatusCompleted
	completed.ReviewFiles = []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "A\n", "B\n")}
	if err := publishObservations(f.ctx, selected, p.Changes, nil, f.workspace, map[string]mekugiHistory{"display": completed}); err != nil {
		t.Fatal(err)
	}
	text, err := p.Changes.Read(f.ctx, selected, changes.Query{Workspace: f.workspace, IDs: []string{displayID}, View: "history"})
	if err != nil || !strings.Contains(text, old.ExecOutcome.Unswept) {
		t.Fatalf("historical display lost coverage diagnostic: %q, %v", text, err)
	}
}

func TestExecCompletionOrderingSkipsUninterruptedHistory(t *testing.T) {
	windows := &Windows{}
	members := make([]execCompletion, 1000)
	if allocations := testing.AllocsPerRun(10, func() {
		ordered := orderExecCompletions(windows, members)
		if len(ordered) != len(members) || &ordered[0] != &members[0] {
			t.Fatal("uninterrupted history was rebuilt")
		}
	}); allocations != 0 {
		t.Fatalf("uninterrupted history allocated %g objects", allocations)
	}
}

func TestCheckpointWriterPatchIntervals(t *testing.T) {
	usePreloadTestFrontend(t)
	for _, scenario := range []string{"formatter", "failed-tests", "opaque-writer", "missing-checkpoint", "overlapping-writers"} {
		t.Run(scenario, func(t *testing.T) {
			isolateSnapshotGit(t)
			f := newMChangesSliceFixture(t, "checkpoint-interval")
			proxy := newManagedMekugiProxy(t)
			setTestProxyStore(proxy, f.store)
			trace := newNativeTraceFixture(t)
			proxy.NativeTrace = nativetrace.NewWithNotice(trace.root, nil)
			path := filepath.Join(f.workspace, "file.go")
			initial := "package example\nvar value=1\n"
			writeTestFile(t, path, initial)
			seed := f.reserve(t, f.thread, "create")
			f.publish(t, seed, "create", "create", mekugiHistory{ExecutingThread: f.thread, ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", path, "", initial)}})
			var items []any
			prepare := func() *mekugiResponseTransform {
				t.Helper()
				request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
					"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, items...),
					"tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
				}))
				if err != nil {
					t.Fatal(err)
				}
				transform, err := proxy.prepareRequest(f.ctx, &request, f.thread, f.thread, codexresponses.TurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{f.workspace: nil}}, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(transform.Close)
				transform.sessionShell = "bash"
				return transform
			}
			transform := prepare()
			observe := func(id, source string) {
				t.Helper()
				call := map[string]any{"type": "custom_tool_call", "id": id + "-item", "call_id": id, "name": "exec", "input": source, "status": "completed"}
				if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{call}})); err != nil {
					t.Fatal(err)
				}
				items = append(items, call, map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": "Script completed\nWall time 0.1 seconds\nOutput:\n"})
				trace.start(f.thread, id+"-runtime", id, source)
			}
			reconcile := func() {
				t.Helper()
				request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
				if _, err := proxy.reconcileVisibleInput(f.ctx, &request, f.workspace, f.thread); err != nil {
					t.Fatal(err)
				}
			}
			lookup := func(id string) mekugiHistory {
				t.Helper()
				h, found, err := f.store.lookup(f.ctx, f.workspace, id)
				if err != nil || !found {
					t.Fatalf("lookup %s: %v, %v", id, found, err)
				}
				return h
			}
			command := "gofmt -w .; mrun --max-tokens 2000 make test"
			if scenario == "opaque-writer" {
				command = "./generate; mrun --max-tokens 2000 make test"
			}
			observe("writer", "await tools.exec_command({cmd: "+string(responses.MustMarshalJSON(command))+"});")
			original := lookup("writer")
			if original.ExecObservation == nil || original.ExecObservation.Tree == "" || len(original.ExecObservation.Files) != 0 {
				t.Fatal("fixture must exercise checkpoint-only before-state")
			}
			if scenario == "missing-checkpoint" {
				// Lose only the witnessed private tree, not retained evidence or
				// current files. Later checkpoints cannot reconstruct that baseline.
				tree := original.ExecObservation.Tree
				directory := fmt.Sprintf("%x", sha256.Sum256([]byte(f.workspace)))[:32]
				if err := os.Remove(filepath.Join(f.store.directory, "workspace-snapshots", directory, "git", "objects", tree[:2], tree[2:])); err != nil {
					t.Fatal(err)
				}
			}
			trace.tool(f.thread, "writer-runtime", "writer-command", "exec_command", string(responses.MustMarshalJSON(map[string]string{"cmd": command})))
			if scenario == "overlapping-writers" {
				transform = prepare()
				observe("sibling", "await tools.exec_command({cmd: "+string(responses.MustMarshalJSON(command))+"});")
				trace.tool(f.thread, "sibling-runtime", "sibling-command", "exec_command", string(responses.MustMarshalJSON(map[string]string{"cmd": command})))
				trace.result(f.thread, "sibling-command", "completed", map[string]any{"session_id": 102})
				trace.end(f.thread, "sibling-runtime")
			}
			// The formatter finishes, but its following tests keep the host open.
			if output, err := exec.Command("gofmt", "-w", f.workspace).CombinedOutput(); err != nil {
				t.Fatalf("format: %v: %s", err, output)
			}
			formatted, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			trace.result(f.thread, "writer-command", "completed", map[string]any{"session_id": 101})
			trace.end(f.thread, "writer-runtime")
			reconcile()
			transform = prepare()
			patch := "*** Begin Patch\n*** Update File: file.go\n@@\n-var value = 1\n+var value = 2\n*** End Patch\n"
			observe("patch", "await tools.apply_patch("+string(responses.MustMarshalJSON(patch))+");")
			trace.tool(f.thread, "patch-runtime", "patch-command", "apply_patch", patch)
			writeTestFile(t, path, strings.Replace(string(formatted), "= 1", "= 2", 1))
			trace.result(f.thread, "patch-command", "completed", map[string]any{"exit_code": 0})
			trace.end(f.thread, "patch-runtime")
			reconcile()
			var secondPatch string
			want := "+var value = 2\n"
			if scenario == "overlapping-writers" {
				// The same two writers remain open across another observed cut.
				writeTestFile(t, path, strings.Replace(string(formatted), "= 1", "= 3", 1))
				transform = prepare()
				patch = "*** Begin Patch\n*** Update File: file.go\n@@\n-var value = 3\n+var value = 4\n*** End Patch\n"
				observe("patch2", "await tools.apply_patch("+string(responses.MustMarshalJSON(patch))+");")
				trace.tool(f.thread, "patch2-runtime", "patch2-command", "apply_patch", patch)
				writeTestFile(t, path, strings.Replace(string(formatted), "= 1", "= 4", 1))
				trace.result(f.thread, "patch2-command", "completed", map[string]any{"exit_code": 0})
				trace.end(f.thread, "patch2-runtime")
				reconcile()
				secondPatch = lookup(legacycall.PatchCallID("patch2", 0)).ChangeID
				want = "+var value = 4\n"
			}
			exit := 0
			if scenario == "failed-tests" {
				exit = 1
			}
			trace.event(f.thread, map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "writer-command", "status": "completed", "runtime_payload": trace.ref(map[string]any{"exit_code": exit})})
			if scenario == "overlapping-writers" {
				trace.event(f.thread, map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "sibling-command", "status": "completed", "runtime_payload": trace.ref(map[string]any{"exit_code": 0})})
			}
			reconcile()
			if scenario == "overlapping-writers" && lookup(legacycall.ExecCallID("sibling", true)).ChangeID != "" {
				t.Fatal("overlapping checkpoint writer claimed the formatter effect twice")
			}
			writer := lookup(legacycall.ExecCallID("writer", true))
			patchHistory := lookup(legacycall.PatchCallID("patch", 0))
			prefix := lookup(execIntervalID("writer", true, 0))
			if writer.ChangeID == "" || writer.ChangeID != prefix.ChangeID || len(writer.ReviewFiles) != 0 || writer.ExecOutcome.Exit == nil || *writer.ExecOutcome.Exit != exit {
				t.Fatalf("writer terminal lost identity, outcome, or attribution: %+v", writer)
			}
			if len(patchHistory.ReviewFiles) != 1 || !strings.Contains(patchHistory.ReviewFiles[0].Diff, "-var value = 1\n+var value = 2") {
				t.Fatalf("patch stole formatter changes: %+v", patchHistory.ReviewFiles)
			}
			ids := []string{seed, writer.ChangeID, patchHistory.ChangeID}
			if secondPatch != "" {
				ids = append(ids, secondPatch)
				second := lookup(execIntervalID("writer", true, 1))
				if len(second.ReviewFiles) != 1 || !strings.Contains(second.ReviewFiles[0].Diff, "-var value = 2\n+var value = 3") {
					t.Fatalf("second writer interval reused stale claims: %+v", second.ReviewFiles)
				}
			}
			stdout, stderr, status := f.run(t, "mchanges "+strings.Join(ids, " ")+" -- file.go")
			if scenario == "missing-checkpoint" {
				if status != 1 || !strings.Contains(stderr, "checkpoint unavailable") || len(prefix.ReviewFiles) != 1 || prefix.ReviewFiles[0].Incomplete == "" || writer.ExecOutcome.Coverage != changecapture.CoveragePartial {
					t.Fatalf("missing checkpoint claimed complete evidence: %q, %q, %d; %+v", stdout, stderr, status, prefix)
				}
				stdout, stderr, status = f.run(t, "mchanges "+strings.Join(ids, " ")+" --summary -- file.go")
				if status != 0 || !strings.Contains(stdout, "?\t-\t-\tfile.go") || !strings.Contains(stdout, "checkpoint unavailable") || stderr != "" {
					t.Fatalf("missing checkpoint lost partial-coverage status: %q, %q, %d", stdout, stderr, status)
				}
				return
			}
			if status != 0 || stderr != "" || !strings.Contains(stdout, want) || strings.Contains(stdout, "+var value=1\n") || len(prefix.ReviewFiles) != 1 || !strings.Contains(prefix.ReviewFiles[0].Diff, "+var value = 1\n") {
				t.Fatalf("composed mchanges lost formatter interval: %q, %q, %d; %+v", stdout, stderr, status, prefix.ReviewFiles)
			}
			writeTestFile(t, path, "unrelated live bytes\n")
			reopened, err := openMekugiReplayStore(f.store.directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.snapshots.Close)
			net, err := reopened.changes.Read(f.ctx, reopened.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: ids, View: "net", Paths: []string{path}})
			if err != nil || net != stdout {
				t.Fatalf("cold retained composition changed: %q, %v", net, err)
			}
		})
	}
}

func TestCheckpointIntervalCanceledAcquisitionRetainsGap(t *testing.T) {
	f := newMChangesSliceFixture(t, "checkpoint-canceled")
	p := testProxyWithStore(f.store, &Windows{})
	selected := observationScope(f.ctx, p.Retained)
	path := filepath.Join(f.workspace, "file.txt")
	writeTestFile(t, path, "before\n")
	observation, _ := changecapture.Capture(f.ctx, []commandanalysis.Command{{Command: "custom-generator", Workdir: f.workspace}}, false, false, changecapture.Options{Directory: f.workspace, Workspaces: p.Checkpoints})
	if observation == nil || observation.Tree == "" {
		t.Fatal("missing checkpoint fixture")
	}
	original := mekugiHistory{ToolName: nativeExecCommandToolName, Root: f.workspace, ExecutingThread: f.thread, ExecObservation: observation}
	if err := publishObservations(f.ctx, selected, p.Changes, nil, f.workspace, map[string]mekugiHistory{"writer": original}); err != nil {
		t.Fatal(err)
	}
	p.Windows.open(&execWindow{ref: "writer", group: "writer", roots: []string{f.workspace}, scope: selected})
	release := p.Windows.serialize(f.workspace)
	defer release()
	acquisition, cancel := context.WithCancel(f.ctx)
	cancel()
	if err := p.splitExecInterval(f.ctx, acquisition, f.workspace, "patch", []changecapture.File{{Path: path, Kind: changecapture.Text, Content: "boundary\n"}}, execIntervalCut{writer: "writer", scope: selected}, false); err != nil {
		t.Fatal(err)
	}
	prefix, found, err := f.store.lookup(f.ctx, f.workspace, execIntervalID("writer", false, 0))
	if err != nil || !found || len(prefix.ReviewFiles) != 1 || !strings.Contains(prefix.ReviewFiles[0].Incomplete, "context canceled") {
		t.Fatalf("canceled acquisition lost explicit incomplete evidence: %+v, %v", prefix, err)
	}
}

func TestPrivateArtifactWriterDoesNotClaimIsolatedWorktree(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%v", canceled), func(t *testing.T) {
			isolateSnapshotGit(t)
			f := newMChangesSliceFixture(t, "private-artifact")
			gitTestRun(t, f.workspace, "init", "--quiet")
			writeTestFile(t, filepath.Join(f.workspace, ".gitignore"), ".worktrees/\n")
			writeTestFile(t, filepath.Join(f.workspace, "test.go"), "before\n")
			gitTestRun(t, f.workspace, "add", ".")
			gitTestRun(t, f.workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
			sibling := filepath.Join(f.workspace, ".worktrees", "delegate")
			gitTestRun(t, f.workspace, "worktree", "add", "--quiet", "--detach", sibling)
			private := filepath.Join(t.TempDir(), "task.txt")
			writeTestFile(t, private, "private before\n")
			p := testProxyWithStore(f.store, &Windows{})
			selected := observationScope(f.ctx, p.Retained)
			command := "printf 'private after\\n' > " + private
			observation, _ := changecapture.Capture(f.ctx, []commandanalysis.Command{{Command: command, Workdir: f.workspace, Shell: "bash"}}, false, false, changecapture.Options{Directory: f.workspace, Workspaces: p.Checkpoints})
			if observation == nil || observation.Tree == "" || len(observation.Files) != 1 {
				t.Fatal("missing private writer checkpoint/baseline")
			}
			original := mekugiHistory{ToolName: nativeExecCommandToolName, Root: f.workspace, Script: command, ExecutingThread: f.thread, ExecObservation: observation}
			if err := publishObservations(f.ctx, selected, p.Changes, nil, f.workspace, map[string]mekugiHistory{"writer": original}); err != nil {
				t.Fatal(err)
			}
			p.Windows.open(&execWindow{ref: "writer", group: "writer", workspace: f.workspace, roots: observation.Roots, paths: observation.ScopePaths(), scope: selected})
			path := filepath.Join(sibling, "test.go")
			childObservation := changecapture.NamedUntil([]string{path}, sibling, time.Now().Add(500*time.Millisecond), captureSource(selected))
			child := mekugiHistory{ToolName: nativeExecCommandToolName, Root: sibling, ExecutingThread: "delegate", ExecObservation: childObservation}
			if err := publishObservations(f.ctx, selected, p.Changes, nil, sibling, map[string]mekugiHistory{"child": child}); err != nil {
				t.Fatal(err)
			}
			p.Windows.open(&execWindow{ref: "child", group: "child", workspace: sibling, roots: childObservation.Roots, paths: childObservation.ScopePaths(), scope: selected})
			acquisition, cancel := context.WithCancel(f.ctx)
			defer cancel()
			if canceled {
				cancel()
			}
			if err := p.splitExecInterval(f.ctx, acquisition, sibling, "child", childObservation.Files, execIntervalCut{writer: "writer", workspace: f.workspace, scope: selected}, false); err != nil {
				t.Fatal(err)
			}
			if prefix, found, err := f.store.lookup(f.ctx, f.workspace, execIntervalID("writer", false, 0)); err != nil || found {
				t.Fatalf("private writer claimed sibling boundary: found=%v err=%v files=%+v", found, err, prefix.ReviewFiles)
			}
			writeTestFile(t, private, "private after\n")
			writeTestFile(t, path, "delegate after\n")
			for _, completion := range []struct {
				workspace, call string
				history         mekugiHistory
			}{{sibling, "child", child}, {f.workspace, "writer", original}} {
				if err := finalizeExecObservations(p.Retained, p.Changes, p.Checkpoints, p.Recovery, p.PublishChanges, p.Windows, p.Audience, p.Notice, f.ctx, completion.workspace, []execCompletion{{callID: completion.call, history: completion.history, result: binding.ExecutionResult{Terminal: true, Succeeded: true}}}); err != nil {
					t.Fatal(err)
				}
			}
			for _, want := range []struct{ workspace, call, thread, path, added, absent string }{
				{f.workspace, "writer", f.thread, private, "+private after", "test.go"},
				{sibling, "child", "delegate", path, "+delegate after", "task.txt"},
			} {
				completed, found, err := f.store.lookup(f.ctx, want.workspace, legacycall.ExecCallID(want.call, false))
				if err != nil || !found || completed.ExecutingThread != want.thread || len(completed.ReviewFiles) != 1 || completed.ReviewFiles[0].AfterPath != want.path || completed.ExecOutcome.Coverage != changecapture.CoverageExact {
					t.Fatalf("incorrect terminal attribution for %s: %+v, %v", want.call, completed, err)
				}
				f.invocation = mchangesSliceInvocation(want.workspace, f.thread)
				stdout, stderr, status := f.run(t, "mchanges "+completed.ChangeID)
				if status != 0 || stderr != "" || !strings.Contains(stdout, want.added) || strings.Contains(stdout, want.absent) {
					t.Fatalf("retained %s evidence: %q, %q, %d", want.call, stdout, stderr, status)
				}
			}
			f.invocation = mchangesSliceInvocation(f.workspace, f.thread)
			stdout, stderr, status := f.run(t, "mchanges --mine")
			if status != 0 || stderr != "" || !strings.Contains(stdout, "+private after") || strings.Contains(stdout, "test.go") {
				t.Fatalf("implicit caller selection included sibling work: %q, %q, %d", stdout, stderr, status)
			}
		})
	}
}

func TestCheckpointIntervalsKeepAuthoredWorkspace(t *testing.T) {
	for _, mode := range []string{"eager", "dynamic-native", "terminal-native", "eager-writer-alias", "eager-patch-alias", "dynamic-native-writer-alias", "dynamic-native-patch-alias", "terminal-native-writer-alias", "terminal-native-patch-alias"} {
		t.Run(mode, func(t *testing.T) {
			isolateSnapshotGit(t)
			f := newMChangesSliceFixture(t, "cross-workspace-interval")
			writer := testProxyWithStore(f.store, &Windows{})
			edit := testProxyWithStore(f.store, writer.Windows.ForRuntime())
			selected := observationScope(f.ctx, writer.Retained)
			directory := t.TempDir()
			writerDirectory := directory
			if strings.HasSuffix(mode, "-alias") {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(directory, alias); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(mode, "-writer-alias") {
					writerDirectory = alias
					mode = strings.TrimSuffix(mode, "-writer-alias")
				} else {
					directory = alias
					mode = strings.TrimSuffix(mode, "-patch-alias")
				}
			}
			path := filepath.Join(directory, "selected.txt")
			writeTestFile(t, path, "A\n")
			seed := f.reserve(t, f.thread, "create")
			f.publish(t, seed, "create", "create", mekugiHistory{ExecutingThread: f.thread, ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", path, "", "A\n")}})
			observation, _ := changecapture.Capture(f.ctx, []commandanalysis.Command{{Command: "custom-generator", Workdir: writerDirectory, Shell: "bash"}}, false, true, changecapture.Options{Directory: f.workspace, Workspaces: writer.Checkpoints})
			if observation == nil || observation.Checkpoints[writerDirectory] == "" || len(observation.Files) != 0 {
				t.Fatal("missing external checkpoint fixture")
			}
			trace := newNativeTraceFixture(t)
			trace.turn = "turn"
			edit.NativeTrace = nativetrace.NewWithNotice(trace.root, nil)
			arguments := responses.MustMarshalJSON(map[string]string{"cmd": "custom-generator", "workdir": writerDirectory})
			writerSource := "await tools.exec_command(" + string(arguments) + ");"
			original := mekugiHistory{ToolName: "exec", Script: writerSource, Root: f.workspace, ExecutingThread: f.thread, ExecObservation: observation}
			if err := publishObservations(f.ctx, selected, writer.Changes, nil, f.workspace, map[string]mekugiHistory{"writer": original}); err != nil {
				t.Fatal(err)
			}
			writer.Windows.open(&execWindow{ref: "writer", group: "writer", workspace: f.workspace, roots: observation.Roots, scope: selected, thread: f.thread, turn: trace.turn, source: writerSource})
			trace.start(f.thread, "writer-runtime", "writer", writerSource)
			trace.tool(f.thread, "writer-runtime", "writer-command", "exec_command", string(arguments))
			writeTestFile(t, path, "B\n")
			before := "B"
			if mode == "terminal-native" {
				before = "D"
			}
			patch := "*** Begin Patch\n*** Update File: selected.txt\n@@\n-" + before + "\n+C\n*** End Patch\n"
			patchSource := "await tools.apply_patch(" + string(responses.MustMarshalJSON(patch)) + ");"
			later := mekugiHistory{ToolName: "exec", Root: directory, ExecutingThread: f.thread}
			if mode == "dynamic-native" {
				patchSource = "await tools.apply_patch(makePatch());"
			} else {
				later.NativePatches = changecapture.Patches([]string{patch}, directory)
			}
			later.Script = patchSource
			if err := publishObservations(f.ctx, selected, edit.Changes, nil, directory, map[string]mekugiHistory{"patch": later}); err != nil {
				t.Fatal(err)
			}
			edit.Windows.open(&execWindow{ref: "patch", group: "patch", workspace: directory, roots: []string{directory}, scope: selected, thread: f.thread, turn: trace.turn, source: patchSource})
			transform := &mekugiResponseTransform{owners: edit, ctx: f.ctx, directory: directory, execGroup: "patch"}
			if err := transform.splitExecIntervals("patch", nil, later.NativePatches); err != nil {
				t.Fatal(err)
			}
			if mode == "terminal-native" {
				// A completed writer changes again between the eager cut and hook.
				writeTestFile(t, path, "D\n")
				trace.result(f.thread, "writer-command", "completed", map[string]any{"exit_code": 0})
			} else {
				trace.result(f.thread, "writer-command", "completed", map[string]any{"session_id": 101})
			}
			trace.end(f.thread, "writer-runtime")
			trace.start(f.thread, "patch-runtime", "patch", patchSource)
			trace.tool(f.thread, "patch-runtime", "patch-command", "apply_patch", patch)
			if mode != "eager" {
				ctx, cancel := context.WithTimeout(f.ctx, segmenttransport.BoundaryHold)
				deadline, _ := ctx.Deadline()
				edit.ObserveNativeBoundary(ctx, segmenttransport.Boundary{Thread: f.thread, Turn: trace.turn, Item: "patch-command", Tool: applyPatchToolName, CWD: directory, Input: patch, Deadline: deadline})
				cancel()
			}
			prefixID := execIntervalID("writer", true, 0)
			prefix, found, err := f.store.lookup(f.ctx, f.workspace, prefixID)
			if err != nil || !found || len(prefix.ReviewFiles) != 1 || !strings.Contains(prefix.ReviewFiles[0].Diff, "-A\n+B") {
				t.Fatalf("writer's authored workspace lost prefix: %+v, %v", prefix, err)
			}
			if _, found, err := f.store.lookup(f.ctx, directory, prefixID); found || err != nil {
				t.Fatalf("prefix published under interrupter workspace: %v, %v", found, err)
			}
			writeTestFile(t, path, "C\n")
			trace.result(f.thread, "patch-command", "completed", map[string]any{"exit_code": 0})
			trace.end(f.thread, "patch-runtime")
			later.nativeCell = edit.NativeTrace.ReadCell(f.thread, "patch", patchSource)
			if err := edit.loadNativeBoundaries(f.ctx, directory, "patch", &later); err != nil {
				t.Fatal(err)
			}
			if err := edit.finalizeNativePatches(f.ctx, directory, f.thread, "patch", later, binding.ExecutionResult{Terminal: true, Succeeded: true}); err != nil {
				t.Fatal(err)
			}
			trace.event(f.thread, map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "writer-command", "status": "completed", "runtime_payload": trace.ref(map[string]any{"exit_code": 0})})
			original.nativeCell = edit.NativeTrace.ReadCell(f.thread, "writer", writerSource)
			if err := finalizeExecObservations(writer.Retained, writer.Changes, writer.Checkpoints, writer.Recovery, writer.PublishChanges, writer.Windows, writer.Audience, writer.Notice, f.ctx, f.workspace, []execCompletion{{callID: "writer", history: original, result: binding.ExecutionResult{Terminal: true, Succeeded: true}}}); err != nil {
				t.Fatal(err)
			}
			terminal, found, err := f.store.lookup(f.ctx, f.workspace, legacycall.ExecCallID("writer", true))
			if err != nil || !found || terminal.ExecOutcome.Coverage != changecapture.CoverageExact || len(terminal.ReviewFiles) != 0 {
				t.Fatalf("terminal writer duplicated the patch effect: %+v, %v", terminal, err)
			}
			patchHistory, _, err := f.store.lookup(f.ctx, directory, legacycall.PatchCallID("patch", 0))
			if err != nil || patchHistory.ChangeID == "" || len(patchHistory.ReviewFiles) != 1 || !strings.Contains(patchHistory.ReviewFiles[0].Diff, "-"+before+"\n+C") {
				t.Fatalf("interrupter attribution: %+v, %v", patchHistory, err)
			}
			ids := []string{seed, prefix.ChangeID, patchHistory.ChangeID}
			f.invocation = mchangesSliceInvocation(directory, f.thread)
			stdout, stderr, status := f.run(t, "mchanges "+strings.Join(ids, " ")+" -- selected.txt")
			if status != 0 || stderr != "" || !strings.Contains(stdout, "+C\n") || strings.Contains(stdout, "+A\n") || strings.Contains(stdout, "+B\n") {
				t.Fatalf("cross-workspace mchanges: %q, %q, %d", stdout, stderr, status)
			}
			writeTestFile(t, path, "unrelated live bytes\n")
			reopened, err := openMekugiReplayStore(f.store.directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.snapshots.Close)
			net, err := reopened.changes.Read(f.ctx, reopened.scoped(f.ctx).changeScope(), changes.Query{Workspace: directory, IDs: ids, View: "net", Paths: []string{path}})
			if err != nil || net != stdout {
				t.Fatalf("cold cross-workspace composition: %q, %v", net, err)
			}
			// Reverting only A-to-B must not silently undo the independent
			// B-to-C patch. Their same-line changes require an ordinary conflict.
			writeTestFile(t, path, "C\n")
			stdout, stderr, status = f.run(t, "mchanges revert "+prefix.ChangeID)
			data, err := os.ReadFile(path)
			if status != 1 || err != nil || !strings.Contains(string(data), "C\n") || !strings.Contains(string(data), "<<<<<<< workspace") {
				t.Fatalf("writer revert erased independent patch: %q, %q, %d, file=%q, %v", stdout, stderr, status, data, err)
			}
		})
	}
}

func TestYieldedNamedWriterPatchIntervals(t *testing.T) {
	usePreloadTestFrontend(t)
	for _, scenario := range []string{"completed", "failed", "no-continuation", "missing-boundary", "missing-prefix", "no-effect", "exec-overlap", "gofmt-cell-yield", "gofmt-mrun-no-effect", "writer-first-command", "writer-first-patch"} {
		t.Run(scenario, func(t *testing.T) {
			noEffect := scenario == "no-effect" || scenario == "gofmt-mrun-no-effect"
			cellYield := scenario == "gofmt-cell-yield" || scenario == "gofmt-mrun-no-effect"
			execOverlap := scenario == "exec-overlap" || scenario == "writer-first-command"
			writerFirst := strings.HasPrefix(scenario, "writer-first-")
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			trace := newNativeTraceFixture(t)
			proxy.NativeTrace = nativetrace.NewWithNotice(trace.root, nil)
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			transform.sessionShell = "bash"
			path := filepath.Join(workspace, "file.txt")
			initial := "A\n"
			if noEffect {
				initial = "B\n"
			}
			writeTestFile(t, path, initial)
			call := func(id, source string) map[string]any {
				return map[string]any{"type": "custom_tool_call", "id": id + "-item", "call_id": id, "name": "exec", "input": source, "status": "completed"}
			}
			output := func(id string) map[string]any {
				return map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": []any{map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"}}}
			}
			observe := func(value map[string]any) {
				t.Helper()
				if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{value}})); err != nil {
					t.Fatal(err)
				}
			}
			var items []any
			reconcile := func(p *Preparer) {
				t.Helper()
				request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
				if _, err := p.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
			}
			lookup := func(id string) mekugiHistory {
				t.Helper()
				history, found, err := testProxyStore(proxy).lookup(transform.ctx, workspace, id)
				if err != nil || !found {
					t.Fatalf("lookup %s: found=%v, %v", id, found, err)
				}
				return history
			}
			writerCommand := "printf 'B\\n' > file.txt"
			if scenario == "gofmt-cell-yield" {
				writerCommand = "gofmt -w file.txt; go test ./..."
			} else if scenario == "gofmt-mrun-no-effect" {
				writerCommand = "gofmt -w file.txt; mrun --max-tokens 2000 make test"
			} else if scenario == "writer-first-command" || scenario == "no-continuation" {
				writerCommand = "printf 'B\\n' > file.txt; gofmt -w file.txt; go test ./..."
			}
			writerSource := "await tools.exec_command({cmd: " + string(responses.MustMarshalJSON(writerCommand)) + "});"
			writer := call("writer", writerSource)
			observe(writer)
			trace.start("thread-1", "writer-runtime", "writer", writerSource)
			trace.tool("thread-1", "writer-runtime", "writer-command", "exec_command", string(responses.MustMarshalJSON(map[string]string{"cmd": writerCommand})))
			trace.result("thread-1", "writer-command", "completed", map[string]any{"session_id": 101})
			trace.end("thread-1", "writer-runtime")
			writeTestFile(t, path, "B\n")
			items = []any{writer, output("writer")}
			if cellYield {
				items[1] = map[string]any{"type": "custom_tool_call_output", "call_id": "writer", "output": "Script running with cell ID writer-cell\nWall time 0.1 seconds\nOutput:\n"}
			}
			reconcile(proxy)

			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, items...),
				"tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
			}))
			if err != nil {
				t.Fatal(err)
			}
			transform, err = proxy.prepareRequest(t.Context(), &request, "session-1", "thread-1", codexresponses.TurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(transform.Close)
			transform.sessionShell = "bash"
			patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-B\n+C\n*** End Patch\n"
			patchSource := "await tools.apply_patch(" + string(responses.MustMarshalJSON(patch)) + ");"
			if execOverlap {
				patchSource = `await tools.exec_command({cmd: "printf 'C\\n' > file.txt"});`
			}
			patchCall := call("patch", patchSource)
			observe(patchCall)
			store := testProxyStore(proxy)
			prefixID := legacycall.ExecCallID("writer", true) + ":interval:0"
			prefix, boundaryFound, err := store.lookup(transform.ctx, workspace, prefixID)
			if err != nil {
				t.Fatal(err)
			}
			query := changes.Query{Workspace: workspace, View: "net"}
			if boundaryFound {
				query.IDs = []string{prefix.ChangeID}
				if text, err := store.changes.Read(transform.ctx, store.scoped(transform.ctx).changeScope(), query); err == nil || !strings.Contains(err.Error(), "pending") || text != "" {
					t.Fatalf("writer interval completed the host: %q, %v", text, err)
				}
			}
			trace.start("thread-1", "patch-runtime", "patch", patchSource)
			if execOverlap {
				trace.tool("thread-1", "patch-runtime", "patch-command", "exec_command", `{"cmd":"printf 'C\\n' > file.txt"}`)
			} else {
				trace.tool("thread-1", "patch-runtime", "patch-command", "apply_patch", patch)
			}
			trace.result("thread-1", "patch-command", "completed", map[string]any{"exit_code": 0})
			trace.end("thread-1", "patch-runtime")
			writeTestFile(t, path, "C\n")
			items = append(items, patchCall, output("patch"))
			if writerFirst {
				// The yielded process exits before the next request observes
				// either terminal result. Input order still lists it first.
				trace.event("thread-1", map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "writer-command", "status": "completed", "runtime_payload": trace.ref(map[string]any{"exit_code": 0})})
			}
			reconcile(proxy)
			patchID := legacycall.PatchCallID("patch", 0)
			if execOverlap {
				patchID = legacycall.ExecCallID("patch", true)
			}
			patchHistory := lookup(patchID)
			if len(patchHistory.ReviewFiles) != 1 || !strings.Contains(patchHistory.ReviewFiles[0].Diff, "-B\n+C") || strings.Contains(patchHistory.ReviewFiles[0].Diff, "-A") {
				t.Fatalf("later operation stole writer attribution: %+v", patchHistory)
			}
			if writerFirst {
				terminal := lookup(legacycall.ExecCallID("writer", true))
				if terminal.ChangeID != prefix.ChangeID || len(terminal.ReviewFiles) != 0 {
					t.Fatalf("completed writer captured the intervening edit: %+v", terminal)
				}
				query.IDs = []string{terminal.ChangeID, patchHistory.ChangeID}
				resumed, err := openMekugiReplayStore(store.directory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(resumed.snapshots.Close)
				writeTestFile(t, path, "unrelated live bytes\n")
				for _, retained := range []*mekugiReplayStore{store, resumed} {
					net, err := retained.changes.Read(transform.ctx, retained.scoped(transform.ctx).changeScope(), query)
					if err != nil || !strings.Contains(net, "-A\n+C") || strings.Contains(net, "unrelated live") {
						t.Fatalf("writer-first composition lost endpoints: %q, %v", net, err)
					}
					selection := string(responses.MustMarshalJSON(map[string]any{"sources": []any{map[string]any{"changes": query.IDs}}}))
					preload, err := compact.ReadSources(transform.ctx, selection, workspace, nil, compact.ChangeReader{Service: retained.changes, Scope: retained.scoped(transform.ctx).changeScope()})
					if err != nil || len(preload.Sources) != 1 || preload.Sources[0].ExitCode != 0 || preload.Sources[0].Stderr != "" || preload.Sources[0].Stdout != net {
						t.Fatalf("composed change preload lost evidence or status: %+v, %v", preload, err)
					}
				}
				return
			}
			if _, found, err := store.lookup(transform.ctx, workspace, legacycall.ExecCallID("writer", true)); found || err != nil {
				t.Fatalf("writer prematurely terminal: %v %v", found, err)
			}
			if scenario == "completed" {
				selected := store.scoped(transform.ctx).changeScope()
				child := selected
				child.Namespace, child.Thread = "interval-fork", "interval-fork"
				err := selected.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
					plan, err := store.changes.PrepareFork(child, selected.Namespace, child.Namespace, selected.Thread)
					if err != nil {
						return err
					}
					if err := tx.Retain(child.Thread, history.Limits{}, plan.Dependencies()...); err != nil {
						return err
					}
					return plan.Publish(tx)
				})
				if err != nil {
					t.Fatal(err)
				}
				reopened, err := openMekugiReplayStore(store.directory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(reopened.snapshots.Close)
				child.Store = reopened.changeScope().Store
				intervals, err := readExecIntervals(t.Context(), child, reopened.changes, workspace, "writer", true)
				if err != nil || len(intervals) != 1 || intervals[0].after == nil {
					t.Fatalf("pending fork/resume intervals: %+v, %v", intervals, err)
				}
				query.IDs = []string{prefix.ChangeID}
				if _, err := reopened.changes.Read(t.Context(), child, query); err == nil || !strings.Contains(err.Error(), "pending") {
					t.Fatalf("fork revived unfinished writer: %v", err)
				}
				deps, err := reopened.changes.Dependencies(child, workspace, query.IDs, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{prefixID, prefixID + ":resume"} {
					found := false
					for _, name := range deps {
						found = found || name == legacycall.Name(workspace, id, false)
					}
					if !found {
						t.Fatalf("missing pending dependency %s: %v", id, deps)
					}
				}
			}
			if scenario == "missing-prefix" {
				if err := os.Remove(filepath.Join(store.directory, legacycall.Name(workspace, prefixID, false))); err != nil {
					t.Fatal(err)
				}
				request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
				if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err == nil || !strings.Contains(err.Error(), "interval missing") {
					t.Fatalf("missing prefix silently lost: %v", err)
				}
				if _, found, err := store.lookup(transform.ctx, workspace, legacycall.ExecCallID("writer", true)); found || err != nil {
					t.Fatalf("missing prefix completed writer: %v, %v", found, err)
				}
				return
			}
			if scenario == "missing-boundary" && boundaryFound {
				if err := os.Remove(filepath.Join(store.directory, legacycall.Name(workspace, prefixID+":resume", false))); err != nil {
					t.Fatal(err)
				}
			}
			var pendingPath string
			var pendingIndex []byte
			if noEffect {
				deps, err := store.changes.Dependencies(store.scoped(transform.ctx).changeScope(), workspace, []string{prefix.ChangeID}, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range deps {
					if strings.HasPrefix(name, "changes-v3-") {
						pendingPath = filepath.Join(store.directory, name)
						pendingIndex, err = os.ReadFile(pendingPath)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			last := "D"
			if scenario == "no-continuation" || noEffect {
				last = "C"
			}
			writeTestFile(t, path, last+"\n")
			exit := 0
			if scenario == "failed" {
				exit = 1
			}
			trace.event("thread-1", map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "writer-command", "status": "completed", "runtime_payload": trace.ref(map[string]any{"exit_code": exit})})
			if scenario == "no-continuation" {
				items = append(items, map[string]any{"type": "function_call", "call_id": "writer-stdin", "name": "write_stdin", "arguments": `{"session_id":101,"chars":""}`}, map[string]any{"type": "function_call_output", "call_id": "writer-stdin", "output": nativeExecOutput("Process exited with code 0")})
			}
			if cellYield {
				items = append(items, map[string]any{"type": "function_call", "call_id": "writer-wait", "name": "wait", "arguments": `{"cell_id":"writer-cell"}`}, map[string]any{"type": "function_call_output", "call_id": "writer-wait", "output": "Script completed\nWall time 0.1 seconds\nOutput:\n"})
			}
			reconcile(proxy)
			terminal := lookup(legacycall.ExecCallID("writer", true))
			if terminal.ExecOutcome == nil || terminal.ExecOutcome.Exit == nil || *terminal.ExecOutcome.Exit != exit {
				t.Fatalf("writer terminal identity/outcome: %+v", terminal)
			}
			if noEffect {
				if terminal.ChangeID != "" || len(terminal.ReviewFiles) != 0 {
					t.Fatalf("no-effect writer kept a change: %+v", terminal)
				}
				index, err := store.changes.Inspect(store.scoped(transform.ctx).changeScope(), workspace)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := index.Changes[prefix.ChangeID]; exists {
					t.Fatal("no-effect reservation remained pending")
				}
				if notice, err := store.agentEditNotice(transform.ctx, workspace, "writer", lookup("writer")); err != nil || notice != "" {
					t.Fatalf("no-effect receipt: %q, %v", notice, err)
				}
				// Reproduce interruption after the terminal record but before index settlement.
				if err := os.WriteFile(pendingPath, pendingIndex, 0600); err != nil {
					t.Fatal(err)
				}
				reconcile(proxy)
				index, err = store.changes.Inspect(store.scoped(transform.ctx).changeScope(), workspace)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := index.Changes[prefix.ChangeID]; exists {
					t.Fatal("replay did not settle no-effect reservation")
				}
				return
			}
			query.IDs = []string{terminal.ChangeID, patchHistory.ChangeID}
			assertEvidence := func(s *mekugiReplayStore) {
				t.Helper()
				net, err := s.changes.Read(transform.ctx, s.scoped(transform.ctx).changeScope(), query)
				if scenario == "missing-boundary" {
					if err == nil || !strings.Contains(err.Error(), "unavailable") && !strings.Contains(err.Error(), "partial") || !strings.Contains(net, "+C") {
						t.Fatalf("missing boundary invented a chain: %q, %v", net, err)
					}
					endpoint, err := s.changes.Read(transform.ctx, s.scoped(transform.ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{terminal.ChangeID}, View: "history", Paths: []string{path}})
					if err != nil || !strings.Contains(endpoint, "+D\n") || !strings.Contains(endpoint, "overlapping writer endpoint unavailable") {
						t.Fatalf("named endpoint recovery lost observed bytes or uncertainty: %q, %v", endpoint, err)
					}
				} else if err != nil || strings.Count(net, "-A\n+"+last) != 1 || strings.Contains(net, "+B") {
					t.Fatalf("net intervals: %q, %v", net, err)
				}
				var orders []uint64
				for _, id := range []string{prefixID, patchID, legacycall.ExecCallID("writer", true)} {
					record, found, err := s.read(workspace, id, false)
					if err != nil || !found {
						t.Fatalf("interval record %s: %v, %v", id, found, err)
					}
					orders = append(orders, record.CaptureOrder)
				}
				if orders[0] == 0 || orders[0] >= orders[1] || scenario != "no-continuation" && orders[1] >= orders[2] {
					t.Fatalf("interval capture order: %v", orders)
				}
				snapshot, err := s.liveDiffSnapshot(transform.ctx, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread-1": true}}})
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Len() != 3 {
					t.Fatalf("duplicate/missing retained intervals: %d", snapshot.Len())
				}
				receipts := snapshot.Receipts()
				writerReceipts := 0
				for _, receipt := range receipts {
					if !slices.Contains(receipt.Calls, "writer-command") {
						continue
					}
					writerReceipts++
					if !strings.Contains(receipt.Text, "-A\n+B") || scenario != "missing-boundary" && scenario != "no-continuation" && !strings.Contains(receipt.Text, "-C\n+D") {
						t.Fatalf("writer receipt lost an interval: %s", receipt.Text)
					}
				}
				if writerReceipts != 1 {
					t.Fatalf("writer receipt count: %d", writerReceipts)
				}
				writerDetail := snapshot.ActivityDetail(activitydetail.Request{Thread: "thread-1", Item: "writer-command"})
				wantPages := 2
				if scenario == "no-continuation" {
					wantPages = 1
				}
				if len(writerDetail.Pages) != wantPages {
					t.Fatalf("writer detail lost intervals: %+v", writerDetail)
				}
				var details strings.Builder
				for _, page := range writerDetail.Pages {
					details.WriteString(page.Code)
				}
				if !strings.Contains(details.String(), "-A\n+B") || strings.Contains(details.String(), "-B\n+C") || scenario != "missing-boundary" && scenario != "no-continuation" && !strings.Contains(details.String(), "-C\n+D") {
					t.Fatalf("writer detail includes another operation: %s", details.String())
				}
				detailItem := "patch"
				if scenario == "exec-overlap" {
					detailItem = "patch-command"
				}
				patchDetail := snapshot.ActivityDetail(activitydetail.Request{Thread: "thread-1", Item: detailItem})
				if len(patchDetail.Pages) != 1 || !strings.Contains(patchDetail.Pages[0].Code, "-B\n+C") {
					t.Fatalf("patch detail: %+v", patchDetail)
				}
			}
			assertEvidence(store)
			if !boundaryFound || terminal.ChangeID != prefix.ChangeID || prefix.ExecOutcome != nil || len(prefix.ReviewFiles) != 1 || !strings.Contains(prefix.ReviewFiles[0].Diff, "-A\n+B") {
				t.Fatalf("prefix identity/attribution: %+v", prefix)
			}
			if scenario != "missing-boundary" && scenario != "no-continuation" && (len(terminal.ReviewFiles) != 1 || !strings.Contains(terminal.ReviewFiles[0].Diff, "-C\n+D")) {
				t.Fatalf("continuation attribution: %+v", terminal)
			}
			if notice, err := store.agentEditNotice(transform.ctx, workspace, "writer", lookup("writer")); err != nil || !strings.Contains(notice, prefix.ChangeID) {
				t.Fatalf("writer notice lost intervals: %q, %v", notice, err)
			}
			// The final snapshot must survive loss of process/window/trace state.
			resumed := newManagedMekugiProxy(t)
			reopened, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.snapshots.Close)
			setTestProxyStore(resumed, reopened)
			writeTestFile(t, path, "unrelated live bytes\n")
			reconcile(resumed)
			assertEvidence(reopened)
			if data, err := os.ReadFile(path); err != nil || string(data) != "unrelated live bytes\n" {
				t.Fatalf("replay repeated an effect: %q, %v", data, err)
			}
			if scenario == "failed" && terminal.ExecOutcome.Status != changecapture.StatusFailed {
				t.Fatal("failed writer was reported successful")
			}
		})
	}
}

func TestUnrelatedPatchKeepsWriterCoverageTruthful(t *testing.T) {
	for _, writerFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("writer-first=%v", writerFirst), func(t *testing.T) {
			isolateSnapshotGit(t)
			f := newMChangesSliceFixture(t, "unrelated-writer-endpoint")
			writer := testProxyWithStore(f.store, &Windows{})
			edit := testProxyWithStore(f.store, writer.Windows.ForRuntime())
			selected := observationScope(f.ctx, writer.Retained)
			own, other := filepath.Join(f.workspace, "own.go"), filepath.Join(f.workspace, "other.txt")
			writeTestFile(t, own, "package fixture\nvar value=1\n")
			writeTestFile(t, other, "other before\n")
			observation, _ := changecapture.Capture(f.ctx, []commandanalysis.Command{{Command: "gofmt -w own.go; make test", Workdir: f.workspace, Shell: "bash"}}, false, false, changecapture.Options{Directory: f.workspace, Workspaces: writer.Checkpoints})
			if observation == nil || observation.Tree == "" || len(observation.Files) != 1 {
				t.Fatal("missing named writer and fallback fixture")
			}
			original := mekugiHistory{ToolName: nativeExecCommandToolName, Root: f.workspace, ExecutingThread: f.thread, ExecObservation: observation}
			if err := publishObservations(f.ctx, selected, writer.Changes, nil, f.workspace, map[string]mekugiHistory{"writer": original}); err != nil {
				t.Fatal(err)
			}
			writer.Windows.open(&execWindow{ref: "writer", group: "writer", workspace: f.workspace, roots: observation.Roots, paths: observation.ScopePaths(), scope: selected})
			writeTestFile(t, own, "package fixture\nvar value = 1\n")
			patch := "*** Begin Patch\n*** Update File: other.txt\n@@\n-other before\n+other after\n*** End Patch\n"
			later := mekugiHistory{ToolName: applyPatchToolName, Root: f.workspace, ExecutingThread: "other-agent", NativePatches: changecapture.Patches([]string{patch}, f.workspace)}
			if err := publishObservations(f.ctx, selected, edit.Changes, nil, f.workspace, map[string]mekugiHistory{"patch": later}); err != nil {
				t.Fatal(err)
			}
			edit.Windows.open(&execWindow{ref: "patch", group: "patch", workspace: f.workspace, roots: []string{f.workspace}, paths: []string{other}, scope: selected})
			transform := &mekugiResponseTransform{owners: edit, ctx: f.ctx, directory: f.workspace, execGroup: "patch"}
			if err := transform.splitExecIntervals("patch", nil, later.NativePatches); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, other, "other after\n")
			finishWriter := func() error {
				return finalizeExecObservations(writer.Retained, writer.Changes, writer.Checkpoints, writer.Recovery, writer.PublishChanges, writer.Windows, writer.Audience, writer.Notice, f.ctx, f.workspace, []execCompletion{{callID: "writer", history: original, result: binding.ExecutionResult{Terminal: true, Succeeded: true}}})
			}
			if writerFirst {
				if err := finishWriter(); err != nil {
					t.Fatal(err)
				}
			}
			if err := edit.finalizeNativePatches(f.ctx, f.workspace, "other-agent", "patch", later, binding.ExecutionResult{Terminal: true, Succeeded: true}); err != nil {
				t.Fatal(err)
			}
			if !writerFirst {
				if err := finishWriter(); err != nil {
					t.Fatal(err)
				}
			}
			completed, found, err := f.store.lookup(f.ctx, f.workspace, legacycall.ExecCallID("writer", false))
			if err != nil || !found || completed.ExecutingThread != f.thread {
				t.Fatalf("writer attribution: %+v, %v", completed, err)
			}
			wantCoverage, wantStatus := changecapture.CoverageExact, 0
			if writerFirst {
				wantCoverage, wantStatus = changecapture.CoveragePartial, 1
			}
			stdout, stderr, status := f.run(t, "mchanges "+completed.ChangeID)
			if completed.ExecOutcome.Coverage != wantCoverage || status != wantStatus || !strings.Contains(stdout, "-var value=1\n+var value = 1\n") || strings.Contains(stdout, "+other after") || writerFirst && !strings.Contains(stderr, "overlapping writer endpoint unavailable") {
				t.Fatalf("writer coverage lost or invented effects: %q, %q, %d, %+v", stdout, stderr, status, completed.ExecOutcome)
			}
			patchHistory, found, err := f.store.lookup(f.ctx, f.workspace, legacycall.PatchCallID("patch", 0))
			if err != nil || !found || patchHistory.ExecutingThread != "other-agent" {
				t.Fatalf("patch attribution: %+v, %v", patchHistory, err)
			}
			stdout, stderr, status = f.run(t, "mchanges "+patchHistory.ChangeID)
			if status != 0 || stderr != "" || !strings.Contains(stdout, "-other before\n+other after\n") || strings.Contains(stdout, "own.go") {
				t.Fatalf("independent patch evidence: %q, %q, %d", stdout, stderr, status)
			}
		})
	}
}

func TestCheckpointWriterOverlappingCreation(t *testing.T) {
	for _, scenario := range []string{"creation", "unretained-deletion", "historical-checkpoint"} {
		t.Run(scenario, func(t *testing.T) {
			unretained := scenario == "unretained-deletion"
			isolateSnapshotGit(t)
			f := newMChangesSliceFixture(t, "overlapping-creation")
			writer := testProxyWithStore(f.store, &Windows{})
			edit := testProxyWithStore(f.store, writer.Windows.ForRuntime())
			selected := observationScope(f.ctx, writer.Retained)
			own, created := filepath.Join(f.workspace, "own.go"), filepath.Join(f.workspace, "created.go")
			writeTestFile(t, own, "package fixture\nvar value=1\n")
			if unretained {
				if writer.Checkpoints.Checkpoint(f.ctx, f.workspace) == "" {
					t.Fatal("missing seed checkpoint")
				}
				for i := range 256 {
					writeTestFile(t, filepath.Join(f.workspace, fmt.Sprintf("burst-%d", i)), "unretained\n")
				}
				writeTestFile(t, created, "existing before writer\n")
			}
			observation, _ := changecapture.Capture(f.ctx, []commandanalysis.Command{{Command: "gofmt -w own.go; make test", Workdir: f.workspace, Shell: "bash"}}, false, false, changecapture.Options{Directory: f.workspace, Workspaces: writer.Checkpoints})
			if observation == nil || observation.Tree == "" || len(observation.Files) != 1 {
				t.Fatal("missing named writer and fallback fixture")
			}
			if scenario == "historical-checkpoint" {
				observation.CheckpointOmissions = nil
			}
			original := mekugiHistory{ToolName: nativeExecCommandToolName, Root: f.workspace, ExecutingThread: f.thread, ExecObservation: observation}
			if err := publishObservations(f.ctx, selected, writer.Changes, nil, f.workspace, map[string]mekugiHistory{"writer": original}); err != nil {
				t.Fatal(err)
			}
			writer.Windows.open(&execWindow{ref: "writer", group: "writer", workspace: f.workspace, roots: observation.Roots, paths: observation.ScopePaths(), scope: selected})
			writeTestFile(t, own, "package fixture\nvar value = 1\n")
			if unretained {
				if err := os.Remove(created); err != nil {
					t.Fatal(err)
				}
			}
			patch := "*** Begin Patch\n*** Add File: created.go\n+package fixture\n+var created=1\n*** End Patch\n"
			later := mekugiHistory{ToolName: applyPatchToolName, Root: f.workspace, ExecutingThread: f.thread, NativePatches: changecapture.Patches([]string{patch}, f.workspace)}
			if err := publishObservations(f.ctx, selected, edit.Changes, nil, f.workspace, map[string]mekugiHistory{"patch": later}); err != nil {
				t.Fatal(err)
			}
			edit.Windows.open(&execWindow{ref: "patch", group: "patch", workspace: f.workspace, roots: []string{f.workspace}, paths: []string{created}, scope: selected})
			transform := &mekugiResponseTransform{owners: edit, ctx: f.ctx, directory: f.workspace, execGroup: "patch"}
			if err := transform.splitExecIntervals("patch", nil, later.NativePatches); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, created, "package fixture\nvar created=1\n")
			if err := edit.finalizeNativePatches(f.ctx, f.workspace, f.thread, "patch", later, binding.ExecutionResult{Terminal: true, Succeeded: true}); err != nil {
				t.Fatal(err)
			}
			// A named formatter overlaps the still-running test command after creation.
			formatted, _ := changecapture.Capture(f.ctx, []commandanalysis.Command{{Command: "gofmt -w created.go", Workdir: f.workspace, Shell: "bash"}}, false, false, changecapture.Options{Directory: f.workspace, Workspaces: edit.Checkpoints})
			formatHistory := mekugiHistory{ToolName: nativeExecCommandToolName, Root: f.workspace, ExecutingThread: f.thread, ExecObservation: formatted}
			if err := publishObservations(f.ctx, selected, edit.Changes, nil, f.workspace, map[string]mekugiHistory{"format": formatHistory}); err != nil {
				t.Fatal(err)
			}
			edit.Windows.open(&execWindow{ref: "format", group: "format", workspace: f.workspace, roots: formatted.Roots, paths: formatted.ScopePaths(), scope: selected})
			if err := transform.splitExecIntervals("format", formatted, nil); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, created, "package fixture\nvar created = 1\n")
			for _, completion := range []execCompletion{
				{callID: "format", history: formatHistory, result: binding.ExecutionResult{Terminal: true, Succeeded: true}},
				{callID: "writer", history: original, result: binding.ExecutionResult{Terminal: true, Succeeded: false}},
			} {
				if err := finalizeExecObservations(writer.Retained, writer.Changes, writer.Checkpoints, writer.Recovery, writer.PublishChanges, writer.Windows, writer.Audience, writer.Notice, f.ctx, f.workspace, []execCompletion{completion}); err != nil {
					t.Fatal(err)
				}
			}
			var ids []string
			for _, call := range []string{legacycall.ExecCallID("writer", false), legacycall.PatchCallID("patch", 0), legacycall.ExecCallID("format", false)} {
				completed, found, err := f.store.lookup(f.ctx, f.workspace, call)
				if err != nil || !found || completed.ChangeID == "" {
					t.Fatalf("completion %s: %+v, %v", call, completed, err)
				}
				ids = append(ids, completed.ChangeID)
			}
			stdout, stderr, status := f.run(t, "mchanges "+strings.Join(ids, " "))
			if scenario != "creation" {
				if status != 1 || !strings.Contains(stderr, "path was not retained in before checkpoint") {
					t.Fatalf("unretained deletion became complete: %q %q %d", stdout, stderr, status)
				}
				stdout, stderr, status = f.run(t, "mchanges "+strings.Join(ids, " ")+" --summary -- created.go")
				if status != 0 || stderr != "" || !strings.Contains(stdout, "?\t-\t-\tcreated.go") {
					t.Fatalf("missing unknown summary: %q %q %d", stdout, stderr, status)
				}
				stdout, stderr, status = f.run(t, "mchanges "+ids[0]+" --history -- created.go")
				if status != 0 || stderr != "" || !strings.Contains(stdout, "incomplete history") {
					t.Fatalf("lost historical gap: %q %q %d", stdout, stderr, status)
				}
				writeTestFile(t, created, "unrelated live bytes\n")
				reopened, err := openMekugiReplayStore(f.store.directory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(reopened.snapshots.Close)
				_, err = reopened.changes.Read(f.ctx, reopened.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: ids, View: "net"})
				if err == nil || !strings.Contains(err.Error(), "path was not retained in before checkpoint") {
					t.Fatalf("cold read repaired unknown history: %v", err)
				}
				return
			}
			if status != 0 || stderr != "" || !strings.Contains(stdout, "+var created = 1\n") || !strings.Contains(stdout, "-var value=1\n+var value = 1\n") {
				t.Fatalf("overlap poisoned retained composition: %q, %q, %d", stdout, stderr, status)
			}
			net := stdout
			stdout, stderr, status = f.run(t, "mchanges "+ids[0]+" -- created.go")
			if status != 0 || stderr != "" || strings.Contains(stdout, "var created") {
				t.Fatalf("writer stole overlapping creation/formatting: %q, %q, %d", stdout, stderr, status)
			}
			stdout, stderr, status = f.run(t, "mchanges "+ids[0]+" --history -- created.go")
			if status != 0 || stderr != "" || strings.Contains(stdout, "delete ") || strings.Contains(stdout, "incomplete history") {
				t.Fatalf("writer retained an invented deletion: %q, %q, %d", stdout, stderr, status)
			}
			writeTestFile(t, created, "unrelated live bytes\n")
			reopened, err := openMekugiReplayStore(f.store.directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.snapshots.Close)
			cold, err := reopened.changes.Read(f.ctx, reopened.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: ids, View: "net"})
			if err != nil || cold != net {
				t.Fatalf("cold composition changed retained evidence: %q, %v", cold, err)
			}
		})
	}
}

func TestConcurrentNamedExecIntervalPublication(t *testing.T) {
	for _, kind := range []string{"command", "patch", "writer-first-command", "writer-first-patch"} {
		t.Run(kind, func(t *testing.T) {
			writerFirst, patch := strings.HasPrefix(kind, "writer-first-"), strings.HasSuffix(kind, "patch")
			isolateSnapshotGit(t)
			f := newMChangesSliceFixture(t, "named-runtime-overlap")
			parent := testProxyWithStore(f.store, &Windows{})
			child := testProxyWithStore(f.store, parent.Windows.ForRuntime())
			path := filepath.Join(f.workspace, "file.txt")
			selected := observationScope(f.ctx, parent.Retained)
			capture := func(ref, value string, proxy *Preparer) mekugiHistory {
				writeTestFile(t, path, value)
				budget := changecapture.MaxContentBytes
				file := changecapture.ReadFile(path, &budget)
				tree := proxy.Checkpoints.Checkpoint(f.ctx, f.workspace)
				if tree == "" {
					t.Fatal("missing checkpoint")
				}
				proxy.Windows.open(&execWindow{ref: ref, group: ref, roots: []string{f.workspace}, paths: []string{path}, scope: selected})
				return mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread,
					ExecObservation: &changecapture.Observation{Commands: []commandanalysis.Command{{Command: "printf changed > file.txt", Workdir: f.workspace, Shell: "bash"}}, Files: []changecapture.File{file}, Roots: []string{f.workspace}, Tree: tree}}
			}
			earlier := capture("earlier", "A\n", parent)
			later := capture("later", "B\n", child)
			if err := publishObservations(f.ctx, selected, parent.Changes, nil, f.workspace, map[string]mekugiHistory{"earlier": earlier, "later": later}); err != nil {
				t.Fatal(err)
			}
			transform := &mekugiResponseTransform{owners: &Preparer{Config: Config{Retained: selected, Changes: parent.Changes, Windows: parent.Windows}}, ctx: f.ctx, directory: f.workspace, execGroup: "later"}
			if patch {
				later.NativePatches = changecapture.Patches([]string{"*** Begin Patch\n*** Update File: file.txt\n@@\n-B\n+C\n*** End Patch\n"}, f.workspace)
			}
			if err := transform.splitExecIntervals("later", later.ExecObservation, later.NativePatches); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, path, "C\n")
			finalize := func(ctx context.Context, ref string, proxy *Preparer, h mekugiHistory, publish func([]changes.Update)) error {
				if ref == "later" && patch {
					proxy.PublishChanges = publish
					return proxy.finalizeNativePatches(ctx, f.workspace, f.thread, ref, h, binding.ExecutionResult{Terminal: true, Succeeded: true})
				}
				return finalizeExecObservations(proxy.Retained, proxy.Changes, proxy.Checkpoints, proxy.Recovery, publish, proxy.Windows, proxy.Audience, proxy.Notice, ctx, f.workspace,
					[]execCompletion{{callID: ref, history: h, result: binding.ExecutionResult{Terminal: true, Succeeded: true}}})
			}
			if writerFirst {
				// The later edit has no witnessed endpoint when the earlier
				// runtime finishes. Keep the endpoint, not invented attribution.
				if err := finalize(f.ctx, "earlier", parent, earlier, nil); err != nil {
					t.Fatal(err)
				}
				if err := finalize(f.ctx, "later", child, later, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				published, release := make(chan struct{}), make(chan struct{})
				laterDone, earlierDone := make(chan error, 1), make(chan error, 1)
				go func() {
					laterDone <- finalize(ctx, "later", child, later, func([]changes.Update) {
						cancel() // Publication must already include the continuation endpoint.
						close(published)
						<-release
					})
				}()
				select {
				case <-published:
				case <-time.After(5 * time.Second):
					t.Fatal("later publication not reached")
				}
				parent.Windows.state().mu.Lock()
				window := parent.Windows.find("earlier")
				parent.Windows.state().mu.Unlock()
				if window.intervalMu.TryLock() {
					window.intervalMu.Unlock()
					close(release)
					<-laterDone
					t.Fatal("later finalizer must lock the interrupted writer before workspace publication")
				}
				resumeID := execIntervalID("earlier", false, 0) + ":resume"
				resume, found, err := lookupObservation(f.ctx, selected, f.workspace, resumeID)
				if err != nil || !found || resume.ExecObservation == nil || len(resume.ExecObservation.Files) != 1 || resume.ExecObservation.Files[0].Content != "C\n" {
					t.Errorf("terminal publication lost its endpoint: found=%v error=%v", found, err)
				}
				go func() { earlierDone <- finalize(f.ctx, "earlier", parent, earlier, nil) }()
				close(release)
				for _, done := range []<-chan error{laterDone, earlierDone} {
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("overlapping finalizers deadlocked")
					}
				}
			}
			writeTestFile(t, path, "unrelated live bytes\n")
			if err := finalize(f.ctx, "later", child, later, nil); err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, ref := range []string{"earlier", "later"} {
				id := legacycall.ExecCallID(ref, false)
				if ref == "later" && patch {
					id = legacycall.PatchCallID(ref, 0)
				}
				record, found, err := selected.Store.LookupHostCall(f.ctx, f.workspace, id)
				if err != nil || !found || record.ChangeID == "" {
					t.Fatalf("terminal %s: found=%v error=%v", ref, found, err)
				}
				ids = append(ids, record.ChangeID)
			}
			net, err := parent.Changes.Read(f.ctx, selected, changes.Query{Workspace: f.workspace, IDs: ids, View: "net"})
			if writerFirst {
				if err == nil || !strings.Contains(err.Error(), "partial captured effects") {
					t.Fatalf("unwitnessed overlap claimed complete attribution: %q, %v", net, err)
				}
				endpoint, err := parent.Changes.Read(f.ctx, selected, changes.Query{Workspace: f.workspace, IDs: ids[:1], View: "history", Paths: []string{path}})
				if err != nil || !strings.Contains(endpoint, "+C\n") || !strings.Contains(endpoint, "overlapping writer endpoint unavailable") || strings.Contains(endpoint, "unrelated live") {
					t.Fatalf("writer-first endpoint is not independently recoverable: %q, %v", endpoint, err)
				}
				return
			}
			if err != nil || !strings.Contains(net, "-A\n+C") || strings.Contains(net, "unrelated live") {
				t.Fatalf("retained concurrent composition: %q, %v", net, err)
			}
		})
	}
}
