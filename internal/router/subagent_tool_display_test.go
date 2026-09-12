package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Most display tests compare one textual preview; delivery tests check message boundaries.
func subagentToolActivityText(item map[string]json.RawMessage, name string) string {
	return subagentToolActivityTextWithHistory(item, name, nil)
}

func subagentToolActivityTextWithHistory(item map[string]json.RawMessage, name string, history *mekugiHistory) string {
	return strings.Join(subagentToolActivityTexts(item, name, history, nil), "\n\n")
}

func TestSubagentToolDisplay(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"wait", `{"cell_id":"7","yield_time_ms":30000,"max_tokens":5000}`, "Still Running · operation unavailable"},
		{"functions.wait", `{"cell_id":"7","terminate":false}`, "Still Running · operation unavailable"},
		{"functions.wait", `{"cell_id":"7","terminate":true}`, "Stop · operation unavailable"},
		{"wait", `{"cell_id":""}`, "Tool call: `wait`\n`{\"cell_id\":\"\"}`"},
		{"wait", `{"cell_id":7}`, "Tool call: `wait`\n`{\"cell_id\":7}`"},
		{"external.wait", `{"cell_id":"7"}`, "Tool call: `external.wait`\n`{\"cell_id\":\"7\"}`"},
		{"shell", "cat 'a b.txt'", "Read `a b.txt`"},
		{"shell", "skills-mgr get golang-best-practices", "Skill Read `golang-best-practices`"},
		{"shell", "skills-mgr get writing-readme/references/cli.md", "Skill Reference Read `writing-readme/references/cli.md`"},
		{"shell", "skills-mgr get writing-readme/references/cli.md 10:30", "Skill Reference Read `writing-readme/references/cli.md 10:30`"},
		{"shell", "skills-mgr get writing-readme 10:30", "Skill Read `writing-readme 10:30`"},
		{"shell", "hcat /skills/writing-readme/SKILL.md 1:20", "Skill Read `writing-readme 1:20`"},
		{"shell", "  echo first\n  echo second\n", "Run\n```bash\n  echo first\n  echo second\n```"},
		{"shell", "cat /skills/writing-readme/SKILL.md", "Skill Read `writing-readme`"},
		{"shell", "cat a\ncat b", "Read `a`\n\nRead `b`"},
		{"shell", "hcat --preview-bytes 80 --max-tokens 100 a.go 1:20", "Read `a.go 1:20`"},
		{"shell", "hcat --tail -n 20 a.go", "Read `a.go`"},
		{"shell", "hcat --tail --max-tokens 100 a.go", "Read `a.go`"},
		{"shell", "hcat a.go 1:20", "Read `a.go 1:20`"},
		{"shell", "hgrep -n -F -e 'some text' a.go", "Search `-n -F -e 'some text' a.go`"},
		{"shell", "hcat --max-tokens 15500 --preview-bytes 65536 a.go 0:1", "Read `a.go 0:1`"},
		{"shell", "inspect_file a.go", "Inspect `a.go`"},
		{"shell", "ls src", "List `src`"},
		{"shell", `{"command":[]}`, "Run"},
		{"shell", `{"command":["sh","-c","echo done"]}`, "Run\n```sh\necho done\n```"},
		{"shell", "shell sh $'echo done'", "Run\n```sh\necho done\n```"},
		{"shell", "#!python\nprint(1)", "Run\n```python\n#!python\nprint(1)\n```"},
		{"shell", "#!python3\nprint(1)", "Run\n```python\n#!python3\nprint(1)\n```"},
		{"shell", "#!/usr/bin/env -S python3 -u\nprint(1)", "Run\n```python\n#!/usr/bin/env -S python3 -u\nprint(1)\n```"},
		{"shell", "#!node\nprint(1)", "Run\n```javascript\n#!node\nprint(1)\n```"},
		{"shell", "#!ruby\nprint(1)", "Run\n```ruby\n#!ruby\nprint(1)\n```"},
		{"shell", "#!sh\nprint(1)", "Run\n```sh\n#!sh\nprint(1)\n```"},
		{"shell", "#!pwsh\nprint(1)", "Run\n```powershell\n#!pwsh\nprint(1)\n```"},
		{"shell", "cat a > b", "Run\n```bash\ncat a > b\n```"},
		{"shell", "cat a && rm b", "Run\n```bash\ncat a && rm b\n```"},
		{"shell", "cat $(echo a)", "Run\n```bash\ncat $(echo a)\n```"},
		{"shell", "cat *.go", "Run\n```bash\ncat *.go\n```"},
		{"shell", "#!params={\"max_output_tokens\":20000}\ncat a\npwd", "Read `a`\n\nRun `pwd`"},
		{"shell", "echo a\n  echo b", "Run\n```bash\necho a\n  echo b\n```"},
		{"exec_command", `{"cmd":"shell bash $'cat a\\n'","login":false}`, "Read `a`"},
		{"shell", `{"command":["bash","-lc","cat a"]}`, "Read `a`"},
		{"view_image", `{"path":"/tmp/a.png"}`, "View image\n`/tmp/a.png`"},
		{"exec", `const result = await tools.exec_command({"cmd":"shell bash $'cat a\\n'","login":false}); text(JSON.stringify(Object.assign({}, result, {"retained":false})));`, "Read `a`"},
		{"exec", `await tools.exec_command({"cmd":"echo a\necho b"})`, "Run\n```bash\necho a\necho b\n```"},
		{"exec", `const r = await tools.write_stdin({session_id: 52915, chars: "", yield_time_ms: 30000, max_output_tokens: 3000}); text(r);`, "Still Running · command unavailable"},
		{"exec", `await tools.write_stdin({session_id: -12, chars: ""})`, "Still Running · command unavailable"},
		{"exec", `await tools.write_stdin({session_id: 9007199254740993, chars: ""})`, "Still Running · command unavailable"},
		{"exec", `await tools.write_stdin({session_id: -9007199254740993, chars: ""})`, "Still Running · command unavailable"},
		{"exec", `await tools.exec_command({cmd: 'cat a', login: false})`, "Read `a`"},
		{"exec", `await tools.apply_patch("*** Begin Patch\n*** Add File: a\n+x\n*** End Patch\n")`, "Write `a`\n```diff\n+x\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.input, func(t *testing.T) {
			item := map[string]json.RawMessage{"name": mustMarshalJSON(tt.name), "input": mustMarshalJSON(tt.input)}
			if got := subagentToolActivityText(item, tt.name); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
func TestSubagentMCPToolDisplay(t *testing.T) {
	name := "mcp__openaiDeveloperDocs__search_openai_docs"
	arguments := `{"query":"Codex subagents model catalog switching threads","limit":5}`
	want := "MCP `openaiDeveloperDocs.search_openai_docs`\n"
	item := map[string]json.RawMessage{"name": mustMarshalJSON(name), "arguments": mustMarshalJSON(arguments)}
	if got := subagentToolActivityText(item, "functions."+name); got != want+commentaryCode(arguments) {
		t.Fatalf("native MCP display = %q", got)
	}
	for _, source := range []string{
		`const r = await tools.` + name + `({query:"Codex subagents model catalog switching threads",limit:5}); text(r);`,
		`text(await tools.` + name + `({query:"Codex subagents model catalog switching threads",limit:5}));`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		got := subagentToolActivityText(item, "functions.exec")
		if got != want+commentaryCode(`{"limit":5,"query":"Codex subagents model catalog switching threads"}`) {
			t.Fatalf("wrapped MCP display = %q", got)
		}
		if jsonString(item, "input") != source {
			t.Fatal("display changed executable input")
		}
	}
	for _, source := range []string{
		`const r = await tools.` + name + `({query:query}); text(r);`,
		`const r = await tools.` + name + `({query:"test"}); text(r); other();`,
		`await tools.mcp__server__({});`,
		`await tools.mcp____tool({});`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		if got := subagentToolActivityText(item, "exec"); got != toolActivityJavaScript(source) {
			t.Fatalf("nontransparent MCP display = %q", got)
		}
	}
}

func TestSubagentToolDisplayInvalidReaderOptions(t *testing.T) {
	for _, input := range []string{
		"hcat -n 0 a.go", "hcat -n 1 -n 2 a.go", "hcat -n 9007199254740992 a.go",
		"hcat --max-tokens 0 a.go", "hcat --max-tokens -1 a.go",
		"hcat --max-tokens 15501 a.go", "hcat --max-tokens 01 a.go",
		"hcat --max-tokens 1 --max-tokens 2 a.go", "hcat --max-tokens a.go",
		"hcat --preview-bytes 65537 a.go", "hcat --preview-bytes nope a.go",
		"hcat --preview-bytes 1 --preview-bytes 2 a.go", "hcat a.go --max-tokens 1",
		"hcat a.go 2:1", "hcat a.go 1:0", "hcat a.go 01:2", "hcat a.go invalid",
		"hcat a.go 1:9007199254740992",
		"inspect_file --source-bytes 100 a.go",
		"inspect_file --source Main --source Other a.go",
		"inspect_file --source Main --source-bytes 0 a.go",
		"inspect_file --source Main --source-bytes 8193 a.go",
		"inspect_file --source Main --source-bytes +1 a.go",
		"inspect_file --source Main --source-bytes 01 a.go",
		"inspect_file --source Main --source-bytes 1 --source-bytes 2 a.go",
		"inspect_file a.go --source Main",
		"inspect_file ''", "inspect_file @shell/script", "inspect_file dir/../@shell/script",
	} {
		t.Run(input, func(t *testing.T) {
			item := map[string]json.RawMessage{"name": mustMarshalJSON("shell"), "input": mustMarshalJSON(input)}
			want := "Run\n```bash\n" + input + "\n```"
			if got := subagentToolActivityText(item, "shell"); got != want {
				t.Fatalf("display = %q, want source fallback %q", got, want)
			}
		})
	}
}

func TestSubagentCodeModeOutputProjection(t *testing.T) {
	command := "python3 - <<'PY'\nimport pathlib, json\nprint('done')\nPY"
	for _, projection := range []string{
		"text(r.output)", "text ( r . output )", "text(/* output */ r.output)",
		"text(r)", "text ( JSON . stringify ( r ) )",
		`text(JSON.stringify(Object.assign( { }, r, { retained : false } )))`,
	} {
		source := "const r = await tools.exec_command({cmd:" +
			string(mustMarshalJSON(command)) +
			`,workdir:"/tmp",yield_time_ms:30000,max_output_tokens:12000});` +
			"\n" + projection + ";"
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		want := "Run\n```bash\n" + command + "\n```"
		if got := subagentToolActivityText(item, "exec"); got != want {
			t.Fatalf("display = %q, want %q", got, want)
		}
	}
}

func TestSubagentWriteStdinDisplay(t *testing.T) {
	for _, tt := range []struct {
		arguments string
		want      string
	}{
		{`{"session_id":52915}`, "Still Running · command unavailable"},
		{`{"session_id":52915,"chars":""}`, "Still Running · command unavailable"},
		{`{"session_id":52915,"chars":"yes"}`, "Send input\n`yes`"},
	} {
		for _, projection := range []string{"text(r)", "text (r . output)", "text(JSON.stringify(r))"} {
			source := "const r = await tools . write_stdin(" + tt.arguments + ");\n" + projection + ";"
			item := map[string]json.RawMessage{"input": mustMarshalJSON(source)}
			native := map[string]json.RawMessage{"arguments": mustMarshalJSON(tt.arguments)}
			want := subagentToolActivityText(native, "write_stdin")
			if want != tt.want {
				t.Fatalf("native display = %q, want %q", want, tt.want)
			}
			if got := subagentToolActivityText(item, "exec"); got != want {
				t.Fatalf("Code Mode display = %q, want native display %q", got, want)
			}
		}
	}
}

func TestSubagentInlineAwaitDisplay(t *testing.T) {
	for _, tt := range []struct {
		source, want string
	}{
		{
			`text(await tools.exec_command({cmd:"cat /home/ubuntu/.codex/IMPLEMENTATION.md; git diff --stat; git diff -- internal/router/subagent_tool_display.go doc/spec/commentary.md",max_output_tokens:11000}));`,
			"Read `/home/ubuntu/.codex/IMPLEMENTATION.md`\n\nRun `git diff --stat;`\n\nRun `git diff -- internal/router/subagent_tool_display.go doc/spec/commentary.md`",
		},
		{
			`text(await tools.exec_command({cmd:"skills-mgr get golang-best-practices; sed -n '1,245p' internal/router/subagent_tool_display.go; sed -n '320,475p' internal/router/subagent_tool_display.go; git diff -- internal/router/subagent_tool_display_test.go",max_output_tokens:10100}));`,
			"Skill Read `golang-best-practices`\n\nRead `internal/router/subagent_tool_display.go 1:245`\n\nRead `internal/router/subagent_tool_display.go 320:475`\n\nRun `git diff -- internal/router/subagent_tool_display_test.go`",
		},
		{
			`text(await tools.exec_command({cmd:"gopls references internal/router/subagent_tool_display.go:17:6; sed -n '60,135p' doc/spec/commentary.md; sed -n '1,65p' internal/router/subagent_tool_display_test.go; sed -n '540,650p' internal/router/subagent_tool_display.go",max_output_tokens:5000}));`,
			"Run `gopls references internal/router/subagent_tool_display.go:17:6;`\n\nRead `doc/spec/commentary.md 60:135`\n\nRead `internal/router/subagent_tool_display_test.go 1:65`\n\nRead `internal/router/subagent_tool_display.go 540:650`",
		},
		{
			`text(await tools.write_stdin({session_id:23221,chars:"",yield_time_ms:1000,max_output_tokens:5000}));`,
			"Still Running · command unavailable",
		},
	} {
		t.Run(tt.source, func(t *testing.T) {
			item := map[string]json.RawMessage{"input": mustMarshalJSON(tt.source)}
			if got := subagentToolActivityText(item, "exec"); got != tt.want {
				t.Fatalf("display = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubagentRunInterpreterLanguages(t *testing.T) {
	for _, tt := range []struct {
		interpreters []string
		language     string
	}{
		{[]string{"python2.7", "python3.12", "pythonw3", "pypy", "pypy3", "/usr/bin/python3.13", `C:\Python\python3.exe`, "/usr/bin/env -S python3.12 -u"}, "python"},
		{[]string{"nodejs", "bun", "deno", "qjs", "quickjs"}, "javascript"},
		{[]string{"ts-node", "ts-node-esm", "tsx"}, "typescript"},
		{[]string{"ruby3.3", "jruby", "truffleruby"}, "ruby"},
		{[]string{"perl5.40"}, "perl"},
		{[]string{"php8.3"}, "php"},
		{[]string{"lua5.4", "luajit2.1"}, "lua"},
		{[]string{"tclsh8.6", "wish"}, "tcl"},
		{[]string{"Rscript"}, "r"},
		{[]string{"runghc", "runghc9.8", "runhaskell"}, "haskell"},
		{[]string{"powershell.exe", "pwsh"}, "powershell"},
		{[]string{"ash", "dash", "ksh93"}, "bash"},
		{[]string{"gawk", "mawk", "nawk"}, "awk"},
		{[]string{"julia"}, "julia"},
		{[]string{"fish"}, "fish"},
		{[]string{"custom3.2"}, "custom3.2"},
		{[]string{"python-helper"}, "python-helper"},
	} {
		for _, interpreter := range tt.interpreters {
			t.Run(interpreter, func(t *testing.T) {
				source := "#!" + interpreter + "\nsource `with` backticks\n"
				item := map[string]json.RawMessage{"name": mustMarshalJSON("shell"), "input": mustMarshalJSON(source)}
				want := "Run\n```" + tt.language + "\n" + source + "```"
				if got := subagentToolActivityText(item, "shell"); got != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			})
		}
	}
}

func TestSubagentToolDisplayDoesNotUnwrapArbitraryCode(t *testing.T) {
	for _, source := range []string{
		`if (false) await tools.exec_command({"cmd":"cat a"})`,
		`await tools.exec_command({"cmd":"cat a"}); await tools.exec_command({"cmd":"rm b"})`,
		`text(await tools.exec_command({cmd:"cat a"}), other())`,
		`other(await tools.exec_command({cmd:"cat a"}))`,
		`text(await tools.exec_command({cmd:command}))`,
		`text(await tools.exec_command({cmd:"cat a"})); other()`,
		`text?.(await tools.exec_command({cmd:"cat a"}))`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(r["output"])`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(r.output, other())`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(JSON.stringify(other))`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(JSON.stringify(Object.assign({}, other, {retained:false})))`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(JSON.stringify(Object.assign({}, r, {retained:other()})))`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(other.output)`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(r.output())`,
		`const r = await tools.exec_command({cmd:"cat a"}); text(r.output); other()`,
		`const r = await tools.exec_command({"cmd":"cat a"}); text(other())`,
		`await tools.exec_command({"cmd":command})`,
		`await tools.exec_command({"cmd":"cat a"}`,
		`await tools.exec_command({["cmd"]:"cat a"})`,
		`await tools.exec_command({...args})`,
		`await tools.exec_command({get cmd() { return "cat a" }})`,
		`await tools.shell({command: ["cat", , "a"]})`,
		`await tools.write_stdin({session_id: 0x10, chars: ""})`,
		`await tools.write_stdin({session_id: 1_000, chars: ""})`,
		`await tools.write_stdin({session_id: 1n, chars: ""})`,
		`await tools.write_stdin({session_id: -0x10, chars: ""})`,
		`await tools.write_stdin({session_id: -1_000, chars: ""})`,
		`await tools.write_stdin({session_id: -1n, chars: ""})`,
		`await tools.write_stdin({session_id: 1e309, chars: ""})`,
		`await tools.write_stdin({session_id: -1e309, chars: ""})`,
		`await tools.exec_command({cmd: "cat a", \u0063md: "cat b"})`,
	} {
		if _, ok := toolActivityUnwrapExec(source, false); ok {
			t.Fatalf("unwrapped nontransparent code: %s", source)
		}
	}

	source := `await tools.exec_command({cmd: command})`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	want := "Run JavaScript\n```javascript\n" + source + "\n```"
	if got := subagentToolActivityText(item, "exec"); got != want {
		t.Fatalf("dynamic display = %q, want %q", got, want)
	}
}

func TestToolActivityMultilineFence(t *testing.T) {
	got := toolActivityCode("echo '```'\necho done")
	if !strings.HasPrefix(got, "````\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("unsafe fence: %s", got)
	}
}

func TestSubagentBuiltinToolDisplay(t *testing.T) {
	tests := []struct {
		source, want string
	}{
		{`{"type":"shell_call","action":{"commands":["echo a","echo b"]}}`, "Run\n```bash\necho a\necho b\n```"},
		{`{"type":"local_shell_call","action":{"command":["bash","-lc","cat a"]}}`, "Read `a`"},
		{`{"type":"web_search_call","action":{"type":"open_page","url":"https://example.com"}}`, "Open page\n`https://example.com`"},
		{`{"type":"file_search_call","queries":["alpha","beta"]}`, "Search files\n```\nalpha\nbeta\n```"},
		{`{"type":"image_generation_call"}`, "Generate image"},
		{`{"type":"code_interpreter_call","code":"print(1)\nprint(2)"}`, "Run code\n```\nprint(1)\nprint(2)\n```"},
	}
	for _, tt := range tests {
		var item map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tt.source), &item); err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(jsonString(item, "type"), "_call")
		if got := subagentToolActivityText(item, name); got != tt.want {
			t.Fatalf("source %s: got %q, want %q", tt.source, got, tt.want)
		}
	}
}

func TestSubagentSedReadDisplay(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"sed -n '1,260p' source.go", "Read `source.go 1:260`"},
		{"sed -n '261,520p' 'source file.go'", "Read `source file.go 261:520`"},
	} {
		if got := toolActivityShell(tc.source); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.source, got, tc.want)
		}
	}
	for _, source := range []string{
		"sed -n '1,260p;d' source.go",
		"sed -i -n '1,260p' source.go",
		"sed -n '1,$p' source.go",
		"sed -n '0,260p' source.go",
		"sed -n '260,1p' source.go",
		"sed -n '+1,260p' source.go",
		"sed -n '1,260p' -",
		"sed -n '1,260p' --version",
		"sed -n '1,260p' ''",
		"sed -n '1,260p' a.go b.go",
		"sed -n '1,260p' \"$file\"",
		"sed -n '1,260p' source.go > copy.go",
		"sed -n '1,260p' source.go && echo done",
	} {
		want := "Run\n" + toolActivityFenced("bash", source)
		if got := toolActivityShell(source); got != want {
			t.Errorf("%s: got %q, want raw source %q", source, got, want)
		}
	}
}

func TestSubagentSearchReadRunGrouping(t *testing.T) {
	source := "rg -n 'func shellCatLiteral|func toolActivityGroup|toolActivityGroup\\(' internal/router\n" +
		"sed -n '238,265p' internal/router/subagent_tool_display.go\n" +
		"sed -n '140,170p' doc/spec/commentary.md\n" +
		"git status --short"
	item := map[string]json.RawMessage{"name": mustMarshalJSON("shell"), "input": mustMarshalJSON(source)}
	want := "In `/root/review_sed_display`\n\n" +
		"- Search `-n 'func shellCatLiteral|func toolActivityGroup|toolActivityGroup\\(' internal/router`\n\n" +
		"- Read `internal/router/subagent_tool_display.go 238:265` `doc/spec/commentary.md 140:170`\n\n" +
		"- Run `git status --short`"
	if got := toolActivityGroup("[`/root/review_sed_display`] ", subagentToolActivityText(item, "shell")); got != want {
		t.Fatalf("grouped display: got %q, want %q", got, want)
	}
}

func TestSubagentMixedReadRunFallbacks(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"git status --short\ncat a", "Run `git status --short`\n\nRead `a`"},
		{"cat a\nsed -n '1,$p' b", "Read `a`\n\nRun `sed -n '1,$p' b`"},
		{"cat a\ncat \"$file\"", "Read `a`\n\nRun `cat \"$file\"`"},
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
		"cat a\ncat b > c",
		"cat a\ncat b && echo done",
		"cat a\nfor f in *.go; do cat \"$f\"; done",
		"cat a\ncat b &",
	} {
		if got, want := toolActivityShell(source), "Read `a`\n\nRun "+toolActivityCode(strings.TrimPrefix(source, "cat a\n")); got != want {
			t.Errorf("%s: got %q, want %q", source, got, want)
		}
	}
	want := "In `/root/worker`\n\n- Read `a`\n\n- Run `echo first` `echo second`"
	if got := toolActivityGroup("[`/root/worker`] ", toolActivityShell("cat a\necho first\necho second")); got != want {
		t.Fatalf("adjacent runs: got %q, want %q", got, want)
	}
}

