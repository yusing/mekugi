package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestPythonInterpreterLiteralAndRecursiveWritesExact(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "src", "one.py"), "old one\n")
	writeTestFile(t, filepath.Join(workspace, "src", "nested", "two.py"), "old two\n")
	script := `from pathlib import Path
with open("open-created.txt", "w", encoding="utf-8") as output:
    output.write("open content\n")
Path("path-created.txt").write_text("path content\n")
for source in Path("src").rglob("*.py"):
    source.write_text(source.read_text().replace("old", "new"))
Path("capture-marker.txt").write_text("executed\n")
`
	writeTestFile(t, filepath.Join(workspace, "edit.py"), script)
	pythonName, pythonBinary := interpreterForTest("python3", "python")
	observation := captureInterpreterTestObservation(t, workspace, pythonName+" edit.py")
	assertInterpreterCaptureWasPassive(t, filepath.Join(workspace, "capture-marker.txt"))

	runOrSimulateInterpreter(t, pythonBinary, workspace, "edit.py", func() {
		writeTestFile(t, filepath.Join(workspace, "open-created.txt"), "open content\n")
		writeTestFile(t, filepath.Join(workspace, "path-created.txt"), "path content\n")
		writeTestFile(t, filepath.Join(workspace, "src", "one.py"), "new one\n")
		writeTestFile(t, filepath.Join(workspace, "src", "nested", "two.py"), "new two\n")
		writeTestFile(t, filepath.Join(workspace, "capture-marker.txt"), "executed\n")
	})
	assertExactInterpreterReviews(t, workspace, observation,
		map[string][]string{
			"open-created.txt":   {"+open content"},
			"path-created.txt":   {"+path content"},
			"src/one.py":         {"-old one", "+new one"},
			"src/nested/two.py":  {"-old two", "+new two"},
			"capture-marker.txt": {"+executed"},
		},
	)
}

func TestPythonInterpreterSequentialPathReassignmentCapturesEveryTarget(t *testing.T) {
	workspace := t.TempDir()
	for _, file := range []string{"a", "b", "c"} {
		writeTestFile(t, filepath.Join(workspace, file), "old "+file+"\n")
	}
	script := `from pathlib import Path
p = Path('a')
s = p.read_text()
s = s.replace('old', 'new')
p.write_text(s)
p = Path('b')
p.write_text(p.read_text().replace('old', 'new'))
p = Path('c')
s = p.read_text(); s = s.replace('old', 'new'); p.write_text(s)
`
	writeTestFile(t, filepath.Join(workspace, "edit.py"), script)
	pythonName, pythonBinary := interpreterForTest("python3", "python")
	observation := captureInterpreterTestObservation(t, workspace, pythonName+" edit.py")
	wantBaselines := map[string]bool{"a": true, "b": true, "c": true, "edit.py": true}
	for _, baseline := range observation.Files {
		relative, err := filepath.Rel(workspace, baseline.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !wantBaselines[relative] {
			t.Errorf("unexpected interpreter baseline %q, want only actual targets and the script", relative)
		}
		delete(wantBaselines, relative)
	}
	if len(wantBaselines) != 0 {
		t.Fatalf("missing interpreter target baselines: %v", wantBaselines)
	}

	runOrSimulateInterpreter(t, pythonBinary, workspace, "edit.py", func() {
		for _, file := range []string{"a", "b", "c"} {
			writeTestFile(t, filepath.Join(workspace, file), "new "+file+"\n")
		}
	})
	assertExactInterpreterReviews(t, workspace, observation, map[string][]string{
		"a": {"-old a", "+new a"},
		"b": {"-old b", "+new b"},
		"c": {"-old c", "+new c"},
	})
}

func TestRTKProxyPythonCaptureKeepsBaselineAndDirectOrigin(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "target.txt")
	writeTestFile(t, target, "before\n")
	writeTestFile(t, filepath.Join(workspace, "edit.py"), `from pathlib import Path
Path("target.txt").write_text("after\n")
`)
	command := "env -u BASH_ENV rtk --ultra-compact --skip-env proxy --skip-env --ultra-compact -- python3 edit.py"
	observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
	if !observed || observation == nil || observation.Class != execScoped.String() {
		t.Fatalf("RTK proxy interpreter capture = %+v observed=%v", observation, observed)
	}
	var baseline *execFileSnapshot
	for i := range observation.Files {
		if observation.Files[i].Path == target {
			baseline = &observation.Files[i]
			break
		}
	}
	if baseline == nil || baseline.Kind != execFileText || baseline.Content != "before\n" || baseline.Origin != "" {
		t.Fatalf("literal target baseline = %+v; want direct pre-call content", baseline)
	}
	if !slices.ContainsFunc(observation.Programs, func(program execProgram) bool {
		return program.Label == "python3" && program.Direct
	}) {
		t.Fatalf("wrapped interpreter lacks Direct program attribution: %+v", observation.Programs)
	}

	_, pythonBinary := interpreterForTest("python3", "python")
	runOrSimulateInterpreter(t, pythonBinary, workspace, "edit.py", func() {
		writeTestFile(t, target, "after\n")
	})
	reviews, complete, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || len(reviews) != 1 || reviews[0].AfterPath != target || reviews[0].Origin != "" ||
		!strings.Contains(reviews[0].Diff, "-before\n+after\n") {
		t.Fatalf("RTK proxy direct review = %+v complete=%v coverage=%q", reviews, complete, coverage)
	}
}

