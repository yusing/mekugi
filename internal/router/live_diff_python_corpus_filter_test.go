package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"mvdan.cc/sh/v3/syntax"
)

// Raw counts deduplicated regex-candidate shell calls, while Invocations and
// Eligible count individual Python scripts. They intentionally need not match.
// UnknownIntent remains eligible. Uncertain shell/source extraction is outside
// the measurable denominator and is reported, never silently excluded.
type pythonCorpusPopulation struct {
	Raw, Invocations, Eligible, NonInvocation, Analysis, Uncertain, UnknownIntent, PythonParseUncertainty int
	Uncertainty                                                                                           map[string]int
}

func pythonCorpusShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func pythonCorpusExtract(command pythonCorpusCommand) ([]pythonCorpusCommand, int) {
	commands, reasons := pythonCorpusExtractDetailed(command)
	total := 0
	for _, count := range reasons {
		total += count
	}
	return commands, total
}

func pythonCorpusExtractDetailed(command pythonCorpusCommand) ([]pythonCorpusCommand, map[string]int) {
	var result []pythonCorpusCommand
	uncertain := make(map[string]int)
	var shell func(string, string, func(string) string, int)
	shell = func(program, directory string, wrap func(string) string, depth int) {
		if depth > 12 {
			uncertain["invocation/source uncertainty"]++
			return
		}
		statements, directory, partial, ok := liveDiffShellStatements(program, directory)
		if !ok || partial {
			uncertain["shell parse uncertainty"]++
			return
		}
		var visit func(*syntax.Stmt)
		visit = func(stmt *syntax.Stmt) {
			if next, ok := liveDiffShellCd(stmt, directory); ok {
				directory = next
				return
			}
			if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
				visit(binary.X)
				visit(binary.Y)
				return
			}
			if sub, ok := stmt.Cmd.(*syntax.Subshell); ok {
				saved := directory
				for _, nested := range sub.Stmts {
					visit(nested)
				}
				directory = saved
				return
			}
			call, ok := stmt.Cmd.(*syntax.CallExpr)
			if !ok {
				// Complex shell control flow cannot establish an executable invocation.
				var rendered bytes.Buffer
				_ = syntax.NewPrinter().Print(&rendered, stmt)
				if pythonCorpusInline.MatchString(rendered.String()) {
					uncertain["invocation/source uncertainty"]++
				}
				return
			}
			if len(call.Args) == 0 {
				return
			}
			name, literal := shellCatLiteral(call.Args[0])
			if !literal {
				uncertain["invocation/source uncertainty"]++
				return
			}
			args := make([]string, len(call.Args))
			for i, word := range call.Args {
				value, ok := shellCatLiteral(word)
				if !ok {
					if strings.HasPrefix(filepath.Base(name), "python") || filepath.Base(name) == "env" || filepath.Base(name) == "bash" || filepath.Base(name) == "sh" {
						uncertain["invocation/source uncertainty"]++
					}
					return
				}
				args[i] = value
			}
			index := 0
			if filepath.Base(name) == "env" {
				index++
				for index < len(args) {
					arg := args[index]
					if arg == "-u" || arg == "--unset" {
						index += 2
						continue
					}
					if arg == "--" || arg == "-i" || arg == "--ignore-environment" || strings.Contains(arg, "=") {
						index++
						continue
					}
					if strings.HasPrefix(arg, "-") {
						uncertain["invocation/source uncertainty"]++
						return
					}
					break
				}
				if index >= len(args) {
					return
				}
				name = args[index]
			}
			identity := shellInterpreterName(name)
			if identity == "bash" || identity == "sh" {
				if len(args) != index+3 || (args[index+1] != "-c" && args[index+1] != "-lc") {
					uncertain["invocation/source uncertainty"]++
					return
				}
				prefix := make([]string, index+2)
				for i := range prefix {
					prefix[i] = pythonCorpusShellQuote(args[i])
				}
				shell(args[index+2], directory, func(s string) string { return wrap(strings.Join(prefix, " ") + " " + pythonCorpusShellQuote(s)) }, depth+1)
				return
			}
			if !pythonCorpusExecutable(identity) {
				return
			}
			input := execProviderInput{identity: identity, args: args[index+1:], cwd: directory}
			for _, redirect := range stmt.Redirs {
				if redirect.Op == syntax.Hdoc || redirect.Op == syntax.DashHdoc {
					source, literal := liveDiffShellHeredoc(redirect, false)
					if !literal {
						uncertain["invocation/source uncertainty"]++
						return
					}
					input.stdin = source
				}
			}
			// Restrict source extraction to inline/stdin. execProgramSource can read
			// script files, but recorded scripts must not depend on today's filesystem.
			inline := len(input.args) == 0 && input.stdin != ""
			for _, arg := range input.args {
				if arg == "-c" || arg == "-" {
					inline = true
					break
				}
				if !strings.HasPrefix(arg, "-") {
					break
				}
			}
			if !inline {
				uncertain["invocation/source uncertainty"]++
				return
			}
			source, _, reason := execProgramSource(input)
			if reason != "" {
				uncertain["invocation/source uncertainty"]++
				return
			}
			var rendered bytes.Buffer
			if err := syntax.NewPrinter().Print(&rendered, stmt); err != nil {
				uncertain["invocation/source uncertainty"]++
				return
			}
			invocation := command
			invocation.command = wrap(rendered.String())
			invocation.directory = directory
			invocation.category = pythonCorpusIntent(source)
			result = append(result, invocation)
		}
		for _, stmt := range statements {
			visit(stmt)
		}
	}
	shell(command.command, command.directory, func(s string) string { return s }, 0)
	return result, uncertain
}

