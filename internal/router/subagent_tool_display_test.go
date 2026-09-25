package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func stockExecDisplayItem(command string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"name":      mustMarshalJSON("exec_command"),
		"arguments": mustMarshalJSON(string(mustMarshalJSON(map[string]string{"cmd": command}))),
	}
}

// Most display tests compare one textual preview; delivery tests check message boundaries.
func subagentToolActivityText(item map[string]json.RawMessage, name string) string {
	return subagentToolPreview(item, name, nil)
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
		{"exec_command", `{"cmd":"cat 'a b.txt'"}`, "Read `a b.txt`"},
		{"exec_command", `{"cmd":"rg -n -F -e 'some text' a.go"}`, "Search `some text` in `a.go`"},
		{"exec_command", `{"cmd":"mcat a.go 1:20"}`, "Read `a.go 1:20`"},
		{"exec_command", `{"cmd":"#!python\nprint(1)"}`, "Run\n```python\n#!python\nprint(1)\n```"},
		{"exec_command", `{"cmd":"msymbol refs a.go 42 Name","login":false}`, "Search `refs a.go 42 Name`"},
		{"exec_command", `{"cmd":"msymbol def a.go Name","login":false}`, "Read `def a.go Name`"},
		{"exec_command", `{"cmd":"msymbol def a.go:42 Name refs a.go 42 Name","login":false}`, "Search `def a.go:42 Name refs a.go 42 Name`"},
		{"exec_command", `{"cmd":"msymbol --max-tokens=4000 def a.go:42 Name","login":false}`, "Read `--max-tokens=4000 def a.go:42 Name`"},
		{"exec_command", `{"cmd":"inspect_file a.go","login":false}`, "Inspect `a.go`"},
		{"view_image", `{"path":"/tmp/a.png"}`, "View image\n`/tmp/a.png`"},
		{"exec", `await tools.exec_command({"cmd":"echo a\necho b"})`, "Run\n```bash\necho a\necho b\n```"},
		{"exec", `const r = await tools.write_stdin({session_id: 52915, chars: "", yield_time_ms: 30000, max_output_tokens: 3000}); text(r);`, "Still Running"},
		{"exec", `await tools.write_stdin({session_id: -12, chars: ""})`, "Still Running"},
		{"exec", `await tools.write_stdin({session_id: 9007199254740993, chars: ""})`, "Still Running"},
		{"exec", `await tools.write_stdin({session_id: -9007199254740993, chars: ""})`, "Still Running"},
		{"exec", `await tools.exec_command({cmd: 'cat a', login: false})`, "Read `a`"},
		{"exec", `await tools.apply_patch("*** Begin Patch\n*** Add File: a\n+x\n*** End Patch\n")`, ""},
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

func TestWebRunActivityPreview(t *testing.T) {
	for _, tc := range []struct {
		arguments, want string
	}{
		{`{"search_query":[{"q":"release notes"}],"response_length":"short"}`, "Search web"},
		{`{"open":[{"ref_id":"turn123search0"}]}`, "Open page"},
		{`{"find":[{"ref_id":"turn123search0","pattern":"install"}]}`, "Find in page"},
		{`{"search_query":[{"q":"release notes"}],"open":[{"ref_id":"turn123search0"}]}`, "Browse web"},
	} {
		for _, name := range []string{"web.run", "web__run"} {
			item := map[string]json.RawMessage{"name": mustMarshalJSON(name), "arguments": mustMarshalJSON(tc.arguments)}
			if got := subagentToolPreview(item, name, nil); got != toolActivityDetail(tc.want, tc.arguments) {
				t.Fatalf("native %s: %q", name, got)
			}
		}
	}
	source := `text(await tools.web__run({search_query:[{q:"release notes"}]}));`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	if got := subagentToolPreview(item, "functions.exec", nil); !strings.HasPrefix(got, "Search web\n") || !strings.Contains(got, "release notes") {
		t.Fatalf("Code Mode web.run = %q", got)
	}
}

func TestGenericPluginActivityPreview(t *testing.T) {
	arguments := `{"query":"docs"}`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("plugin__lookup"), "arguments": mustMarshalJSON(arguments)}
	want := toolActivityDetail("Tool call: "+commentaryCode("plugin__lookup"), arguments)
	if got := subagentToolPreview(item, "plugin__lookup", nil); got != want {
		t.Fatalf("native plugin preview = %q", got)
	}
	source := `text(await tools.plugin__lookup({query:"docs"}));`
	item = map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	if got := subagentToolPreview(item, "functions.exec", nil); got != want {
		t.Fatalf("Code Mode plugin preview = %q", got)
	}
}