func TestRTKProxyGofmtCaptureScopesManagedGoFiles(t *testing.T) {
	workspace := t.TempDir()
	goFile := filepath.Join(workspace, "main.go")
	writeTestFile(t, goFile, "package sample\nfunc main(){println(\"before\")}\n")
	writeTestFile(t, filepath.Join(workspace, "notes.txt"), "not go\n")
	observation, observed := captureExecObservation([]execCommandInput{{Command: "rtk proxy gofmt -w .", Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
	if !observed || observation == nil || observation.Class != execScoped.String() {
		t.Fatalf("RTK gofmt capture = %+v observed=%v", observation, observed)
	}
	if len(observation.Files) != 1 || observation.Files[0].Path != goFile || observation.Files[0].Origin != "gofmt" {
		t.Fatalf("gofmt baseline scope = %+v; want only managed .go baseline", observation.Files)
	}
	writeTestFile(t, goFile, "package sample\n\nfunc main() { println(\"after\") }\n")
	reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || len(reviews) != 1 || reviews[0].AfterPath != goFile || reviews[0].Origin != "gofmt" {
		t.Fatalf("gofmt managed reviews = %+v complete=%v", reviews, complete)
	}
}

func TestPythonInterpreterComputedTargetDoesNotClaimWorkspaceWrite(t *testing.T) {
	workspace := t.TempDir()
	pythonName, _ := interpreterForTest("python3", "python")
	command := pythonName + " -c 'import os; from pathlib import Path; target = os.environ.get(" +
		"\"MEKUGI_INTERPRETER_TEST_TARGET\"); Path(target).write_text(\"computed content\\n\")'"
	observation := captureInterpreterTestObservationAnyClass(t, workspace, command)
	writeTestFile(t, filepath.Join(workspace, "computed-result.txt"), "computed content\n")

	reviews, _, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if len(reviews) != 0 {
		t.Fatalf("computed target claimed an undeclared write: %+v", reviews)
	}
}

func TestNodeInterpreterFSAliasesPromisesAndRecursiveScopeExact(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "src", "one.js"), "old one\n")
	writeTestFile(t, filepath.Join(workspace, "src", "nested", "two.js"), "old two\n")
	script := `import fs, { promises as fsPromises } from "node:fs";
fs.readdirSync("src", { recursive: true });
await fsPromises.writeFile("src/one.js", "new one\n");
await fs.promises.writeFile("src/nested/two.js", "new two\n");
await fsPromises.writeFile("node-created.txt", "node content\n");
await fsPromises.writeFile("capture-marker.txt", "executed\n");
`
	writeTestFile(t, filepath.Join(workspace, "edit.mjs"), script)
	nodeBinary, _ := exec.LookPath("node")
	observation := captureInterpreterTestObservation(t, workspace, "node edit.mjs")
	assertInterpreterCaptureWasPassive(t, filepath.Join(workspace, "capture-marker.txt"))

	runNodeOrSimulate(t, nodeBinary, workspace, func() {
		writeTestFile(t, filepath.Join(workspace, "src", "one.js"), "new one\n")
		writeTestFile(t, filepath.Join(workspace, "src", "nested", "two.js"), "new two\n")
		writeTestFile(t, filepath.Join(workspace, "node-created.txt"), "node content\n")
		writeTestFile(t, filepath.Join(workspace, "capture-marker.txt"), "executed\n")
	})
	assertExactInterpreterReviews(t, workspace, observation,
		map[string][]string{
			"src/one.js":         {"-old one", "+new one"},
			"src/nested/two.js":  {"-old two", "+new two"},
			"node-created.txt":   {"+node content"},
			"capture-marker.txt": {"+executed"},
		},
	)
}

func captureInterpreterTestObservation(t *testing.T, workspace, command string) *execObservation {
	t.Helper()
	observation := captureInterpreterTestObservationAnyClass(t, workspace, command)
	if observation.Class != execScoped.String() {
		t.Fatalf("interpreter class for %q = %q, want scoped", command, observation.Class)
	}
	return observation
}

func captureInterpreterTestObservationAnyClass(t *testing.T, workspace, command string) *execObservation {
	t.Helper()
	observation, observed := captureExecObservation([]execCommandInput{{
		Command: command, Workdir: workspace, Shell: "bash",
	}}, false, false, execCaptureEnv{directory: workspace})
	if !observed || observation == nil {
		t.Fatalf("interpreter command %q was not captured", command)
	}
	return observation
}

func assertInterpreterCaptureWasPassive(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("capture evaluated interpreter code: marker stat error = %v", err)
	}
}