func TestSubagentMixedSedReadGrouping(t *testing.T) {
	source := "#!params={\"workdir\":\"/project\",\"yield_time_ms\":10000,\"max_output_tokens\":30000}\n" +
		"cat /project/COLLABORATION.md\n" +
		"skills-mgr get deltapath-go-common-patterns\n" +
		"skills-mgr get golang-best-practices\n" +
		"sed -n '1,260p' source.go\n" +
		"sed -n '261,520p' 'source file.go'"
	item := map[string]json.RawMessage{"name": mustMarshalJSON("shell"), "input": mustMarshalJSON(source)}
	want := "In `/root/dect_evidence`\n\n" +
		"- Read `/project/COLLABORATION.md`\n\n" +
		"- Skill Read `deltapath-go-common-patterns` `golang-best-practices`\n\n" +
		"- Read `source.go 1:260` `source file.go 261:520`"
	got := toolActivityGroup("[`/root/dect_evidence`] ", subagentToolActivityText(item, "shell"))
	if got != want {
		t.Fatalf("grouped display: got %q, want %q", got, want)
	}
}

func TestClassifiedToolActivityShowsEveryOperation(t *testing.T) {
	path := strings.Repeat("a", 4200)
	input := "cat " + path + "\nrg needle src\ncat last"
	want := "Read `" + path + "`\n\nSearch `needle src`\n\nRead `last`"
	if got := toolActivityShell(input); got != want {
		t.Fatalf("display: got %q, want %q", got, want)
	}
}

