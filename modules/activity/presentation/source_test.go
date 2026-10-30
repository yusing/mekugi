package presentation

import (
	"strings"
	"testing"

	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	"github.com/yusing/mekugi/modules/activity/feed"
)

func TestShellPreviewSkipsErrorOptions(t *testing.T) {
	for _, source := range []string{
		"set -e\nplane comment ITEM --body - <<'EOF'\nbody\nEOF\nplane update ITEM",
		"# setup\nset -eu; set -o pipefail; plane update ITEM",
		"set -e && plane update ITEM | cat",
	} {
		block := Value{Kind: "op", Verb: "Run", Lang: "bash", Code: source}
		if preview := block.PreviewCode(); !strings.HasPrefix(preview, "plane ") || block.Code != source {
			t.Fatalf("preview=%q, retained=%q", preview, block.Code)
		}
		if first, omitted := previewSource(source); !strings.HasPrefix(first, "plane ") || !omitted {
			t.Fatalf("compact preview=%q, omitted=%v", first, omitted)
		}
	}
	for _, source := range []string{"set -e", "set -x\nplane update ITEM", "set -e | cat", "! set -e\nplane update ITEM", "set -e\nbroken '"} {
		if got := shellPreviewSource(source); got != source {
			t.Fatalf("changed opaque source: %q -> %q", source, got)
		}
	}
}

