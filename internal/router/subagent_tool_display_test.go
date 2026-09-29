package router

import (
	"strings"
	"testing"
)

// Most display tests compare one textual preview; delivery tests check message boundaries.
func TestSubagentInterpreterWrapperProjection(t *testing.T) {
	for _, test := range []struct {
		name, source, command, language, program string
	}{
		{"python command", "python3 -I -c 'import sys\nprint(\"ok\")' arg", "python3 -I -c … arg", "python", "import sys\nprint(\"ok\")"},
		{"pypy command", "pypy3 -c 'import sys\nprint(\"ok\")'", "pypy3 -c …", "python", "import sys\nprint(\"ok\")"},
		{"python heredoc", "python3 - <<'PY'\nimport sys\nprint('ok')\nPY\n", "python3 -", "python", "import sys\nprint('ok')\n"},
		{"python heredoc without dash", "python3 <<'PY'\nimport sys\nprint('ok')\nPY\n", "python3", "python", "import sys\nprint('ok')\n"},
		{"node command", "node --input-type=module -e 'const a = 1\nconsole.log(a)'", "node --input-type=module -e …", "javascript", "const a = 1\nconsole.log(a)"},
		{"node heredoc", "node - <<'JS'\nconst a = 1\nconsole.log(a)\nJS\n", "node -", "javascript", "const a = 1\nconsole.log(a)\n"},
		{"bun command", "bun -e 'const a = 1\nconsole.log(a)'", "bun -e …", "javascript", "const a = 1\nconsole.log(a)"},
		{"perl heredoc", "perl - <<'PL'\nmy $a = 1;\nprint $a;\nPL\n", "perl -", "perl", "my $a = 1;\nprint $a;\n"},
		{"ruby command", "ruby -e 'a = 1\nputs a'", "ruby -e …", "ruby", "a = 1\nputs a"},
		{"php command", "php -r '$a = 1;\necho $a;'", "php -r …", "php", "$a = 1;\necho $a;"},
		{"shell combined flag", "sh -ec 'cd dir\nprintf ok'", "sh -ec …", "sh", "cd dir\nprintf ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := "Run " + toolActivityCode(test.command) + "\n" + toolActivityFenced(test.language, test.program)
			if got := toolActivityShell(test.source); got != want {
				t.Fatalf("display = %q; want %q", got, want)
			}
		})
	}

	// A one-line program reads best as the literal command, which names its
	// interpreter; a lone `-` notes that stdin supplies the program.
	for source, label := range map[string]string{
		`python3 -I -c 'print("ok")'`:         "Run",
		"python3 - <<'PY'\nprint('ok')\nPY\n": "Run",
		`perl -e 'print "ok"'`:                "Run",
		"python3 -":                           "Run · program read from stdin",
		"python3 -u -":                        "Run · program read from stdin",
		"python3 - < script.py":               "Run",
		"cat script.py | python3 -":           "Run",
		"python3 - arg":                       "Run",
	} {
		if got, want := toolActivityShell(source), label+"\n"+toolActivityFenced("bash", source); got != want {
			t.Errorf("literal display = %q; want %q", got, want)
		}
	}

	for _, source := range []string{
		`python3 -c "$program"`,
		`python3 -c 'print(1)' "$(touch hidden-effect)"`,
		"python3 script.py <<'PY'\ndata\nPY\n",
		"printf before\npython3 -c 'print(1)'",
		"python3 -c 'print(1)' <<'DATA'\ninput\nDATA\n",
		`perl -e 'print "first\n"' -e 'print "second\n"'`,
		`perl -e 'print "first\n"' '-eprint "second\n"'`,
	} {
		if got, want := toolActivityShell(source), "Run\n"+toolActivityFenced("bash", source); got != want {
			t.Errorf("dynamic or composed wrapper display = %q; want %q", got, want)
		}
	}
}

func TestToolActivityMultilineFence(t *testing.T) {
	got := toolActivityCode("echo '```'\necho done")
	if !strings.HasPrefix(got, "````\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("unsafe fence: %s", got)
	}
}