func TestSubagentEditDisplayUsesRetainedTranslation(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a\n@@\n-old\n+new\n*** End Patch\n"
	want := "Edit `a`\n```diff\n@@\n-old\n+new\n```"
	for _, name := range []string{"hpatch", "hpatch_recover"} {
		item := map[string]json.RawMessage{
			"name":    mustMarshalJSON(name),
			"call_id": mustMarshalJSON("call-edit"),
			"input":   mustMarshalJSON("source edit"),
		}
		history := &mekugiHistory{toolName: name, script: "source edit", patch: patch}
		if got := subagentToolActivityTextWithHistory(item, name, history); got != want {
			t.Fatalf("%s translated display = %q, want %q", name, got, want)
		}
		history.translationError = "rejected"
		if got := subagentToolActivityTextWithHistory(item, name, history); got != "Edit\n`source edit`" {
			t.Fatalf("%s rejected display = %q", name, got)
		}
	}

	for _, item := range []map[string]json.RawMessage{
		{"name": mustMarshalJSON("apply_patch"), "input": mustMarshalJSON(patch)},
		{"name": mustMarshalJSON("apply_patch"), "arguments": mustMarshalJSON(`{"patch":` + string(mustMarshalJSON(patch)) + `}`)},
	} {
		if got := subagentToolActivityText(item, "apply_patch"); got != want {
			t.Fatalf("native apply_patch display = %q, want %q", got, want)
		}
	}
}