func TestSubagentToolDisplayInvalidReaderOptions(t *testing.T) {
	for _, input := range []string{
		"mcat -n 0 a.go", "mcat -n 1 -n 2 a.go", "mcat -n 9007199254740992 a.go",
		"mcat --max-tokens 0 a.go", "mcat --max-tokens -1 a.go",
		"mcat --max-tokens 15501 a.go", "mcat --max-tokens 01 a.go",
		"mcat --max-tokens 1 --max-tokens 2 a.go", "mcat --max-tokens a.go", "mcat --tail a.go",
		"mcat a.go 2:1", "mcat a.go 1:0", "mcat a.go 01:2",
		"mcat a.go 1:9007199254740992",
		"msymbol refs a.go 2:abcd Name", "msymbol refs a.go 0 Name",
		"msymbol --max-tokens 0 refs a.go 2 Name", "msymbol --max-tokens 15501 refs a.go 2 Name",
		"msymbol --workspace refs a.go 2 Name", "msymbol refs a.go 2 Name 0",
		"inspect_file --source-bytes 100 a.go",
		"inspect_file --source Main --source Other a.go",
		"inspect_file --source Main --source-bytes 0 a.go",
		"inspect_file --source Main --source-bytes 8193 a.go",
		"inspect_file --source Main --source-bytes +1 a.go",
		"inspect_file --source Main --source-bytes 01 a.go",
		"inspect_file --source Main --source-bytes 1 --source-bytes 2 a.go",
		"inspect_file a.go --source Main",
		"inspect_file ''",
	} {
		t.Run(input, func(t *testing.T) {
			item := stockExecDisplayItem(input)
			want := "Run\n```bash\n" + input + "\n```"
			if got := subagentToolActivityText(item, "exec_command"); got != want {
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
		want := "Run\n```python\nimport pathlib, json\nprint('done')\n```"
		if got := subagentToolActivityText(item, "exec"); got != want {
			t.Fatalf("display = %q, want %q", got, want)
		}
	}
}

func TestSubagentInlineAwaitOutputProjection(t *testing.T) {
	for _, tc := range []struct{ script, want string }{
		{`rg -n 'function success|success\(' internal/router`, "Search `function success|success\\(` in `internal/router`"},
		{`mcat plugins/inspect_file.ts 125:162`, "Read `plugins/inspect_file.ts 125:162`"},
		{`echo hello`, "Run\n```bash\necho hello\n```"},
	} {
		source := "text((await tools.exec_command({cmd:" + string(mustMarshalJSON(tc.script)) + "})).output);"
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		if got := subagentToolActivityText(item, "exec"); got != tc.want {
			t.Fatalf("display %q = %q, want %q", source, got, tc.want)
		}
		if _, ok := toolActivityUnwrapExec(source, true); ok {
			t.Fatalf("output-only form claimed result metadata: %q", source)
		}
	}
	for _, source := range []string{
		`text((await tools.exec_command({cmd:"cat a"})).output + "other")`,
		`text((await tools.exec_command({cmd:"cat a"}))?.output)`,
		`text((await tools.exec_command({cmd:"cat a"}))["output"])`,
	} {
		if _, ok := toolActivityUnwrapExec(source, false); ok {
			t.Fatalf("nontransparent inline output unwrapped: %q", source)
		}
	}
}

func TestSubagentInterpreterWrapperProjection(t *testing.T) {
	for _, test := range []struct {
		name, source, language, program string
	}{
		{"python command", `python3 -I -c 'print("ok")'`, "python", `print("ok")`},
		{"pypy command", `pypy3 -c 'print("ok")'`, "python", `print("ok")`},
		{"python heredoc", "python3 - <<'PY'\nprint('ok')\nPY\n", "python", "print('ok')\n"},
		{"node command", `node --input-type=module -e 'console.log("ok")'`, "javascript", `console.log("ok")`},
		{"node heredoc", "node - <<'JS'\nconsole.log('ok')\nJS\n", "javascript", "console.log('ok')\n"},
		{"bun command", `bun -e 'console.log("ok")'`, "javascript", `console.log("ok")`},
		{"bun heredoc", "bun - <<'JS'\nconsole.log('ok')\nJS\n", "javascript", "console.log('ok')\n"},
		{"perl command", `perl -e 'print "ok"'`, "perl", `print "ok"`},
		{"perl heredoc", "perl - <<'PL'\n" + `print "ok";` + "\nPL\n", "perl", `print "ok";` + "\n"},
		{"ruby command", `ruby -e 'puts "ok"'`, "ruby", `puts "ok"`},
		{"php command", `php -r 'echo "ok";'`, "php", `echo "ok";`},
		{"shell combined flag", `sh -ec 'printf ok'`, "sh", `printf ok`},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := "Run\n" + toolActivityFenced(test.language, test.program)
			if got := toolActivityShell(test.source); got != want {
				t.Fatalf("display = %q; want %q", got, want)
			}
		})
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

func TestSubagentWriteStdinDisplay(t *testing.T) {
	for _, tt := range []struct {
		arguments string
		want      string
	}{
		{`{"session_id":52915}`, "Still Running"},
		{`{"session_id":52915,"chars":""}`, "Still Running"},
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
			"Read `/home/ubuntu/.codex/IMPLEMENTATION.md`\n\nRun `git diff --stat`\n\nRun `git diff -- internal/router/subagent_tool_display.go doc/spec/commentary.md`",
		},
		{
			`text(await tools.exec_command({cmd:"skills-mgr get golang-best-practices; sed -n '1,245p' internal/router/subagent_tool_display.go; sed -n '320,475p' internal/router/subagent_tool_display.go; git diff -- internal/router/subagent_tool_display_test.go",max_output_tokens:10100}));`,
			"Skill Read `golang-best-practices`\n\nRead `internal/router/subagent_tool_display.go 1:245`\n\nRead `internal/router/subagent_tool_display.go 320:475`\n\nRun `git diff -- internal/router/subagent_tool_display_test.go`",
		},
		{
			`text(await tools.exec_command({cmd:"gopls references internal/router/subagent_tool_display.go:17:6; sed -n '60,135p' doc/spec/commentary.md; sed -n '1,65p' internal/router/subagent_tool_display_test.go; sed -n '540,650p' internal/router/subagent_tool_display.go",max_output_tokens:5000}));`,
			"Run `gopls references internal/router/subagent_tool_display.go:17:6`\n\nRead `doc/spec/commentary.md 60:135`\n\nRead `internal/router/subagent_tool_display_test.go 1:65`\n\nRead `internal/router/subagent_tool_display.go 540:650`",
		},
		{
			`text(await tools.write_stdin({session_id:23221,chars:"",yield_time_ms:1000,max_output_tokens:5000}));`,
			"Still Running",
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
				item := stockExecDisplayItem(source)
				want := "Run\n```" + tt.language + "\n" + source + "```"
				if got := subagentToolActivityText(item, "exec_command"); got != want {
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
		{`{"type":"web_search_call","action":{"type":"find","pattern":"install"}}`, "Find in page\n`install`"},
		{`{"type":"web_search_call","action":{"type":"find_in_page","url":"https://example.com","pattern":"install"}}`, "Find in page\n`install in https://example.com`"},
		{`{"type":"tool_search_call","arguments":{"query":"OpenAI docs","limit":3}}`, "Search tools\n`{\"query\":\"OpenAI docs\",\"limit\":3}`"},
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
		{"sed -n '1,260p' source.go && echo done", "Read `source.go 1:260`\n\nRun `echo done`"},
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
		"nl -ba \"$file\" | sed -n '1,2p'",
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

func TestSubagentSearchReadRunGrouping(t *testing.T) {
	source := "rg -n 'func shellCatLiteral|func toolActivityGroup|toolActivityGroup\\(' internal/router\n" +
		"sed -n '238,265p' internal/router/subagent_tool_display.go\n" +
		"sed -n '140,170p' doc/spec/commentary.md\n" +
		"git status --short"
	item := stockExecDisplayItem(source)
	want := "In `/root/review_sed_display`\n\n" +
		"- Search `func shellCatLiteral|func toolActivityGroup|toolActivityGroup\\(` in `internal/router`\n\n" +
		"- Read `internal/router/subagent_tool_display.go 238:265` `doc/spec/commentary.md 140:170`\n\n" +
		"- Run `git status --short`"
	if got := toolActivityGroup("[`/root/review_sed_display`] ", subagentToolActivityText(item, "exec_command")); got != want {
		t.Fatalf("grouped display: got %q, want %q", got, want)
	}
}

func TestSubagentMixedReadRunFallbacks(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"cat a; git diff --check; git diff --stat;", "Read `a`\n\nRun `git diff --check`\n\nRun `git diff --stat`"},
		{"cat a\ncat b && echo done", "Read `a`\n\nRead `b`\n\nRun `echo done`"},
		{"cat a; printf '%s;' value;", "Read `a`\n\nRun `printf '%s;' value`"},
		{"cat a; sleep 1 &", "Read `a`\n\nRun `sleep 1 &`"},
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
	source := "cat /project/COLLABORATION.md\n" +
		"skills-mgr get deltapath-go-common-patterns\n" +
		"skills-mgr get golang-best-practices\n" +
		"sed -n '1,260p' source.go\n" +
		"sed -n '261,520p' 'source file.go'"
	item := stockExecDisplayItem(source)
	want := "In `/root/dect_evidence`\n\n" +
		"- Read `/project/COLLABORATION.md`\n\n" +
		"- Skill Read `deltapath-go-common-patterns` `golang-best-practices`\n\n" +
		"- Read `source.go 1:260` `source file.go 261:520`"
	got := toolActivityGroup("[`/root/dect_evidence`] ", subagentToolActivityText(item, "exec_command"))
	if got != want {
		t.Fatalf("grouped display: got %q, want %q", got, want)
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

func TestSubagentEditDisplayLabel(t *testing.T) {
	for _, name := range []string{"apply_patch"} {
		for _, input := range []string{
			"*** Begin Patch\n*** Update File: a\n@@\n-old\n+new\n*** End Patch\n",
			"incomplete edit",
			"",
		} {
			item := map[string]json.RawMessage{
				"name": mustMarshalJSON(name), "input": mustMarshalJSON(input),
			}
			if got := subagentToolActivityText(item, name); got != "" {
				t.Fatalf("%s generated edit commentary: %q", name, got)
			}
			item["arguments"] = mustMarshalJSON(`{"patch":` + string(mustMarshalJSON(input)) + `}`)
			delete(item, "input")
			if got := subagentToolActivityText(item, "functions."+name); got != "" {
				t.Fatalf("structured %s generated edit commentary: %q", name, got)
			}
		}
	}
}

func TestToolActivityNestedLanguageFencePreservesBlankLinesAndBackticks(t *testing.T) {
	display := "Run\n" + toolActivityFenced("bash", "+before\n+``` literal\n\n+`after`")
	nested := toolActivityNested(display)
	want := "- Run\n  ````bash\n  +before\n  +``` literal\n  \n  +`after`\n  ````"
	if nested != want {
		t.Fatalf("nested language fence = %q, want %q", nested, want)
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
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); results.forEach((result, i) => text(JSON.stringify({i, result})));`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]); r.forEach((v,i)=>text(JSON.stringify({i,...v})));`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]);for(let i=0;i<r.length;i++)text(JSON.stringify({i,...r[i]}))`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]); for (let n = 0; n < r.length; ++n) text(JSON.stringify({index:n, result:r[n]}));`,
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
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); results.forEach((result) => text(JSON.stringify({result, extra})));`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]); r.forEach((v,i)=>text(JSON.stringify({i,...extra})));`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]); r.forEach((v,i)=>text(JSON.stringify({i,...tools.exec_command({cmd:"echo hidden"})})));`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]);for(let i=0;i<=r.length;i++)text(JSON.stringify({i,...r[i]}))`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]);for(let i=0;i<r.length;i++)text(JSON.stringify({i,...r[i+1]}))`,
		`const r=await Promise.allSettled([tools.list_mcp_resources({}),tools.clock__curr_time({})]);for(let i=0;i<r.length;i++){tools.exec_command({cmd:"echo hidden"});text(JSON.stringify({i,...r[i]}))}`,
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); results.forEach((result) => { text(JSON.stringify({result})) });`,
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); results.forEach((text) => text(JSON.stringify({text})));`,
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); results.forEach((JSON) => text(JSON.stringify({result:JSON})));`,
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); results.forEach((\u0074ext) => text(JSON.stringify({result:\u0074ext})));`,
		`const results = await Promise.allSettled([tools.list_mcp_resources({}), tools.clock__curr_time({})]); results.forEach((result) => text(JSON.stringify({[tools.exec_command({cmd:"echo hidden"})]:result})));`,
		`await tools.update_plan({});`,
		`await tools.list_mcp_resources({}); await tools.update_plan({});`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		want := toolActivityJavaScript(source)
		if strings.HasPrefix(source, "const results = await Promise.allSettled") ||
			strings.HasPrefix(source, "const r=await Promise.allSettled") {
			want = "List MCP resources\n`{}`\n\nRead current time\n`{}`\n\nRun JavaScript · other code"
		}
		if got := subagentToolActivityText(item, "exec"); got != want {
			t.Fatalf("unsafe or excluded %s: %q", source, got)
		}
	}
	item := map[string]json.RawMessage{"name": mustMarshalJSON("update_plan"), "arguments": mustMarshalJSON(`{}`)}
	if got := subagentToolActivityText(item, "update_plan"); got != "Tool call: `update_plan`\n`{}`" {
		t.Fatal(got)
	}
}

func TestSubagentPromiseBatchResultLoopDisplaysCommands(t *testing.T) {
	source := `const r = await Promise.allSettled([
		tools.exec_command({cmd:"rg -n 'needle' internal/router"}),
		tools.exec_command({cmd:"mcat internal/router/subagent_tool_display.go 1:20"}),
	]); r.forEach((x, i) => text(JSON.stringify({i, result:x})));`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	want := "Search `needle` in `internal/router`\n\nRead `internal/router/subagent_tool_display.go 1:20`"
	if got := subagentToolActivityText(item, "exec"); got != want {
		t.Fatalf("batch command preview = %q, want %q", got, want)
	}
	if jsonString(item, "input") != source {
		t.Fatal("source changed")
	}
}

func TestSubagentPromiseBatchIndexedLoopsAndUnknownResultFormatting(t *testing.T) {
	source := `const r=await Promise.allSettled([tools.exec_command({cmd:"cat a.go"}),tools.exec_command({cmd:"cat b.go"})]);