// Most display tests compare one textual preview; delivery tests check message boundaries.
func TestSubagentInterpreterWrapperProjection(t *testing.T) {
	for _, test := range []struct {
		name, source, via, language, program string
	}{
		{"python command", "python3 -I -c 'import sys\nprint(\"ok\")' arg", "python3 -I -c … arg", "python", "import sys\nprint(\"ok\")"},
		{"pypy command", "pypy3 -c 'import sys\nprint(\"ok\")'", "pypy3", "python", "import sys\nprint(\"ok\")"},
		{"python heredoc", "python3 - <<'PY'\nimport sys\nprint('ok')\nPY\n", "python3", "python", "import sys\nprint('ok')\n"},
		{"python heredoc without dash", "python3 <<'PY'\nimport sys\nprint('ok')\nPY\n", "python3", "python", "import sys\nprint('ok')\n"},
		{"python heredoc arguments", "python3 -u - \"$dir\" out <<'PY'\nprint('ok')\nPY\n", `python3 -u - "$dir" out`, "python", "print('ok')\n"},
		{"assignment prefix", "PROBE=\"$dir\" python3 - <<'PY'\nprint('ok')\nPY\n", `PROBE="$dir" python3`, "python", "print('ok')\n"},
		{"node command", "node --input-type=module -e 'const a = 1\nconsole.log(a)'", "node --input-type=module", "javascript", "const a = 1\nconsole.log(a)"},
		{"node heredoc", "node - <<'JS'\nconst a = 1\nconsole.log(a)\nJS\n", "node", "javascript", "const a = 1\nconsole.log(a)\n"},
		{"bun command", "bun -e 'const a = 1\nconsole.log(a)'", "bun", "javascript", "const a = 1\nconsole.log(a)"},
		{"perl heredoc", "perl - <<'PL'\nmy $a = 1;\nprint $a;\nPL\n", "perl", "perl", "my $a = 1;\nprint $a;\n"},
		{"ruby command", "ruby -e 'a = 1\nputs a'", "ruby", "ruby", "a = 1\nputs a"},
		{"php command", "php -r '$a = 1;\necho $a;'", "php", "php", "$a = 1;\necho $a;"},
		{"shell combined flag", "sh -ec 'cd dir\nprintf ok'", "sh -e", "sh", "cd dir\nprintf ok"},
		{"one-line python", `python3 -I -c 'print("ok")'`, "python3 -I", "python", `print("ok")`},
		{"one-line sql", `psql -c 'select 1'`, "psql", "sql", "select 1"},
		{"stdin marker argument", "python3 - - <<'PY'\nprint('ok')\nPY\n", "python3 - -", "python", "print('ok')\n"},
		// Without stdin arguments, a `-` stays visible rather than being explained.
		{"shell stdin marker", "bash - <<'SH'\necho ok\nSH\n", "bash -", "bash", "echo ok\n"},
		{"php stdin marker", "php - <<'PHP'\n<?php echo 1;\nPHP\n", "php -", "php", "<?php echo 1;\n"},
		{"leading blank lines", "python3 - <<'PY'\n\n  \n  print('ok')\nPY\n", "python3", "python", "  print('ok')\n"},
		{"bun heredoc", "bun --no-install - <<'JS'\nconsole.log(1)\nJS\n", "bun --no-install", "javascript", "console.log(1)\n"},
		{"bun run heredoc", "bun run - <<'JS'\nconsole.log(1)\nJS\n", "bun run", "javascript", "console.log(1)\n"},
		{"bun module heredoc", "bun --input-type=module <<'JS'\nconsole.log(1)\nJS\n", "bun --input-type=module", "javascript", "console.log(1)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := "Run · via " + SourceCode(test.via) + "\n" + markdownsyntax.SourceFence(test.language, test.program)
			if got := shellSource(test.source); got != want {
				t.Fatalf("display = %q; want %q", got, want)
			}
		})
	}

	// A composed command keeps its other statements as their own operations.
	if got, want := shellSource("printf before\npython3 -c 'print(1)'"),
		"Run `printf before`\n\nRun · via `python3`\n"+markdownsyntax.SourceFence("python", "print(1)"); got != want {
		t.Errorf("composed display = %q; want %q", got, want)
	}
	if got, want := shellSource("rg -n x a.go; python3 - <<'PY'\nprint(1)\nPY\n"),
		"Search `x` in `a.go`\n\nRun · via `python3`\n"+markdownsyntax.SourceFence("python", "print(1)\n"); got != want {
		t.Errorf("composed heredoc display = %q; want %q", got, want)
	}
	if got, want := shellSource("python3 - <<'A'; node - <<'B'\nprint(1)\nA\nconsole.log(2)\nB\n"),
		"Run · via `python3`\n"+markdownsyntax.SourceFence("python", "print(1)\n")+
			"\n\nRun · via `node`\n"+markdownsyntax.SourceFence("javascript", "console.log(2)\n"); got != want {
		t.Errorf("same-line heredocs display = %q; want %q", got, want)
	}
	if got, want := shellSource("timeout 5 python3 - <<'PY'\nprint(1)\nPY\n"),
		"Run · via `python3` (timeout 5)\n"+markdownsyntax.SourceFence("python", "print(1)\n"); got != want {
		t.Errorf("timeout display = %q; want %q", got, want)
	}

	// A lone `-` notes that stdin supplies the program.
	for source, label := range map[string]string{
		"python3 -":                 "Run · program read from stdin",
		"python3 -u -":              "Run · program read from stdin",
		"python3 - < script.py":     "Run",
		"cat script.py | python3 -": "Run",
		"python3 - arg":             "Run",
		"python3 -c ''":             "Run",
	} {
		if got, want := shellSource(source), label+"\n"+markdownsyntax.SourceFence("bash", source); got != want {
			t.Errorf("literal display = %q; want %q", got, want)
		}
	}

	for _, source := range []string{
		`python3 -c "$program"`,
		`python3 -c 'print(1)' "$(touch hidden-effect)"`,
		"python3 - \"$(touch hidden-effect)\" <<'PY'\nprint(1)\nPY\n",
		"X=$(touch hidden-effect) python3 - <<'PY'\nprint(1)\nPY\n",
		"python3 script.py <<'PY'\ndata\nPY\n",
		"python3 -c 'print(1)' <<'DATA'\ninput\nDATA\n",
		// Shells and php may run a file named after `-`, not the heredoc.
		"bash - script.sh <<'SH'\necho ok\nSH\n",
		"php - script.php <<'PHP'\n<?php echo 1;\nPHP\n",
		`node -p 'process.version'`,
		"bun run package-script <<'JS'\ndata\nJS\n",
		// env settings and enclosing commands belong to what runs.
		"env A=1 python3 - <<'PY'\nprint(1)\nPY\n",
		"f=$(python3 - <<'PY'\nprint(1)\nPY\n)\necho \"$f\"",
		"if true; then python3 - <<'PY'\nprint(1)\nPY\nfi",
		`perl -e 'print "first\n"' -e 'print "second\n"'`,
		`perl -e 'print "first\n"' '-eprint "second\n"'`,
	} {
		if got, want := shellSource(source), "Run\n"+markdownsyntax.SourceFence("bash", source); got != want {
			t.Errorf("dynamic or composed wrapper display = %q; want %q", got, want)
		}
	}
}