func TestSubagentSedReadDisplay(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"sed -n '1,260p' source.go", "Read `source.go 1:260`"},
		{"sed -i -n '1,260p' source.go", "Edit `source.go` · sed (requested)"},
		{"sed -n '1,260p' source.go && echo done", "Read `source.go 1:260`\n\nRun `echo done`"},
		{"sed -n '261,520p' 'source file.go'", "Read `source file.go 261:520`"},
	} {
		if got := toolActivityShell(tc.source); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{
		"sed -n '1,260p;d' source.go",
		"sed -n '1,$p' source.go",
		"sed -n '0,260p' source.go",
		"sed -n '260,1p' source.go",
		"sed -n '+1,260p' source.go",
		"sed -n '1,260p' -",
		"sed -n '1,260p' --version",
		"sed -n '1,260p' ''",
		"sed -n '1,260p' a.go b.go",
		"sed -n '1,260p' source.go > copy.go",
	} {
		want := "Run\n" + toolActivityFenced("bash", source)
		if got := toolActivityShell(source); got != want {
			t.Errorf("%s: got %q, want raw source %q", source, got, want)
		}
	}
}

func TestSubagentCatHeadReadDisplay(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"cat foo | head", "Read `foo`"},
		{"cat 'a b.txt' | head -n 20", "Read `a b.txt`"},
		{"cat foo bar | head -5", "Read `foo`\n\nRead `bar`"},
	} {
		if got := toolActivityShell(tc.source); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{"cat foo | head bar", "cat foo | head -n \"$count\"", "cat foo | head > out"} {
		if got := toolActivityShell(source); !strings.HasPrefix(got, "Run\n") {
			t.Errorf("unsafe pipeline %q: %q", source, got)
		}
	}
}

func TestSubagentCatSedReadDisplay(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"cat source.go | sed -n '45,150p'", "Read `source.go 45:150`"},
		{"cat 'source file.go' | sed -n '1,2p;4,8p'", "Read `source file.go 1:2 4:8`"},
		{"cat source.go | sed \\\n -n '45,150p'", "Read `source.go 45:150`"},
		{`cat "$file" | sed -n '1,2p'`, "Read `\"$file\" 1:2`"},
	} {
		if got := toolActivityShell(tc.source); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{
		"cat a.go b.go | sed -n '1,2p'",
		"cat -s source.go | sed -n '1,2p'",
		"cat source.go | sed -n '1,$p'",
		"cat source.go | sed -n '1,2p;w out'",
		"cat source.go | sed -n '1,2p' > out",
		"cat source.go | sed -n '1,2p' other.go",
		"cat $(generate) | sed -n '1,2p'",
		"cat source.go | sed -n \"$range\"",
		"cat source.go |& sed -n '1,2p'",
		"cat source.go | sed -n '1,2p' | head -1",
	} {
		if got := toolActivityShell(source); got != "Run\n"+toolActivityFenced("bash", source) {
			t.Errorf("unsupported pipeline %q changed meaning: %q", source, got)
		}
	}
}

func TestSubagentNumberedReadDisplay(t *testing.T) {
	source := "nl -ba semantic-assessment.ts | sed -n '58,154p'; printf '\\n--- judge validations ---\\n'; " +
		"nl -ba judge.ts | sed -n '79,162p'; printf '\\n--- run grading / assessment lifecycle ---\\n'; " +
		"nl -ba runner.ts | sed -n '231,250p;314,452p';"
	want := "Read `semantic-assessment.ts 58:154`\n\n" +
		"Read `judge.ts 79:162`\n\n" +
		"Read `runner.ts 231:250 314:452`"
	if got := toolActivityShell(source); got != want {
		t.Fatalf("numbered read display: got %q, want %q", got, want)
	}
	for _, invalid := range []string{
		"nl -b a source.go | sed -n '1,2p'",
		"nl -ba source.go | sed -n '1,$p'",
		"nl -ba source.go | sed -n '1,2p' > copy.go",
		"nl -ba source.go | sed -n '1,2p' | head -n 1",
	} {
		want := "Run\n" + toolActivityFenced("bash", invalid)
		if got := toolActivityShell(invalid); got != want {
			t.Errorf("invalid numbered read %q: got %q, want %q", invalid, got, want)
		}
	}
	for _, visible := range []string{
		"printf '\\n--- heading ---\\n'",
		"cat source.go; printf \"\\n--- $heading ---\\n\"",
		"cat source.go; printf '\\nnot a heading\\n'",
		"cat source.go; printf '\\n--- heading ---\\n'; make test",
	} {
		got := toolActivityShell(visible)
		if !strings.Contains(got, "Run") {
			t.Errorf("visible printf %q was hidden: %q", visible, got)
		}
	}
}