func interpreterForTest(candidates ...string) (name, binary string) {
	for _, candidate := range candidates {
		if binary, err := exec.LookPath(candidate); err == nil {
			return candidate, binary
		}
	}
	return candidates[0], ""
}

func runOrSimulateInterpreter(t *testing.T, binary, workspace, script string, simulate func()) {
	t.Helper()
	if binary == "" {
		t.Log("Python runtime unavailable; simulating post-capture filesystem writes")
		simulate()
		return
	}
	command := exec.Command(binary, script)
	command.Dir = workspace
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run interpreter: %v\n%s", err, output)
	}
}

func runNodeOrSimulate(t *testing.T, binary, workspace string, simulate func()) {
	t.Helper()
	if binary == "" || !nodeSupportsRecursiveReaddir(binary, workspace) {
		t.Log("Node recursive readdir unavailable; simulating post-capture filesystem writes")
		simulate()
		return
	}
	command := exec.Command(binary, "edit.mjs")
	command.Dir = workspace
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run node script: %v\n%s", err, output)
	}
}

func nodeSupportsRecursiveReaddir(binary, workspace string) bool {
	command := exec.Command(binary, "--input-type=module", "-e", `import fs from "node:fs"; fs.readdirSync(process.cwd(), { recursive: true });`)
	command.Dir = workspace
	return command.Run() == nil
}

func assertExactInterpreterReviews(t *testing.T, workspace string, observation *execObservation, want map[string][]string) {
	t.Helper()
	reviews, complete, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact {
		t.Fatalf("interpreter evidence complete=%v coverage=%q files=%+v; want exact", complete, coverage, reviews)
	}
	byPath := make(map[string]mekugi.ReviewFile, len(reviews))
	for _, review := range reviews {
		path := cmpOrPath(review.AfterPath, review.BeforePath)
		relative, err := filepath.Rel(workspace, path)
		if err != nil {
			t.Fatal(err)
		}
		if review.Origin != "" {
			t.Errorf("direct interpreter path %s has managed origin %q", relative, review.Origin)
		}
		byPath[relative] = review
	}
	if len(byPath) != len(want) {
		t.Fatalf("interpreter review count = %d, want %d: %+v", len(byPath), len(want), byPath)
	}
	for path, snippets := range want {
		review, found := byPath[path]
		if !found {
			t.Errorf("missing interpreter review for %s", path)
			continue
		}
		for _, snippet := range snippets {
			if !strings.Contains(review.Diff, snippet) {
				t.Errorf("review for %s does not include %q: %q", path, snippet, review.Diff)
			}
		}
	}
}
