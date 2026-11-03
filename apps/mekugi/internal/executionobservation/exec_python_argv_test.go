package executionobservation

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func TestPythonArgvExternalEditsAndCreations(t *testing.T) {
	python, binary := interpreterForTest("python3", "python")
	if binary == "" {
		t.Skip("Python runtime unavailable")
	}
	for _, form := range []string{"heredoc", "command", "escaped quotes", "ANSI-C quotes", "script"} {
		t.Run(form, func(t *testing.T) {
			workspace, external := t.TempDir(), t.TempDir()
			writeTestFile(t, filepath.Join(external, "state.json"), "before\n")
			value := "sys.argv[1]"
			if form == "command" {
				value = `__import__("sys").argv[1]`
			}
			source := "import sys, pathlib\nroot = pathlib.Path(" + value + ")\n(root/\"state.json\").write_text(\"after\\n\")\n(root/\"created.json\").write_text(\"created\\n\")\n"
			if form == "escaped quotes" || form == "ANSI-C quotes" {
				source = "import sys, pathlib\nroot = pathlib.Path(sys.argv[1])\n(root/'state.json').write_text('after\\n')\n(root/'created.json').write_text('created\\n')\n"
			}
			command := python + " - " + commandargs.QuoteArgument(external) + " <<'PY'\n" + source + "PY"
			if form == "command" || form == "escaped quotes" {
				command = python + " -c " + commandargs.Quote(source) + " " + commandargs.QuoteArgument(external)
			} else if form == "ANSI-C quotes" {
				command = python + " -c " + commandargs.QuoteArgument(source) + " " + commandargs.QuoteArgument(external)
			} else if form == "script" {
				writeTestFile(t, filepath.Join(workspace, "edit.py"), source)
				command = python + " edit.py " + commandargs.QuoteArgument(external)
			}
			observation := captureInterpreterTestObservation(t, workspace, command)
			assertCapturedExecVCSTestFile(t, observation, filepath.Join(external, "state.json"), "before\n")
			if !slices.ContainsFunc(observation.Files, func(file changecapture.File) bool {
				return file.Path == filepath.Join(external, "created.json") && file.Kind == changecapture.Absent
			}) {
				t.Fatalf("missing creation baseline: %+v", observation.Files)
			}
			host := exec.Command("bash", "-c", command)
			host.Dir = workspace
			if output, err := host.CombinedOutput(); err != nil {
				t.Fatalf("Python write: %v: %s", err, output)
			}
			relative, err := filepath.Rel(workspace, external)
			if err != nil {
				t.Fatal(err)
			}
			assertExactInterpreterReviews(t, workspace, observation, map[string][]string{
				filepath.Join(relative, "state.json"):   {"-before\n+after\n"},
				filepath.Join(relative, "created.json"): {"+created\n"},
			})
		})
	}
}

func TestPythonArgvMixedWorktreesRetainsComputedSiblings(t *testing.T) {
	python, binary := interpreterForTest("python3", "python")
	if binary == "" {
		t.Skip("Python runtime unavailable")
	}
	repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "tracked\n"})
	f := newMChangesSliceFixture(t, "python-argv")
	external, worktrees := t.TempDir(), t.TempDir()
	before := "{\"pendingQuestion\":\"question\",\"proposedGroups\":[{\"name\":\"one\"},{\"name\":\"two\"},{\"name\":\"three\"},{\"name\":\"four\"},{\"name\":\"five\"}]}\n"
	writeTestFile(t, filepath.Join(external, "state.json"), before)
	source := `import json,pathlib
root=pathlib.Path(__import__("sys").argv[1])
p=root/"state.json"; state=json.loads(p.read_text())
state["pendingQuestion"]=None
for group in state["proposedGroups"]:
 (root/(group["name"]+"-context.json")).write_text(json.dumps(group,indent=2)+"\n")
p.write_text(json.dumps(state,indent=2)+"\n")
`
	command := python + " - " + commandargs.QuoteArgument(external) + " <<'PY'\n" + source + "PY\n"
	for _, name := range []string{"first", "second"} {
		command += "git -C " + commandargs.QuoteArgument(repo) + " worktree add --detach " + commandargs.QuoteArgument(filepath.Join(worktrees, name)) + " HEAD\n"
	}
	observation := captureInterpreterTestObservationAnyClass(t, f.workspace, command)
	assertCapturedExecVCSTestFile(t, observation, filepath.Join(external, "state.json"), before)
	if !slices.ContainsFunc(observation.Listings, func(listing changecapture.Listing) bool { return listing.Root == external && listing.Entries != nil }) {
		t.Fatalf("no bounded before-listing for computed siblings: %+v", observation)
	}
	if _, err := os.Stat(filepath.Join(worktrees, "first")); !os.IsNotExist(err) {
		t.Fatalf("capture executed worktree creation: %v", err)
	}
	host := exec.Command("bash", "-c", command)
	host.Dir = f.workspace
	if output, err := host.CombinedOutput(); err != nil {
		t.Fatalf("mixed command: %v: %s", err, output)
	}
	proxy := testProxyWithStore(f.store)
	history := mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread, ExecObservation: observation}
	if err := finalizeExecObservations(proxy.Retained, proxy.Changes, proxy.Checkpoints, proxy.Recovery, proxy.PublishChanges, proxy.Windows, proxy.Audience, proxy.Notice, f.ctx, f.workspace, []execCompletion{{callID: "argv-write", history: history, result: binding.DecodeExecutionResult(history.ToolName, responses.MustMarshalJSON(map[string]any{"exit_code": 0}))}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := reopened.lookup(f.ctx, f.workspace, legacycall.ExecCallID("argv-write", false))
	if err != nil || !found || record.ChangeID == "" || len(record.ReviewFiles) != 6 || record.ExecOutcome.Coverage != changecapture.CoveragePartial {
		t.Fatalf("durable argv evidence: %+v, %v", record, err)
	}
	for _, file := range record.ReviewFiles {
		if file.Incomplete != "" || file.Origin != "" || !strings.HasPrefix(file.AfterPath, external+string(filepath.Separator)) {
			t.Errorf("external authored evidence: %+v", file)
		}
		if file.AfterPath == filepath.Join(external, "state.json") && !strings.Contains(file.Diff, "-"+strings.TrimSuffix(before, "\n")) {
			t.Errorf("state before-image lost: %s", file.Diff)
		}
	}
	net, err := reopened.changes.Read(f.ctx, reopened.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: []string{record.ChangeID}, View: "history"})
	if err != nil || !strings.Contains(net, "+  \"pendingQuestion\": null") {
		t.Fatalf("retained state after-image missing: %q, %v", net, err)
	}
	for _, name := range []string{"one", "two", "three", "four", "five"} {
		if !strings.Contains(net, name+"-context.json") || !strings.Contains(net, `+  "name": "`+name+`"`) {
			t.Errorf("retained sibling %s missing: %s", name, net)
		}
	}
}