for(let i=0;i<r.length;i++)text(JSON.stringify({i,...r[i]}));
const next=await Promise.allSettled([tools.exec_command({cmd:"cat c.go"})]);
for(let j=0;j<next.length;j++)text(JSON.stringify({j,...next[j]}));`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	want := "Read `a.go`\n\nRead `b.go`\n\nRead `c.go`"
	if got := subagentToolActivityText(item, "exec"); got != want {
		t.Fatalf("two indexed batches = %q, want %q", got, want)
	}
	source = `const r=await Promise.allSettled([tools.exec_command({cmd:"cat a.go"}),tools.exec_command({cmd:"cat b.go"})]); for(const row of r){text(row)}`
	item["input"] = mustMarshalJSON(source)
	want = "Read `a.go`\n\nRead `b.go`"
	if got := subagentToolActivityText(item, "exec"); got != want {
		t.Fatalf("unknown result formatter hid proven calls: %q", got)
	}
	for _, prefixOrSuffix := range []string{
		"// @exec: {\"max_output_tokens\":1000}\n",
		``,
	} {
		shadowed := prefixOrSuffix + `const r=await Promise.allSettled([tools.exec_command({cmd:"cat a.go"})]);for(var tools of []){}`
		item["input"] = mustMarshalJSON(shadowed)
		if got := subagentToolActivityText(item, "exec"); got != toolActivityJavaScript(shadowed) {
			t.Fatalf("hoisted tools binding was classified as a real call: %q", got)
		}
	}
	for _, suffix := range []string{`var \u0074ools;`, `function \u0074ools(){}`, `class \u0074ools{}`} {
		escaped := `const r=await Promise.allSettled([tools.exec_command({cmd:"cat a.go"})]);` + suffix
		item["input"] = mustMarshalJSON(escaped)
		if got := subagentToolActivityText(item, "exec"); got != toolActivityJavaScript(escaped) {
			t.Fatalf("escaped tools binding %q was classified as a real call: %q", suffix, got)
		}
	}
	source = `// @exec: {"max_output_tokens":1000}