func pythonCorpusExecutable(name string) bool {
	if name == "python" || name == "python3" {
		return true
	}
	if !strings.HasPrefix(name, "python3.") {
		return false
	}
	for _, c := range strings.TrimPrefix(name, "python3.") {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(name) > len("python3.")
}

// Calls, not matching bytes, determine intent. Unknown callees may write files
// and stay in the measured population. Known output streams are not files.
func pythonCorpusIntent(source string) string {
	data := []byte(source)
	tree, err := parseSourceTree(data, execPythonLanguage, func() bool { return false })
	if err != nil || tree == nil {
		return "Python parse uncertainty"
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return "Python parse uncertainty"
	}
	write, unknown := false, false
	text := func(n *sitter.Node) string {
		if n == nil {
			return ""
		}
		return string(data[n.StartByte():n.EndByte()])
	}
	var visit func(*sitter.Node)
	visit = func(n *sitter.Node) {
		if n.Kind() == "comment" {
			return
		}
		if function, _ := sourceCall(n); function != nil {
			callee := text(function)
			attribute := text(function.ChildByFieldName("attribute"))
			switch {
			case callee == "sys.stdout.write" || callee == "sys.stderr.write" || callee == "sys.stdout.flush" || callee == "sys.stderr.flush":
			case attribute == "write_text" || attribute == "write_bytes" || attribute == "write" || attribute == "writelines" || attribute == "truncate" || attribute == "unlink" || attribute == "rename" || attribute == "replace" && strings.Contains(callee, "os."):
				write = true
			case callee == "print":
				_, args := sourceCall(n)
				for _, arg := range args {
					argument := strings.ReplaceAll(text(arg), " ", "")
					if strings.HasPrefix(argument, "file=") && argument != "file=sys.stdout" && argument != "file=sys.stderr" {
						write = true
					}
				}
			case callee == "open":
				// A read-only open cannot mutate a file, but computed modes remain unknown.
				_, args := sourceCall(n)
				if len(args) >= 2 {
					mode := strings.ReplaceAll(text(args[1]), " ", "")
					mode = strings.TrimPrefix(mode, "mode=")
					if len(mode) > 1 && (mode[0] == '\'' || mode[0] == '"') {
						if strings.ContainsAny(mode, "wax+") {
							write = true
						}
					} else {
						unknown = true
					}
				}
			case attribute == "replace":
				object := function.ChildByFieldName("object")
				constructor, _ := sourceCall(object)
				if constructor != nil && (text(constructor) == "Path" || text(constructor) == "pathlib.Path") {
					write = true
				} else if object == nil || object.Kind() != "string" {
					// A receiver may be a Path or another effectful object, not
					// necessarily a string. Ambiguity remains in the denominator.
					unknown = true
				}
			case callee == "len" || callee == "str" || callee == "repr" || callee == "int" || callee == "range" || callee == "enumerate" || callee == "Path" || callee == "pathlib.Path" || callee == "sorted" || callee == "list" || callee == "tuple" || callee == "set" || callee == "dict" || callee == "isinstance" || callee == "zip" || callee == "min" || callee == "max" || callee == "sum":
			case attribute == "read_text" || attribute == "read_bytes" || attribute == "read" || attribute == "readlines" || attribute == "split" || attribute == "splitlines" || attribute == "join" || attribute == "strip" || attribute == "startswith" || attribute == "endswith" || attribute == "find" || attribute == "index":
			default:
				unknown = true
			}
		}
		for i := range n.NamedChildCount() {
			visit(n.NamedChild(uint(i)))
		}
	}
	visit(tree.RootNode())
	if write {
		return "file write"
	}
	if unknown {
		return "unknown edit intent"
	}
	return "analysis"
}

func TestLiveDiffPythonCorpusFilter(t *testing.T) {
	tests := []struct {
		name, command string
		categories    []string
		uncertain     int
	}{
		{"literal inline", `python3 -c 'from pathlib import Path; Path("a.txt").write_text("x")'`, []string{"file write"}, 0},
		{"fixture heredoc", "cat <<'GO'\npython3 -c 'open(\"a\",\"w\").write(\"x\")'\nGO\n", nil, 0},
		{"fixture string", `printf '%s' 'python3 -c open("a","w").write("x")'`, nil, 0},
		{"analysis write string", `python -c 'print("p.write_text(x)")'`, []string{"analysis"}, 0},
		{"streams", `python -c 'import sys; sys.stdout.write("x"); sys.stderr.write("y")'`, []string{"analysis"}, 0},
		{"unknown", `python -c 'mystery(data)'`, []string{"unknown edit intent"}, 0},
		{"unsupported write", `python -c 'while True: p.write_text(compute())'`, []string{"file write"}, 0},
		{"malformed Python", `python -c 'p.write_text('`, []string{"Python parse uncertainty"}, 0},
		{"computed source", `python -c "$SCRIPT"`, nil, 1},
		{"malformed shell", `python -c 'unfinished`, nil, 1},
		{"env wrapper", `env -u BASH_ENV MODE=x python3 -c 'p.write_text("x")'`, []string{"file write"}, 0},
		{"bash wrapper", `bash -lc "python3 -c 'p.write_text(\"x\")'"`, []string{"file write"}, 0},
		{"multiple", `python -c 'p.write_text("x")'; python -c 'p.write_text("y")'`, []string{"file write", "file write"}, 0},
		{"literal heredoc", "python3 <<'PY'\np.write_text('x')\nPY\n", []string{"file write"}, 0},
		{"expanded heredoc", "python3 <<PY\np.write_text('$VALUE')\nPY\n", nil, 1},
		{"print file", `python -c 'print("x", file=handle)'`, []string{"file write"}, 0},
		{"fstring write", `python -c 'print(f"{p.write_text(x)}")'`, []string{"file write"}, 0},
		{"computed mode", `python -c 'open("a", mode).write("x")'`, []string{"file write"}, 0},
		{"readonly", `python -c 'print(open("a", "r").read())'`, []string{"analysis"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands, uncertain := pythonCorpusExtract(pythonCorpusCommand{directory: t.TempDir(), command: tt.command})
			if uncertain != tt.uncertain || len(commands) != len(tt.categories) {
				t.Fatalf("got %d invocations, %d uncertain; want %d, %d", len(commands), uncertain, len(tt.categories), tt.uncertain)
			}
			for i, command := range commands {
				if command.category != tt.categories[i] {
					t.Fatalf("category %q, want %q", command.category, tt.categories[i])
				}
			}
		})
	}
}

func TestLiveDiffPythonCorpusExtractionPreservesWrapperAndDirectory(t *testing.T) {
	command := pythonCorpusCommand{directory: "/tmp", command: `cd source && env A=b python -c 'p.write_text("x")'`}
	invocations, uncertain := pythonCorpusExtract(command)
	if uncertain != 0 || len(invocations) != 1 || invocations[0].directory != "/tmp/source" || !strings.HasPrefix(invocations[0].command, "env A=b python") {
		t.Fatal("wrapper/directory not preserved")
	}
	// Corpus classification never requires evaluation of unsupported expressions.
	if got := pythonCorpusIntent(`p.write_text(transform(x))`); got != "file write" {
		t.Fatal(got)
	}
}

func TestLiveDiffPythonCorpusPopulationAndFreeze(t *testing.T) {
	root := t.TempDir()
	shellCommands := []string{
		`printf '%s' 'python3 -c open("a","w").write("x")'`,
		`python -c 'print(".write(x)")'`,
		`python -c 'p.write_text("x")'; python -c 'unknown()'`,
		`python -c "$SCRIPT"`,
	}
	var log strings.Builder
	for _, command := range shellCommands {
		entry := map[string]any{"type": "assistant", "cwd": "/tmp", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": command}}}}}
		data, err := json.Marshal(&entry)
		if err != nil {
			t.Fatal(err)
		}
		log.Write(data)
		log.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(root, "fixture.jsonl"), []byte(log.String()), 0600); err != nil {
		t.Fatal(err)
	}
	commands, populations, err := pythonCorpusCommands([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	population := populations["claude"]
	if len(commands) != 2 || population.Raw != 4 || population.Invocations != 3 || population.Eligible != 2 || population.NonInvocation != 1 || population.Analysis != 1 || population.Uncertain != 1 || population.UnknownIntent != 1 {
		t.Fatalf("population: %+v", population)
	}
	path := filepath.Join(root, "frozen.gob")
	if err := pythonCorpusSave(path, commands, populations); err != nil {
		t.Fatal(err)
	}
	restored, counts, err := pythonCorpusLoad(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commands, restored) || !reflect.DeepEqual(populations, counts) {
		t.Fatal("freeze did not retain exact population")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v", info.Mode())
	}
	if err := pythonCorpusSave(path, commands, populations); err == nil {
		t.Fatal("freeze unexpectedly overwrote existing private population")
	}
}