func TestPythonArgvUnresolvedWriteKeepsKnownEvidencePartial(t *testing.T) {
	f := newMChangesSliceFixture(t, "pending-argv")
	workspace, external := f.workspace, t.TempDir()
	path := filepath.Join(external, "state.json")
	writeTestFile(t, path, "before\n")
	command := "python3 -c " + commandargs.Quote("import sys\nfrom pathlib import Path\nPath(sys.argv[1]).write_text(\"after\")\nPath(sys.argv[index()]).write_text(\"unknown\")") + " " + commandargs.QuoteArgument(path)
	observation := captureInterpreterTestObservationAnyClass(t, workspace, command)
	if err := f.store.put(f.ctx, workspace, map[string]mekugiHistory{"pending": {ToolName: nativeExecCommandToolName, ExecutingThread: f.thread, ExecObservation: observation}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	pending, found, err := reopened.lookup(f.ctx, workspace, "pending")
	if err != nil || !found || pending.ExecObservation == nil {
		t.Fatalf("pending observation not restored: %+v, %v", pending, err)
	}
	writeTestFile(t, path, "after\n")
	result := changecapture.Reconcile(t.Context(), *pending.ExecObservation, changecapture.Comparison{})
	if result.Complete || result.Coverage != changecapture.CoveragePartial || !strings.Contains(result.ScopeReason, "unresolved interpreter targets") || len(result.Files) != 1 || !strings.Contains(result.Files[0].Diff, "-before\n+after\n") {
		t.Fatalf("unresolved write erased uncertainty or known evidence: %+v", result)
	}
}

func TestPythonArgvModuleMutationLeavesExternalCoveragePartial(t *testing.T) {
	python, binary := interpreterForTest("python3", "python")
	if binary == "" {
		t.Skip("Python runtime unavailable")
	}
	for _, mode := range []string{"module dictionary", "wildcard import"} {
		t.Run(mode, func(t *testing.T) {
			workspace, supplied, actual := t.TempDir(), t.TempDir(), t.TempDir()
			writeTestFile(t, filepath.Join(supplied, "state.json"), "decoy\n")
			writeTestFile(t, filepath.Join(actual, "state.json"), "before\n")
			writeTestFile(t, filepath.Join(workspace, "known.txt"), "known before\n")
			prefix := "import sys\nsys.__dict__.update(argv=['-'," + strconv.Quote(actual) + "])\n"
			if mode == "wildcard import" {
				writeTestFile(t, filepath.Join(workspace, "fake.py"), "from types import SimpleNamespace\nsys=SimpleNamespace(argv=['-',"+strconv.Quote(actual)+"])\n")
				prefix = "import sys\nfrom fake import *\n"
			}
			source := prefix + "from pathlib import Path\nPath('known.txt').write_text('known after\\n')\n(Path(sys.argv[1])/'state.json').write_text('after\\n')\n"
			command := python + " - " + commandargs.QuoteArgument(supplied) + " <<'PY'\n" + source + "PY"
			observation := captureInterpreterTestObservationAnyClass(t, workspace, command)
			if slices.ContainsFunc(observation.Files, func(file changecapture.File) bool { return file.Path == filepath.Join(supplied, "state.json") }) {
				t.Fatal("capture trusted argv after an unsupported module mutation")
			}
			host := exec.Command("bash", "-c", command)
			host.Dir = workspace
			if output, err := host.CombinedOutput(); err != nil {
				t.Fatalf("Python mutation: %v: %s", err, output)
			}
			content, err := os.ReadFile(filepath.Join(actual, "state.json"))
			if err != nil || string(content) != "after\n" {
				t.Fatalf("external mutation missing: %q, %v", content, err)
			}
			result := changecapture.Reconcile(t.Context(), *observation, changecapture.Comparison{})
			if result.Complete || result.Coverage != changecapture.CoveragePartial || len(result.Files) != 1 || !strings.Contains(result.Files[0].Diff, "-known before\n+known after\n") {
				t.Fatalf("unknown external write claimed complete or lost known effect: %+v", result)
			}
		})
	}
}