// Detail pages keep the complete inner program in its own language.
func TestInterpreterDetailSource(t *testing.T) {
	program := "python3 -c 'print(1)'"
	command := Command{Script: program, Segments: []CommandSegment{{Source: program, Text: shellSource(program)}}}
	for detail, want := range map[bool]Value{
		false: {Content: feed.Content{Verb: "Run", Label: "· via `python3`", Code: "print(1)"}},
		true:  {Content: feed.Content{Verb: "Run", Code: "print(1)"}},
	} {
		values := command.SegmentValues(detail)
		if len(values) != 1 || values[0].Verb != want.Verb || values[0].Label != want.Label || values[0].Code != want.Code {
			t.Errorf("detail %v values = %+v; want %+v", detail, values, want)
		}
	}
}

func TestCapturedInterpreterSource(t *testing.T) {
	for _, source := range []string{
		"python3 - <<'PY'\nfrom pathlib import Path\nPath('file').write_text('new')\nPY\n",
		"bun run - <<'JS'\nimport { writeFileSync } from 'node:fs';\nwriteFileSync('file', 'new');\nJS\n",
	} {
		projection, ok := shellInterpreterScriptProjection(source)
		if !ok {
			t.Fatal("missing interpreter projection")
		}
		for _, tracked := range []bool{false, true} {
			for _, running := range []bool{false, true} {
				command := Command{Source: 7, Script: source, Display: source, Text: ShellSource(source, ""), Captured: true,
					CaptureText: "Edit `file` · +1 −1 · script", Running: running, Exited: !running, Duration: 1,
					Tail: []string{"ordinary output"}}
				if tracked {
					command.Segments = []CommandSegment{{Source: source, Text: command.Text, Writer: !running, Running: running, Duration: 1, Tail: command.Tail}}
				}
				item := ItemContent{Seq: 7, Kind: "tool", Text: command.Text, Command: command, Details: &ItemDetails{CommandText: source}}
				values := item.Values()
				if len(values) < 2 || values[0].Code != strings.TrimSuffix(projection.Source, "\n") || values[0].Lang != projection.Language || !strings.Contains(values[0].Label, projection.Via) || len(values[0].Tail) != 1 {
					t.Fatalf("tracked=%v running=%v values=%+v", tracked, running, values)
				}
				ids := make(map[string]bool)
				for _, page := range DetailPages(values) {
					if ids[page.ID] {
						t.Fatalf("duplicate source/edit identity: %s", page.ID)
					}
					ids[page.ID] = true
				}
				pages := RefreshDetails(DetailPages(values[:1]), map[uint64]DetailSource{7: {Command: &command}})
				if len(pages) != 1 || pages[0].Code != values[0].Code || pages[0].Lang != projection.Language {
					t.Fatalf("detail lost program: %+v", pages)
				}
			}
		}
	}
}

func TestInternalReloadSource(t *testing.T) {
	for _, source := range []string{
		`mreload '{"sources":[{"path":"context.md"}]}'`,
		"mreload \\\n  '{\"sources\":[{\"skill\":\"commit\"}]}'",
		`mrun --max-tokens 1000 mreload '{"sources":[]}'`,
		`mreload "$selection"`,
		`SELECTION=x mreload '{"sources":[]}'`,
		`mreload '{"sources":[]}' 2>/dev/null`,
	} {
		if text := ShellSource(source, ""); text != "" {
			t.Fatalf("internal source visible: %q", text)
		}
		for _, running := range []bool{false, true} {
			command := Command{Source: 7, Script: source, Display: source, Running: running, Exited: !running, Duration: 1, Tail: []string{"Reload snapshot: mread pearl"},
				Segments: []CommandSegment{{Source: source, Running: running, Tail: []string{"Reload snapshot: mread pearl"}}}}
			item := ItemContent{Seq: 7, Kind: "tool", Command: command, Details: &ItemDetails{CommandText: source, Output: 1}}
			if values := item.Values(); len(values) != 0 {
				t.Fatalf("internal output/source visible: %+v", values)
			}
			var model Model[modelSource, modelAgent]
			model.Append(modelSource{Seq: 6, Agent: "/root/worker", Kind: "tool", Text: "Run `ordinary`"}, []Value{{Kind: "op", Verb: "Run", Code: "ordinary"}})
			item.Agent = "/root/worker"
			model.Append(modelSource{Facts: item.Facts}, item.Values())
			if !model.Entries[1].Publish().Hidden() || model.Summary("/root/worker", false).Index != 0 {
				t.Fatal("internal transport replaced transcript/roster activity")
			}
		}
	}
	// Ordinary output mentioning the transport is still ordinary output.
	const ordinary = `printf 'Reload snapshot: mread pearl'`
	item := ItemContent{Kind: "tool", Text: ShellSource(ordinary, ""), Command: Command{Script: ordinary, Display: ordinary, Tail: []string{"Reload snapshot: mread pearl"}}, Details: &ItemDetails{CommandText: ordinary}}
	if values := item.Values(); len(values) != 1 || len(values[0].Tail) != 1 {
		t.Fatalf("ordinary output suppressed: %+v", values)
	}
	for _, source := range []string{`mreload "$(printf selection)"`, `SELECTION=$(printf selection) mreload '{"sources":[]}'`} {
		if hiddenCommandSource(source) {
			t.Fatalf("nested command effects hidden: %s", source)
		}
	}
}