func TestSubagentMixedReadRunFallbacks(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"cat a\ncat b > c", "Read `a`\n\nEdit `c` · cat (requested)"},
		{"cat a; git diff --check; git diff --stat;", "Read `a`\n\nCheck `working tree` · git diff --check\n\nDiff `working tree` · git --stat"},
		{"cat a; git log --oneline;", "Read `a`\n\nRun `git log --oneline`"},
		{"cat a\ncat b && echo done", "Read `a`\n\nRead `b`\n\nRun `echo done`"},
		{"cat a; printf '%s;' value;", "Read `a`\n\nRun `printf '%s;' value`"},
		{"cat a; sleep 1 &", "Read `a`\n\nRun `sleep 1 &`"},
		{"git status --short\ncat a", "Status `working tree` · git\n\nRead `a`"},
		{"git status --porcelain=v2\ncat a", "Run `git status --porcelain=v2`\n\nRead `a`"},
		{"cat a\nsed -n '1,$p' b", "Read `a`\n\nRun `sed -n '1,$p' b`"},
		{"cat a\ncat \"$file\"", "Read `a`\n\nRead `\"$file\"`"},
		{"cat a\ncat good -n", "Read `a`\n\nRun `cat good -n`"},
		{"cat a\necho 'first\n\nlast'", "Read `a`\n\nRun\n```bash\necho 'first\n\nlast'\n```"},
		{"cat a\n  printf '%s\\n' \\\n    value", "Read `a`\n\nRun\n```bash\n  printf '%s\\n' \\\n    value\n```"},
		{"cat a;  printf '%s\\n' \\\n    value", "Read `a`\n\nRun\n```bash\nprintf '%s\\n' \\\n    value\n```"},
		{"cat a\necho first\necho second", "Read `a`\n\nRun `echo first`\n\nRun `echo second`"},
	} {
		if got := toolActivityShell(tc.source); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{
		"cat a\nfor f in *.go; do cat \"$f\"; done",
		"cat a\ncat b &",
	} {
		if got, want := toolActivityShell(source), "Read `a`\n\nRun "+toolActivityCode(strings.TrimPrefix(source, "cat a\n")); got != want {
			t.Errorf("%s: got %q, want %q", source, got, want)
		}
	}

}

func TestClassifiedToolActivityShowsEveryOperation(t *testing.T) {
	path := strings.Repeat("a", 4200)
	input := "cat " + path + "\nrg needle src\ncat last"
	want := "Read `" + path + "`\n\nSearch `needle` in `src`\n\nRead `last`"
	if got := toolActivityShell(input); got != want {
		t.Fatalf("display: got %q, want %q", got, want)
	}
}

func TestShellBatchActivityDisplay(t *testing.T) {
	const first = "sed -n '1,360p' internal/router/session_inspect.go"
	const search = "rg -n '^func Test' internal/router/session_inspect_test.go cmd/mekugi/main_test.go 2>/dev/null"
	const last = "git status --short --branch\ngit log -1 --oneline"
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		separator := ending + "#!bash" + ending
		source := first + separator + search + separator + last
		want := strings.Join([]string{
			toolActivityShell(first + ending), toolActivityShell("#!bash" + ending + search + ending), toolActivityShell("#!bash" + ending + last),
		}, "\n\n")
		if got := toolActivityShell(source); got != want {
			t.Fatalf("batch display = %q, want %q", got, want)
		}
	}

	firstSource := "#!params={\"workdir\":\"/tmp\"}\ncat first"
	secondSource := "#!python3\nprint('SESSION')"
	thirdSource := "cat third"
	source := firstSource + "\n" + secondSource + "\n#!bash\n" + thirdSource
	want := "Read `first`\n\n" +
		toolActivityShell("#!python3\n#!params={\"workdir\":\"/tmp\"}\nprint('SESSION')\n") +
		"\n\nRead `third`"
	if got := toolActivityShell(source); got != want {
		t.Fatalf("mixed interpreters and inherited params = %q, want %q", got, want)
	}
	for _, invalid := range []string{"#!bash\n#!bash\ncat first", "cat first\n#!bash\n"} {
		if got := toolActivityShell(invalid); got != "Run\n"+toolActivityFenced("", invalid) {
			t.Fatalf("invalid batch lost source: %q", got)
		}
	}
}