const r=await Promise.allSettled([tools.exec_command({cmd:"cat a.go"})]);for(const row of r){text(row)}`
	item["input"] = mustMarshalJSON(source)
	want = "Read `a.go`"
	if got := subagentToolActivityText(item, "exec"); got != want {
		t.Fatalf("leading pragma hid proven batch: %q", got)
	}
	for _, suffix := range []string{
		`if(r[0].status === "fulfilled") text(r[0].value)`,
		`try { text(r[0]) } catch (error) { text(error) }`,
		`for(var i=0;i<r.length;i++) text(r[i])`,
	} {
		source = `const r=await Promise.allSettled([tools.exec_command({cmd:"cat a.go"})]);` + suffix
		item["input"] = mustMarshalJSON(source)
		expected := want
		if !strings.HasPrefix(suffix, "if(") {
			expected += "\n\nRun JavaScript · other code"
		}
		if got := subagentToolActivityText(item, "exec"); got != expected {
			t.Fatalf("result formatter %q hid proven batch: %q", suffix, got)
		}
	}
}

func TestSubagentBatchSuppressesOnlyPatchCommentary(t *testing.T) {
	source := `text(await tools.apply_patch("*** Begin Patch\n*** Add File: a\n+x\n*** Add File: b\n+y\n*** End Patch\n")); text(await tools.clock__curr_time({}));`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	display := subagentToolPreview(item, "exec", nil)
	if display != "Read current time\n`{}`" {
		t.Fatalf("mixed batch preview = %q", display)
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

func TestJournalToolActivityIsNotRepeated(t *testing.T) {
	for _, source := range []string{
		`const r = await tools.journal({op:"add", text:"Confirmed cold artifact", report_now:true}); text(r)`,
		`await journal({op:"add", text:"Progress", report_now:true})`,
		`text(await tools.journal({op:"edit", id:"amber", text:"Updated"}))`,
		`await journal([{op:"add", text:"One"}, {op:"add", text:"Two"}])`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		if got := subagentToolActivityText(item, "exec"); got != "" {
			t.Errorf("journal preview = %q for %s", got, source)
		}
	}
	source := `await journal({op:"add", text:"Progress"}); text(await tools.exec_command({cmd:"cat a.go"}))`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
	if got := subagentToolActivityText(item, "exec"); got != "Read `a.go`" {
		t.Errorf("mixed preview = %q", got)
	}
	for _, source := range []string{
		`await tools.journal({text: sideEffect()})`,
		`const journal = await tools.exec_command({cmd:"cat a"}); text(journal); await journal({text:"x"})`,
		`await other.journal({text:"x"})`,
		`await journal({text:"x"}); sideEffect()`,
	} {
		item["input"] = mustMarshalJSON(source)
		if got := subagentToolActivityText(item, "exec"); !strings.HasPrefix(got, "Run JavaScript") {
			t.Errorf("opaque code was suppressed: %q", got)
		}
	}
}

func TestSubagentMappedCommandArray(t *testing.T) {
	prefix := `const cmds=["curl -fsSL https://example.test/a | nl -ba | sed -n '360,540p'", "cat a.go"];`
	batch := `const r=await Promise.allSettled(cmds.map(cmd=>tools.exec_command({cmd,max_output_tokens:4500})));`
	format := "for(let i=0;i<r.length;i++){const v=r[i];text(`${i}:\\n${v.status===\"fulfilled\"?v.value.output:v.reason}`)}"
	for _, call := range []string{batch, strings.Replace(batch, "cmd=>", "(cmd)=>", 1), strings.Replace(batch, "{cmd,", "{cmd:cmd,", 1)} {
		source := prefix + call + format
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: subagentToolActivityText(item, "exec")})
		if len(blocks) != 2 || blocks[0].verb != "Run" || !strings.Contains(blocks[0].code, "curl -fsSL") || blocks[1].kind != "reads" || blocks[1].reads[0].path != "a.go" {
			t.Fatalf("mapped commands not displayed: %+v", blocks)
		}
		if jsonString(item, "input") != source {
			t.Fatal("source changed")
		}
		if _, ok := toolActivityUnwrapExec(source, true); ok {
			t.Fatal("map became result evidence")
		}
	}
	for _, source := range []string{
		strings.Replace(prefix, "const cmds", "let cmds", 1) + batch,
		strings.Replace(prefix, `"cat a.go"`, "dynamic", 1) + batch,
		prefix + strings.Replace(batch, "cmd=>", "tools=>", 1),
		prefix + strings.Replace(batch, "{cmd,", "{cmd: transform(cmd),", 1),
		prefix + strings.Replace(batch, "{cmd,", "{cmd,workdir:dynamic,", 1),
		prefix + strings.Replace(batch, "cmd=>", "async cmd=>", 1),
		prefix + "cmds.push('echo hidden');" + batch,
		prefix + batch + "var tools;",
		prefix + batch + "const Promise = other;",
		prefix + strings.Replace(batch, "{cmd,", "{cmd,...extra,", 1),
		`const text=["cat a.go"]; const r=await Promise.all(text.map(cmd=>tools.exec_command({cmd})));text(r);`,
		`const JSON=["cat a.go"]; const r=await Promise.all(JSON.map(cmd=>tools.exec_command({cmd})));text(JSON.stringify(r));`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		if got := subagentToolActivityText(item, "exec"); got != toolActivityJavaScript(source) {
			t.Fatalf("unsafe map classified: %s: %s", source, got)
		}
	}
}