func TestMixedInterpreterAndInternalSource(t *testing.T) {
	const program = "import { writeFileSync } from 'node:fs';\nwriteFileSync('file', 'new');"
	const interpreter = "bun run - <<'JS'\n" + program + "\nJS\n"
	for _, source := range []string{interpreter + "printf done\n", "printf before\n" + interpreter, "printf before && " + interpreter} {
		command := Command{Source: 7, Script: source, Display: source, Text: ShellSource(source, ""), Captured: true, CaptureText: "Edit `file` · +1 −1 · bun", Exited: true, Duration: 1, Tail: []string{"ordinary output"}}
		values := (ItemContent{Seq: 7, Kind: "tool", Text: command.Text, Command: command, Details: &ItemDetails{CommandText: source}}).Values()
		found := false
		for _, value := range values {
			found = found || value.Code == program && value.Lang == "javascript" && strings.Contains(value.Label, "bun run")
			if len(value.Tail) > 0 && !value.BatchExit {
				t.Fatalf("aggregate output attributed to source: %+v", value)
			}
		}
		if !found {
			t.Fatalf("mixed program omitted: %+v", values)
		}
		pages := command.DetailValues()
		if len(pages) != 3 || !pages[2].BatchExit || len(pages[2].Tail) != 1 {
			t.Fatalf("mixed detail boundaries changed: %+v", pages)
		}
		found = false
		for _, page := range pages[:2] {
			found = found || page.Code == program && page.Lang == "javascript"
		}
		if !found {
			t.Fatalf("mixed detail program omitted: %+v", pages)
		}
	}
	const mixed = `printf ordinary; mreload '{"sources":[{"path":"private-context.md"}]}'`
	command := Command{Source: 7, Script: mixed, Display: mixed, Text: ShellSource(mixed, ""), Exited: true, Duration: 1, Tail: []string{"ordinary", "Reload snapshot: mread pearl"}}
	values := (ItemContent{Seq: 7, Kind: "tool", Text: command.Text, Command: command, Details: &ItemDetails{CommandText: mixed}}).Values()
	for _, value := range append(values, command.DetailValues()...) {
		if strings.Contains(value.Code+value.Label, "private-context.md") || strings.Contains(value.Code+value.Label, "mreload") {
			t.Fatalf("mixed transport source leaked: %+v", value)
		}
	}
	if len(values[len(values)-1].Tail) != 2 {
		t.Fatal("inseparable ordinary output was discarded")
	}
	// Actual segment boundaries hide only the transport's own retained stream.
	command.Segments = []CommandSegment{{Source: "printf ordinary", Text: ShellSource("printf ordinary", ""), Tail: []string{"ordinary"}}, {Source: `mreload '{"sources":[]}'`, Tail: []string{"Reload snapshot: mread pearl"}}}
	command.Tail = nil
	values = (ItemContent{Seq: 7, Kind: "tool", Text: command.Text, Command: command, Details: &ItemDetails{CommandText: mixed}}).Values()
	if len(values) != 1 || len(values[0].Tail) != 1 || values[0].Tail[0] != "ordinary" {
		t.Fatalf("segmented ordinary output changed: %+v", values)
	}
}

