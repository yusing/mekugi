package execscope

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

func TestPythonArgvPathsAndShadowing(t *testing.T) {
	for _, test := range []struct {
		name, prefix, value string
		open                bool
	}{
		{"module", "import sys", "sys.argv[1]", false},
		{"alias", "import sys as system", "system.argv[1]", false},
		{"dynamic import", "", `__import__("sys").argv[1]`, false},
		{"unimported", "", "sys.argv[1]", true},
		{"module shadow", "import sys\nsys = unknown()", "sys.argv[1]", true},
		{"import shadow", "__import__ = unknown()", `__import__("sys").argv[1]`, true},
		{"import alias shadow", "import sys as __import__", `__import__("sys").argv[1]`, true},
		{"wildcard import", "import sys\nfrom unknown import *", "sys.argv[1]", true},
		{"module dictionary", "import sys\nsys.__dict__.update(argv=unknown())", "sys.argv[1]", true},
		{"parameter shadow", "import sys\ndef f(sys):\n pass", "sys.argv[1]", true},
		{"loop shadow", "import sys\nfor sys in unknown():\n pass", "sys.argv[1]", true},
		{"module escape", "import sys\nmutate(sys)", "sys.argv[1]", true},
		{"argv replacement", "import sys\nsys.argv = unknown()", "sys.argv[1]", true},
		{"argv item mutation", "import sys\nsys.argv[1] = unknown()", "sys.argv[1]", true},
		{"argv method mutation", "import sys\nsys.argv.reverse()", "sys.argv[1]", true},
		{"argv escape", "import sys\nmutate(sys.argv)", "sys.argv[1]", true},
		{"dynamic module escape", `mutate(__import__("sys"))`, `__import__("sys").argv[1]`, true},
		{"missing argument", "import sys", "sys.argv[2]", true},
		{"dynamic index", "import sys", "sys.argv[index()]", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := test.prefix + "\nfrom pathlib import Path\nroot = Path(" + test.value + ")\n(root / 'state.json').write_text('new')"
			result := inspectExecSource(execProviderInput{cwd: root, pythonArgv: []string{"-", root}, deadline: time.Now().Add(time.Second)}, source, "", sourceparse.Python, true)
			var paths []string
			for _, entry := range result.scope {
				for _, operand := range entry.Operands {
					paths = append(paths, operand.Path)
				}
			}
			var want []string
			if !test.open {
				want = []string{filepath.Join(root, "state.json")}
			}
			if result.open != test.open || !slices.Equal(paths, want) {
				t.Fatalf("scope=%v open=%v; want %v open=%v", paths, result.open, want, test.open)
			}
		})
	}
}

func TestPythonInvocationArguments(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	source := "import sys\nfrom pathlib import Path\n(Path(sys.argv[1]) / sys.argv[2]).write_text(\"new\")"
	writeTestFile(t, filepath.Join(workspace, "edit.py"), source)
	for _, command := range []string{
		"python3 - " + commandargs.QuoteArgument(external) + " state.json <<'PY'\n" + source + "\nPY",
		"python3 -c " + commandargs.Quote(source) + " " + commandargs.QuoteArgument(external) + " state.json",
		"python3 -W ignore -X utf8 edit.py " + commandargs.QuoteArgument(external) + " state.json",
		"python3 -- edit.py " + commandargs.QuoteArgument(external) + " state.json",
		"cat > created.py <<'PY'\n" + source + "\nPY\npython3 created.py " + commandargs.QuoteArgument(external) + " state.json",
	} {
		plan := Classify(Input{Command: command, Directory: workspace, Shell: "bash", AuthoredOnly: true})
		if plan.Unresolved || !slices.ContainsFunc(plan.Scope, func(entry Entry) bool {
			return slices.Contains(entry.Operands, Operand{Path: filepath.Join(external, "state.json")})
		}) {
			t.Fatalf("argv target missing for %q: %+v", command, plan)
		}
	}
}

func TestPythonArgvUncertaintySurvivesSourceWrappers(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "edit.py"), "import sys\nfrom pathlib import Path\n(Path(sys.argv[1])/\"state.json\").write_text(\"new\")\nPath(sys.argv[index()]).write_text(\"unknown\")\n")
	command := "python3 edit.py " + commandargs.QuoteArgument(external)
	for _, wrapped := range []string{
		command,
		"bash -c " + commandargs.Quote(command),
		"rtk run -c " + commandargs.Quote(command),
		"python3 -c " + commandargs.Quote("import subprocess; subprocess.run([\"python3\", \"edit.py\", "+strconv.Quote(external)+"])"),
	} {
		plan := Classify(Input{Command: wrapped, Directory: workspace, Shell: "bash", AuthoredOnly: true})
		if !plan.Unresolved || !slices.ContainsFunc(plan.Scope, func(entry Entry) bool {
			return slices.Contains(entry.Operands, Operand{Path: filepath.Join(external, "state.json")})
		}) {
			t.Fatalf("wrapper lost known scope or unresolved coverage for %q: %+v", wrapped, plan)
		}
	}
}