func TestSubagentBatchFormattingIsNotOtherCode(t *testing.T) {
	prefix := `const results = await Promise.allSettled([tools.exec_command({cmd:"cat a.go"}),tools.exec_command({cmd:"cat b.go"})]);`
	for _, format := range []string{
		"for(let i=0;i<results.length;i++){const v=results[i];text(`${i}:\\n${v.status===\"fulfilled\"?v.value.output:v.reason}`)}",
		`for (const row of results) { text(row) }`,
		`if(results[0].status === "fulfilled") text(results[0].value)`,
		"const labels=[\"source\",\"tests\"]; for(let i=0;i<results.length;i++){const result=results[i];if(result.status===\"rejected\"){text(`${labels[i]}: ${result.reason}`);continue;}const value=result.value;text(`${labels[i]}: ${value.session_id ? `running session_id=${value.session_id}` : `exit_code=${value.exit_code}`}\\n${value.output}`);}",
	} {
		source := prefix + format
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		if got := subagentToolActivityText(item, "exec"); got != "Read `a.go`\n\nRead `b.go`" {
			t.Fatalf("formatter misclassified: %s: %s", format, got)
		}
	}
	for _, format := range []string{
		`for(let i=0;i<results.length;i++){tools.exec_command({cmd:"echo hidden"});text(results[i])}`,
		`for(let i=0;i<results.length;i++){text(external);}`,
		`for(let i=0;i<results.length;i++){results[i] = changed;text(results[i]);}`,
		`for(let i=0;i<results.length;i++){const text=results[i];text(text);}`,
		`for(let i=0;i<results.length;i++){text(JSON.stringify({[tools.exec_command({cmd:"hidden"})]:results[i]}));}`,
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(prefix + format)}
		if got := subagentToolActivityText(item, "exec"); !strings.HasSuffix(got, "Run JavaScript · other code") {
			t.Fatalf("unrecognized work hidden: %s: %s", format, got)
		}
	}
}

func TestSubagentMappedPreviewDoesNotAmplifySharedOptions(t *testing.T) {
	source := `const cmds=["cat a.go","cat b.go"];const results=await Promise.all(cmds.map(cmd=>tools.exec_command({cmd,justification:"` + strings.Repeat("x", 10000) + `"})));`
	calls, other, ok := toolActivityBatchProducerCalls(source)
	if !ok || other || len(calls) != 2 {
		t.Fatalf("mapped calls unavailable: %v %v %d", ok, other, len(calls))
	}
	for _, call := range calls {
		if len(call["arguments"]) > 100 {
			t.Fatal("shared options amplified in preview")
		}
	}
}