func TestInternalReloadSetupAndApprovalFallbacks(t *testing.T) {
	const internal = `mreload '{"sources":[{"path":"private-context.md"}]}'`
	const source = "set -e; printf ordinary; " + internal
	text := ShellSource(source, "")
	command := Command{Source: 7, Script: source, Display: source, Text: text, Exited: true, Duration: 1,
		Tail: []string{"ordinary", "Reload snapshot: mread pearl"}}
	values := (ItemContent{Seq: 7, Kind: "tool", Text: text, Command: command, Details: &ItemDetails{CommandText: source}}).Values()
	if strings.Contains(text, "mreload") || strings.Contains(text, "private-context.md") || !strings.Contains(text, "printf ordinary") {
		t.Fatalf("setup fallback changed visible source: %q", text)
	}
	for _, value := range append(values, command.DetailValues()...) {
		if strings.Contains(value.Code+value.Label, "mreload") || strings.Contains(value.Code+value.Label, "private-context.md") {
			t.Fatalf("setup fallback exposed transport source: %+v", value)
		}
	}
	if len(values) == 0 || len(values[len(values)-1].Tail) != 2 {
		t.Fatal("setup fallback discarded inseparable ordinary output")
	}
	for _, approval := range []string{"Pending Approval", "Approved", "Denied"} {
		t.Run(approval, func(t *testing.T) {
			source := "sudo true; " + internal
			command := Command{Source: 7, Script: source, Display: source, Text: ShellSource(source, ""), Approval: approval,
				Segments: []CommandSegment{{Source: "sudo true", Text: ShellSource("sudo true", ""), Skipped: true}, {Source: internal, Skipped: true}}}
			values := (ItemContent{Seq: 7, Kind: "tool", Text: command.Text, Command: command, Details: &ItemDetails{CommandText: source}}).Values()
			pages := command.DetailValues()
			for _, value := range append(values, pages...) {
				if strings.Contains(value.Code+value.Label, "mreload") || strings.Contains(value.Code+value.Label, "private-context.md") {
					t.Fatalf("approval fallback exposed transport source: %+v", value)
				}
			}
			if len(pages) == 0 || !pages[len(pages)-1].BatchExit || pages[len(pages)-1].Approval != approval || !strings.Contains(pages[len(pages)-1].Code, "sudo true") {
				t.Fatalf("approval fallback lost ordinary source or approval outcome: %+v", pages)
			}
		})
	}
}

func TestPendingInterpreterKeepsTimeout(t *testing.T) {
	source := "timeout 5 python3 - <<'PY'\nprint(1)\nPY\n"
	command := Command{Script: source, Display: source, Segments: []CommandSegment{{Source: source, Pending: true}}}
	values := command.SegmentValues(false)
	if len(values) != 1 || values[0].Code != "print(1)" || len(values[0].Timeouts) != 1 || values[0].Timeouts[0] != "5" {
		t.Fatalf("pending bound lost: %+v", values)
	}
}

func TestInterpreterProjectionKeepsWholeInvocation(t *testing.T) {
	const nested = `bash -lc 'python3 -c "print(1)"; printf done'`
	command := Command{Source: 7, Script: nested, Display: nested, Text: ShellSource(nested, ""), Segments: []CommandSegment{{Source: nested, Pending: true}}}
	if values := command.SegmentValues(false); len(values) != 1 || !strings.Contains(values[0].Code, "printf done") || values[0].Lang != "bash" {
		t.Fatalf("nested sibling omitted: %+v", values)
	}
	if pages := command.DetailValues(); len(pages) != 1 || !strings.Contains(pages[0].Code, "printf done") {
		t.Fatalf("nested detail sibling omitted: %+v", pages)
	}
	for index, source := range []string{
		`bash -lc 'python3 -c "print(1)"; rm missing-file'`,
		"python3 - <<'PY'\nfrom pathlib import Path\nPath('file').write_text('new')\nPY\nmreload '{\"sources\":[{\"path\":\"private-context.md\"}]}'\n",
		`bash -lc 'printf ordinary; mreload "{\"sources\":[{\"path\":\"private-context.md\"}]}"'`,
	} {
		command := Command{Source: 7, Script: source, Display: source, Text: ShellSource(source, ""), Captured: true, CaptureText: "Edit `file` · +1 −1 · script", Exited: true, ExitCode: 1, Duration: 1, Tail: []string{"aggregate failure"}}
		if index == 0 {
			command.Captured, command.CaptureText = false, ""
		}
		values := (ItemContent{Seq: 7, Kind: "tool", Text: command.Text, Command: command, Details: &ItemDetails{CommandText: source}}).Values()
		found := false
		for _, value := range values {
			if value.Exited && !value.BatchExit {
				t.Fatalf("aggregate exit attributed to source: %+v", value)
			}
			found = found || value.BatchExit && value.Exited && value.ExitCode == 1
		}
		if !found {
			t.Fatalf("missing aggregate failure: %+v", values)
		}
		ids := make(map[string]bool)
		for _, page := range DetailPages(values) {
			if ids[page.ID] {
				t.Fatalf("duplicate operation identity %s", page.ID)
			}
			ids[page.ID] = true
		}
		for _, page := range command.DetailValues() {
			for _, value := range append([]Value{page}, page.RequestedOperations...) {
				if strings.Contains(value.Code+value.Label, "private-context.md") || strings.Contains(value.Code+value.Label, "mreload") {
					t.Fatalf("detail transport source leaked: %+v", value)
				}
			}
		}
	}
}