func TestToolActivityNestedLanguageFencePreservesBlankLinesAndBackticks(t *testing.T) {
	display := toolActivityDiff("Edit", "+before\n+``` literal\n\n+`after`")
	if !strings.HasPrefix(display, "Edit\n````diff\n") {
		t.Fatalf("diff fence did not avoid literal backticks: %q", display)
	}
	nested := toolActivityNested(display)
	want := "- Edit\n  ````diff\n  +before\n  +``` literal\n  \n  +`after`\n  ````"
	if nested != want {
		t.Fatalf("nested language fence = %q, want %q", nested, want)
	}
}

func TestSubagentEditDisplayFileSections(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: a b.txt\n+*** Begin Patch\n+```\n+\n" +
		"*** Update File: old.txt\n*** Move to: new.txt\n@@\n-old\n+new\n*** End of File\n" +
		"*** Delete File: obsolete.txt\n*** End Patch\n"
	want := "Write `a b.txt`\n````diff\n+*** Begin Patch\n+```\n+\n````\n\n" +
		"Move `old.txt` → `new.txt`\n```diff\n@@\n-old\n+new\n```\n\nDelete `obsolete.txt`"
	for _, source := range []string{patch, strings.ReplaceAll(patch, "\n", "\r\n")} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("apply_patch"), "input": mustMarshalJSON(source)}
		if got := subagentToolActivityText(item, "apply_patch"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestSubagentEditDisplayUnrecognizedPatch(t *testing.T) {
	for _, patch := range []string{
		"*** Begin Patch\n*** Update File: a\n+incomplete",
		"*** Begin Patch\n*** Update File: \n+missing path\n*** End Patch",
		"*** Begin Patch\nno file header\n*** End Patch",
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("apply_patch"), "input": mustMarshalJSON(patch)}
		want := "Edit\n```diff\n" + patch + "\n```"
		if got := subagentToolActivityText(item, "apply_patch"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestSubagentAdditionalBuiltinDisplays(t *testing.T) {
	for _, tc := range []struct{ name, label string }{
		{"list_mcp_resources", "List MCP resources"},
		{"list_mcp_resource_templates", "List MCP resource templates"},
		{"read_mcp_resource", "Read MCP resource"},
		{"clock__curr_time", "Read current time"},
		{"clock__sleep", "Sleep"},
		{"get_context_remaining", "Check remaining context"},
		{"new_context", "Start new context"},
		{"create_goal", "Create goal"}, {"get_goal", "Read goal"}, {"update_goal", "Update goal"},
		{"web__run", "Browse web"}, {"image_gen__imagegen", "Generate image"},
		{"wait", "Wait for execution"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := `{"value":"full detail"}`
			want := tc.label + "\n" + commentaryCode(args)
			item := map[string]json.RawMessage{"name": mustMarshalJSON(tc.name), "arguments": mustMarshalJSON(args)}
			if got := subagentToolActivityText(item, tc.name); got != want {
				t.Fatalf("native = %q", got)
			}
			source := "const r = await tools." + tc.name + "(" + args + "); text(r);"
			item = map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
			if got := subagentToolActivityText(item, "exec"); got != want {
				t.Fatalf("wrapper = %q", got)
			}
		})
	}
	for _, tc := range []struct{ namespace, name, want string }{
		{"mcp__docs", "search", "MCP `docs.search`"},
		{"mcp__docs__v2", "search", "MCP `docs__v2.search`"},
		{"clock", "curr_time", "Read current time"},
		{"web", "run", "Browse web"},
		{"image_gen", "imagegen", "Generate image"},
	} {
		item := map[string]json.RawMessage{"namespace": mustMarshalJSON(tc.namespace), "name": mustMarshalJSON(tc.name)}
		if got := subagentToolActivityText(item, tc.namespace+"."+tc.name); got != tc.want {
			t.Fatalf("namespaced = %q", got)
		}
	}
	source := `const r = await tools.image_gen__imagegen({prompt:"A bird"}); generatedImage(r);`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	if got := subagentToolActivityText(item, "exec"); got != "Generate image\n`{\"prompt\":\"A bird\"}`" {
		t.Fatal(got)
	}
}

func TestSubagentStaticMultiCallDisplays(t *testing.T) {
	for _, source := range []string{
		`text(await tools.list_mcp_resources({})); text(await tools.clock__curr_time({}));`,
		`const r = await tools.list_mcp_resources({}); text(r); const clock = await tools.clock__curr_time({}); text(clock);`,
		`text(await Promise.all([tools.list_mcp_resources({}), tools.clock__curr_time({})]));`,
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); text(results);`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		want := "List MCP resources\n`{}`\n\nRead current time\n`{}`"
		if got := subagentToolActivityText(item, "exec"); got != want {
			t.Fatalf("%s: %q", source, got)
		}
		if _, ok := toolActivityUnwrapExec(source, true); ok {
			t.Fatal("batch became session evidence")
		}
		if jsonString(item, "input") != source {
			t.Fatal("source changed")
		}
	}
	for _, source := range []string{
		`await tools.list_mcp_resources({}); other();`,
		`if (flag) await tools.list_mcp_resources({});`,
		`text(await Promise.all([tools.list_mcp_resources(args), tools.clock__curr_time({})]));`,
		`text(await Promise.all([tools.list_mcp_resources({}), other()]));`,
		`const tools = await tools.list_mcp_resources({}); text(tools);`,
		`const text = await tools.list_mcp_resources({}); text(text);`,
		`const Promise = await tools.list_mcp_resources({}); text(Promise);`,
		`await tools.update_plan({});`,
		`await tools.list_mcp_resources({}); await tools.update_plan({});`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		if got := subagentToolActivityText(item, "exec"); got != toolActivityJavaScript(source) {
			t.Fatalf("unsafe or excluded %s: %q", source, got)
		}
	}
	item := map[string]json.RawMessage{"name": mustMarshalJSON("update_plan"), "arguments": mustMarshalJSON(`{}`)}
	if got := subagentToolActivityText(item, "update_plan"); got != "Tool call: `update_plan`\n`{}`" {
		t.Fatal(got)
	}
}

func TestSubagentBatchPatchFilesStaySeparate(t *testing.T) {
	source := `text(await tools.apply_patch("*** Begin Patch\n*** Add File: a\n+x\n*** Add File: b\n+y\n*** End Patch\n")); text(await tools.clock__curr_time({}));`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	displays := subagentToolActivityTexts(item, "exec", nil, nil)
	if len(displays) != 3 || displays[0] != "Write `a`\n```diff\n+x\n```" || displays[1] != "Write `b`\n```diff\n+y\n```" || displays[2] != "Read current time\n`{}`" {
		t.Fatalf("patch file boundaries = %q", displays)
	}
}

func TestShellBatchActivityDisplay(t *testing.T) {
	const first = "sed -n '1,360p' internal/router/session_inspect.go"
	const search = "rg -n '^func Test' internal/router/session_inspect_test.go cmd/mekugi/main_test.go 2>/dev/null"
	const last = "git status --short --branch\ngit log -1 --oneline"
	for _, marker := range []string{"#!batch=SESSION", "#!batch-stop=SESSION"} {
		source := marker + "\n" + first + "\nSESSION\n" + search + "\nSESSION\n" + last
		want := strings.Join([]string{
			toolActivityShell(first), toolActivityShell(search), toolActivityShell(last),
		}, "\n\n")
		if got := toolActivityShell(source); got != want {
			t.Fatalf("batch display = %q, want %q", got, want)
		}
	}

	firstSource := "#!params={\"workdir\":\"/tmp\"}\ncat first"
	secondSource := "#!python3\nprint('SESSION')"
	thirdSource := "cat third"
	source := "#!batch=SESSION\n" + firstSource + "\nSESSION\n" + secondSource + "\nSESSION\n" + thirdSource
	want := "Read `first`\n\n" +
		toolActivityShell("#!python3\n#!params={\"workdir\":\"/tmp\"}\nprint('SESSION')\n") +
		"\n\nRead `third`"
	if got := toolActivityShell(source); got != want {
		t.Fatalf("mixed interpreters and inherited params = %q, want %q", got, want)
	}
	for _, invalid := range []string{"#!batch=SESSION\ncat first", "#!batch=SESSION\ncat first\nSESSION\n"} {
		if got := toolActivityShell(invalid); got != "Run\n"+toolActivityFenced("", invalid) {
			t.Fatalf("invalid batch lost source: %q", got)
		}
	}
}

func TestSubagentShellJournalAndDiscovery(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"journal add 'Checking tools.'", ""},
		{"journal add \"$progress\"\njournal add 'More progress.'", ""},
		{"journal add 'Checking.'\nprintf done", "Run `printf done`"},
		{"journal add 'Checking.'\nhgrep --max-tokens 2000 -F -e 'needle' a.go", "Search `--max-tokens 2000 -F -e 'needle' a.go`"},
		{"command -v codex-code-mode-host || true", "Inspect `command -v codex-code-mode-host || true`"},
		{"find /home/ubuntu/projects/codex -type f -name codex-code-mode-host -perm -111 -print 2>/dev/null | head -n 40",
			"Search `find /home/ubuntu/projects/codex -type f -name codex-code-mode-host -perm -111 -print 2>/dev/null | head -n 40`"},
		{"find /home/ubuntu/projects/codex -type f -path '*/target/*' -name '*code*mode*host*' -print 2>/dev/null | head -n 40",
			"Search `find /home/ubuntu/projects/codex -type f -path '*/target/*' -name '*code*mode*host*' -print 2>/dev/null | head -n 40`"},
		{"ls -ld /clone/code-mode-host /clone/code-mode-runtime", "List `-ld /clone/code-mode-host /clone/code-mode-runtime`"},
		{"hgrep --max-tokens 2000 needle a.go\nfalse || true", "Search `--max-tokens 2000 needle a.go`\n\nRun `false || true`"},
		{"#!batch=NEXT\njournal add 'Working'\nNEXT\ncat a", "Read `a`"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			if got := toolActivityShell(tc.source); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, source := range []string{
		"journal add \"$(touch marker)\"",
		"find /tmp/cache -type f -delete",
		"find /tmp/cache -type f -exec rm '{}' ';'",
		"find /tmp/cache -fprint results",
		"journal add 'Working' > progress.txt",
		"find /tmp -print 2>errors | head -n 40",
		"find /tmp -print | head -n \"$limit\"",
		"command -v tool || rm file",
		"#!python3\njournal('Working')",
	} {
		if got := toolActivityShell(source); !strings.HasPrefix(got, "Run\n") || !strings.Contains(got, source) {
			t.Fatalf("unsafe simplification: %q", got)
		}
	}
}

func TestSubagentDiscoveryActivityJSONAndSSE(t *testing.T) {
	const script = "journal add 'Checking whether the host is installed.'\n" +
		"command -v codex-code-mode-host || true\n" +
		"find /clone -type f -name codex-code-mode-host -perm -111 -print 2>/dev/null | head -n 40\n" +
		"find /clone -type f -path '*/target/*' -name '*code*mode*host*' -print 2>/dev/null | head -n 40\n" +
		"ls -ld /clone/code-mode-host /clone/code-mode-runtime\n" +
		"hgrep --max-tokens 2000 -F host /clone/config"
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
			child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/host_test_evidence", nil)
			for i, source := range []string{"journal add 'Progress only.'", script} {
				call := map[string]any{
					"type": "custom_tool_call", "name": "shell", "id": fmt.Sprintf("display-%d", i),
					"call_id": fmt.Sprintf("display-%d", i), "input": source,
				}
				if stream {
					if _, err := child.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": call})); err != nil {
						t.Fatal(err)
					}
				} else if _, err := child.TransformJSON(mustMarshalJSON(map[string]any{"status": "completed", "output": []any{call}})); err != nil {
					t.Fatal(err)
				}
			}
			output, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Inspect", "Search", "List", "--max-tokens 2000", "/root/host_test_evidence", "*code*mode*host*"} {
				if !strings.Contains(string(output), want) {
					t.Fatalf("missing %q: %s", want, output)
				}
			}
			for _, hidden := range []string{"Progress only.", "Checking whether", "Run", "journal add '", "```bash"} {
				if strings.Contains(string(output), hidden) {
					t.Fatalf("unexpected %q: %s", hidden, output)
				}
			}
		})
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