func TestSubagentMixedHeredocPreview(t *testing.T) {
	const body = "cat <<EOF; printf done\nhello\nEOF\n"
	source := "journal add 'Working'\n" + body + "journal add 'Finished'\n"
	want := "Run\n" + toolActivityFenced("bash", strings.TrimRight(body, "\n"))
	if got := toolActivityShell(source); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	source = "cat a\n" + body
	if got, want := toolActivityShell(source), "Run\n"+toolActivityFenced("bash", source); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSubagentSymbolicReadPaths(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{`mcat "$HOME/.codex/INSTRUCTION-AUTHORING.md" AGENTS.md`, "Read `\"$HOME/.codex/INSTRUCTION-AUTHORING.md\"`\n\nRead `AGENTS.md`"},
		{`mcat "${HOME}/a b.go" 1:20 other.go`, "Read `\"${HOME}/a b.go\" 1:20`\n\nRead `other.go`"},
		{`cat $ROOT/*.go`, "Read `$ROOT/*.go`"},
		{`sed -n '390,432p' $(go env GOROOT)/src/encoding/json/v2/arshal_time.go`, "Read `$(go env GOROOT)/src/encoding/json/v2/arshal_time.go 390:432`"},
		{`sed -n '1,260p' "$file"`, "Read `\"$file\" 1:260`"},
		{`nl -ba "$file" | sed -n '1,2p'`, "Read `\"$file\" 1:2`"},
		{`inspect_file "$ROOT/a.go"`, "Inspect `\"$ROOT/a.go\"`"},
		{`rg needle "$ROOT"`, "Search `needle` in `\"$ROOT\"`"},
	} {
		if got := toolActivityShell(tc.source); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{
		`mcat "$(touch sentinel)/a" AGENTS.md`,
		`mcat "$(touch sentinel)$HOME/a"`,
		`mcat "${HOME:=/tmp}$HOME/a"`,
		`mcat "${paths[index++]}$HOME/a"`,
		"mcat \"`touch sentinel`/a\" AGENTS.md",
		`mcat "${HOME:-$(touch sentinel)}/a"`,
		`mcat "${HOME:=/tmp}/a"`,
		`mcat "${paths[index++]}/a"`,
		`mcat <(cat a)`,
		`"$READER" a.go`,
	} {
		if got := toolActivityShell(source); got != "Run\n"+toolActivityFenced("bash", source) {
			t.Errorf("%s: unexpected classification %q", source, got)
		}
	}
}

func TestSubagentSedDynamicOperands(t *testing.T) {
	for _, operand := range []string{
		`$(project-root)/file.go`, `"$(dirname "$file")/file.go"`,
		"`project-root`/file.go", `${ROOT:-/tmp}/file.go`,
		`$(touch sentinel; printf /tmp)/file.go`, `<(generate-source)`,
		`$(go env -w GOPATH=/tmp)/file.go`,
		`$(GOENV=other go env GOROOT)/file.go`,
	} {
		source := "sed -n '1,2p' " + operand
		want := "Read " + toolActivityCode(operand+" 1:2")
		if got := toolActivityShell(source); got != want {
			t.Errorf("%s: got %q, want %q", source, got, want)
		}
	}
	for _, source := range []string{
		`sed -i -n '1,2p' $(project-root)/file.go`,
		`sed -n '1,2p;w out' $(project-root)/file.go`,
		`sed -n '1e touch sentinel' $(project-root)/file.go`,
		`sed -n "$program" $(project-root)/file.go`,
		`sed "$flags" '1,2p' $(project-root)/file.go`,
	} {
		if got := toolActivityShell(source); strings.HasPrefix(got, "Read") {
			t.Errorf("%s: misclassified as %q", source, got)
		}
	}
}