// A created script with an unresolved target names its invocation once.
func TestCreatedScriptUnresolvedRun(t *testing.T) {
	script := "from pathlib import Path\nPath('known.go').write_text('x')\nPath(input()).write_text('y')\n"
	for call, suffix := range map[string]string{"timeout 5 python3 s.py": " (timeout 5)", "python3 \\\n  s.py": ""} {
		got := shellSource("cat > s.py <<'EOF'\n" + script + "EOF\n" + call)
		if want := "\n\nRun · via `python3 s.py`" + suffix + "\n" + markdownsyntax.SourceFence("python", script); !strings.HasSuffix(got, want) {
			t.Errorf("%q display = %q; want suffix %q", call, got, want)
		}
	}
}

func TestToolActivityMultilineFence(t *testing.T) {
	got := SourceCode("echo '```'\necho done")
	if !strings.HasPrefix(got, "````\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("unsafe fence: %s", got)
	}
}

func TestToolActivityShellSetupStaysTogether(t *testing.T) {
	for _, setup := range []string{
		"export ROOT=/tmp", "ROOT=$(mktemp -d)", "trap 'echo cleanup' EXIT",
		"set -x", "source setup.sh", ". setup.sh", "unset ROOT", "builtin export ROOT=/tmp",
		"f() { cat a; }",
		"f() { timeout 10 sleep 1; }",
		"set -e\ntimeout \"$duration\" sleep 1",
		"export ROOT=/tmp\ncat <<'EOF'\nbody\nEOF\ngit log -1",
	} {
		source := "cat before\n" + setup + "\nsed -n '1,20p' after"
		if got, want := shellSource(source), "Run\n"+markdownsyntax.SourceFence("bash", source); got != want {
			t.Errorf("shell setup split: got %q, want %q", got, want)
		}
	}
}

func TestSubagentSedReadDisplay(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"sed -n '1,260p' source.go", "Read `source.go 1:260`"},
		{"sed -i -n '1,260p' source.go", "Edit `source.go` · sed (requested)"},
		{"sed -n '1,260p' source.go && echo done", "Read `source.go 1:260`\n\nRun `echo done`"},
		{"sed -n '261,520p' 'source file.go'", "Read `source file.go 261:520`"},
	} {
		if got := shellSource(tc.source); got != tc.want {
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
		want := "Run\n" + markdownsyntax.SourceFence("bash", source)
		if got := shellSource(source); got != want {
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
		if got := shellSource(tc.source); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{"cat foo | head bar", "cat foo | head -n \"$count\"", "cat foo | head > out"} {
		if got := shellSource(source); !strings.HasPrefix(got, "Run\n") {
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
		if got := shellSource(tc.source); got != tc.want {
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
		if got := shellSource(source); got != "Run\n"+markdownsyntax.SourceFence("bash", source) {
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
	if got := shellSource(source); got != want {
		t.Fatalf("numbered read display: got %q, want %q", got, want)
	}
	for _, invalid := range []string{
		"nl -b a source.go | sed -n '1,2p'",
		"nl -ba source.go | sed -n '1,$p'",
		"nl -ba source.go | sed -n '1,2p' > copy.go",
		"nl -ba source.go | sed -n '1,2p' | head -n 1",
	} {
		want := "Run\n" + markdownsyntax.SourceFence("bash", invalid)
		if got := shellSource(invalid); got != want {
			t.Errorf("invalid numbered read %q: got %q, want %q", invalid, got, want)
		}
	}
	for _, visible := range []string{
		"printf '\\n--- heading ---\\n'",
		"cat source.go; printf \"\\n--- $heading ---\\n\"",
		"cat source.go; printf '\\nnot a heading\\n'",
		"cat source.go; printf '\\n--- heading ---\\n'; make test",
	} {
		got := shellSource(visible)
		if !strings.Contains(got, "Run") {
			t.Errorf("visible printf %q was hidden: %q", visible, got)
		}
	}
}

func TestHeredocHeadingChainsKeepOperationsAndBody(t *testing.T) {
	for _, tc := range []struct{ chain, child string }{
		{"cat <<'EOF' && printf 'Section:'", "cat <<'EOF' "},
		{"cat <<'EOF' && printf 'Section:'; echo done", "cat <<'EOF' ; echo done"},
		{"cat <<'EOF' && printf 'Section:' && ls state/*", "cat <<'EOF' && ls state/*"},
	} {
		body := "\nretained body\nEOF"
		if got, want := ShellSource(tc.chain+body, ""), ShellSource(tc.child+body, ""); got != want {
			t.Errorf("heredoc inside chain = %q, want %q", got, want)
		}
	}
	for _, chain := range []string{
		`printf 'Section:' && printf 'Another:' && ls state/*; echo done`,
		`cat before && printf 'Section:' && ls state/*; echo done`,
		`ls state/* && printf 'Section:' && printf 'Another:'; echo done`,
	} {
		body := "cat <<'EOF'\nprintf 'literal body:'\nEOF\n"
		got := ShellSource(body+chain, "")
		if strings.Contains(got, "Section:") || strings.Contains(got, "Another:") || !strings.Contains(got, "ls state/*") || !strings.Contains(got, "echo done") || !strings.Contains(got, "printf 'literal body:'") {
			t.Errorf("heredoc chain lost operations/body or retained heading: %q", got)
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
		if got := shellSource(tc.source); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{
		"cat a\nfor f in *.go; do cat \"$f\"; done",
		"cat a\ncat b &",
	} {
		if got, want := shellSource(source), "Read `a`\n\nRun "+SourceCode(strings.TrimPrefix(source, "cat a\n")); got != want {
			t.Errorf("%s: got %q, want %q", source, got, want)
		}
	}

}

func TestClassifiedToolActivityShowsEveryOperation(t *testing.T) {
	path := strings.Repeat("a", 4200)
	input := "cat " + path + "\nrg needle src\ncat last"
	want := "Read `" + path + "`\n\nSearch `needle` in `src`\n\nRead `last`"
	if got := shellSource(input); got != want {
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
			shellSource(first + ending), shellSource("#!bash" + ending + search + ending), shellSource("#!bash" + ending + last),
		}, "\n\n")
		if got := shellSource(source); got != want {
			t.Fatalf("batch display = %q, want %q", got, want)
		}
	}

	firstSource := "#!params={\"workdir\":\"/tmp\"}\ncat first"
	secondSource := "#!python3\nprint('SESSION')"
	thirdSource := "cat third"
	source := firstSource + "\n" + secondSource + "\n#!bash\n" + thirdSource
	want := "Read `first`\n\n" +
		shellSource("#!python3\n#!params={\"workdir\":\"/tmp\"}\nprint('SESSION')\n") +
		"\n\nRead `third`"
	if got := shellSource(source); got != want {
		t.Fatalf("mixed interpreters and inherited params = %q, want %q", got, want)
	}
	for _, invalid := range []string{"#!bash\n#!bash\ncat first", "cat first\n#!bash\n"} {
		if got := shellSource(invalid); got != "Run\n"+markdownsyntax.SourceFence("", invalid) {
			t.Fatalf("invalid batch lost source: %q", got)
		}
	}
}

func TestSubagentMixedHeredocPreview(t *testing.T) {
	const body = "cat <<EOF; printf done\nhello\nEOF\n"
	source := "printf 'Working'\n" + body + "printf 'Finished'\n"
	want := "Run\n" + markdownsyntax.SourceFence("bash", strings.TrimRight(source, "\n"))
	if got := shellSource(source); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	source = "cat a\n" + body
	if got, want := shellSource(source), "Run\n"+markdownsyntax.SourceFence("bash", source); got != want {
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
		if got := shellSource(tc.source); got != tc.want {
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
		if got := shellSource(source); got != "Run\n"+markdownsyntax.SourceFence("bash", source) {
			t.Errorf("%s: unexpected classification %q", source, got)
		}
	}
}

func TestSubagentSymbolQueryDisplay(t *testing.T) {
	for _, tc := range []struct{ args, label string }{
		{`refs sample.go Target`, "Search"},
		{`-- refs sample.go Target`, "Search"},
		{`refs sample.go 12 Target 2`, "Search"},
		{`--workspace '/a b' --max-tokens=100 def 'sample file.go:12' 'pkg.Target'`, "Read"},
		{`def sample.go Target refs 'other file.go' Other`, "Search"},
		{`refs sample.go Target def other.go Other`, "Search"},
		{`def sample.go Target`, "Read"},
		{`refs sample.go`, "Run"},
		{`def sample.go Target 2`, "Run"},
		{`refs sample.go 0 Target`, "Run"},
		{`refs sample.go Target --workspace`, "Run"},
		{`refs sample.go Target --max-tokens=bad`, "Run"},
		{`def sample.go Target refs other.go`, "Run"},
	} {
		source := "msymbol " + tc.args
		want := tc.label + " " + SourceCode(tc.args)
		if tc.label == "Run" {
			want = "Run\n" + markdownsyntax.SourceFence("bash", source)
		}
		if got := shellSource(source); got != want {
			t.Errorf("%s: got %q, want %q", source, got, want)
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
		want := "Read " + SourceCode(operand+" 1:2")
		if got := shellSource(source); got != want {
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
		if got := shellSource(source); strings.HasPrefix(got, "Read") {
			t.Errorf("%s: misclassified as %q", source, got)
		}
	}
}

func shellSource(source string) string { return ShellSource(source, "") }

func TestShellSourceFeedPreservesOperations(t *testing.T) {
	source := "timeout 5 sed -n '2,4p' /selected/file.go\n" +
		"unknown 'literal argument'\nmsymbol refs other.go Name"
	operations := feed.ParseOperations(ShellSource(source, "/selected"))
	if len(operations) != 3 {
		t.Fatalf("operations = %+v", operations)
	}
	read, run, search := operations[0], operations[1], operations[2]
	if read.Verb != "Read" || len(read.Timeouts) != 1 || read.Timeouts[0] != "5" ||
		len(read.Reads) != 1 || read.Reads[0].Path != "file.go" ||
		len(read.Reads[0].Ranges) != 1 || read.Reads[0].Ranges[0] != "2:4" {
		t.Fatalf("read source = %+v", read)
	}
	if run.Verb != "Run" || run.Label != "`unknown 'literal argument'`" {
		t.Fatalf("unclassified neighbor = %+v", run)
	}
	if search.Verb != "Search" || len(search.Reads) != 1 || search.Reads[0].Path != "refs other.go Name" {
		t.Fatalf("symbol source = %+v", search)
	}
}

func TestShellErrorOptionsKeepRecognizedNeighbors(t *testing.T) {
	for _, source := range []string{
		"set -e\ncat a.go", "set -eu; cat a.go", "set -euo pipefail && cat a.go",
		`timeout 10 env MODE=test bash -lc 'set -e; cat a.go'`,
	} {
		if got := ShellSource(source, ""); !strings.Contains(got, "Read `a.go`") || strings.Contains(got, "set -") {
			t.Errorf("source %q = %q", source, got)
		}
	}
}

func TestShellOptionsRetainPureRunAndExternalFailures(t *testing.T) {
	source := "set -e\ntimeout 10 sleep 1"
	want := "Run\n" + markdownsyntax.SourceFence("bash", source)
	if got := ShellSource(source, ""); got != feed.AddTimeouts(want, []string{"10"}) {
		t.Fatalf("timeout setup = %q", got)
	}
	for _, source := range []string{
		"env MODE=test set -e", "timeout 1 set -e", "mrun -n 5 set -e",
		"env MODE=test set -e; cat a.go", "timeout 1 set -e && cat a.go", "mrun -n 5 set -e; cat a.go",
	} {
		text := ShellSource(source, "")
		if !strings.Contains(text, "set -e") {
			t.Errorf("external command erased: %q = %q", source, text)
		}
		item := ItemContent{Kind: "tool", Text: text, Command: Command{Script: source, Display: source, Exited: true, ExitCode: 127}, Details: &ItemDetails{CommandText: source}}
		values := CommandExit(item.Values(), source, 127, []string{"set: command not found"}, 0, false)
		failed := false
		for _, value := range values {
			failed = failed || value.ExitCode == 127
		}
		if !failed {
			t.Errorf("external failure erased: %q = %+v", source, values)
		}
	}
}
