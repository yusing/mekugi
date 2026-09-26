package router

import (
	json "encoding/json/v2"
	"strconv"
	"strings"
)

// previewBoundaryCase describes cumulative tool-input deltas, never executable fixture edits.
type previewBoundaryCase struct {
	Name, Path, Kind string
	Steps            []previewBoundaryStep
	Final            string
}

type previewBoundaryStep struct {
	Label, Delta string
	Want, Absent []string
}

func previewBoundaryCases() []previewBoundaryCase {
	cases := []previewBoundaryCase{
		{
			Name: "Markdown: newline releases a line",
			Path: "boundary-notes.md",
			Kind: applyPatchToolName,
			Steps: []previewBoundaryStep{
				{Label: "HEADER ONLY: previous case remains visible", Delta: "*** Begin Patch\n*** Add File: boundary-notes.md\n"},
				{Label: "BASELINE: first complete source appears", Delta: "+# Boundary notes\n", Want: []string{"# Boundary notes"}, Absent: []string{"A partial paragraph"}},
				{Label: "HOLD: incomplete source must stay hidden", Delta: "+A partial paragraph", Want: []string{"# Boundary notes"}, Absent: []string{"A partial paragraph"}},
				{Label: "HOLD: more text is not a source boundary", Delta: " waits for its newline", Want: []string{"# Boundary notes"}, Absent: []string{"A partial paragraph"}},
				{Label: "RELEASE: completed source appears before call completion", Delta: "\n", Want: []string{"# Boundary notes", "A partial paragraph"}},
			},
			Final: "*** Begin Patch\n*** Add File: boundary-notes.md\n+# Boundary notes\n+A partial paragraph waits for its newline\n*** End Patch\n",
		},
		{
			Name: "Go: call closes before reveal",
			Path: "boundary.go",
			Kind: applyPatchToolName,
			Steps: []previewBoundaryStep{
				{Label: "HEADER ONLY: previous case remains visible", Delta: "*** Begin Patch\n*** Add File: boundary.go\n"},
				{Label: "BASELINE: first complete source appears", Delta: "+package demo\n", Want: []string{"package demo"}, Absent: []string{"var boundaryGo"}},
				{Label: "HOLD: incomplete source must stay hidden", Delta: "+var boundaryGo = append(\n", Want: []string{"package demo"}, Absent: []string{"var boundaryGo"}},
				{Label: "HOLD: more text is not a source boundary", Delta: "+  []string{\"ready; still quoted\"},\n", Want: []string{"package demo"}, Absent: []string{"var boundaryGo"}},
				{Label: "RELEASE: completed source appears before call completion", Delta: "+  \"done\",\n+)\n", Want: []string{"package demo", "var boundaryGo"}},
			},
			Final: "*** Begin Patch\n*** Add File: boundary.go\n+package demo\n+var boundaryGo = append(\n+  []string{\"ready; still quoted\"},\n+  \"done\",\n+)\n*** End Patch\n",
		},
		{
			Name: "Python: triple-quoted text is one statement",
			Path: "boundary.py",
			Kind: applyPatchToolName,
			Steps: []previewBoundaryStep{
				{Label: "HEADER ONLY: previous case remains visible", Delta: "*** Begin Patch\n*** Add File: boundary.py\n"},
				{Label: "BASELINE: first complete source appears", Delta: "+ready_python = True\n", Want: []string{"ready_python = True"}, Absent: []string{"boundary_python ="}},
				{Label: "HOLD: incomplete source must stay hidden", Delta: "+boundary_python = \"\"\"first; not a boundary\n", Want: []string{"ready_python = True"}, Absent: []string{"boundary_python ="}},
				{Label: "HOLD: more text is not a source boundary", Delta: "+second quoted line; still inside\n", Want: []string{"ready_python = True"}, Absent: []string{"boundary_python ="}},
				{Label: "RELEASE: completed source appears before call completion", Delta: "+last quoted line\"\"\"\n", Want: []string{"ready_python = True", "boundary_python ="}},
			},
			Final: "*** Begin Patch\n*** Add File: boundary.py\n+ready_python = True\n+boundary_python = \"\"\"first; not a boundary\n+second quoted line; still inside\n+last quoted line\"\"\"\n*** End Patch\n",
		},
		{
			Name: "JavaScript: template newlines are not boundaries",
			Path: "boundary.js",
			Kind: applyPatchToolName,
			Steps: []previewBoundaryStep{
				{Label: "HEADER ONLY: previous case remains visible", Delta: "*** Begin Patch\n*** Add File: boundary.js\n"},
				{Label: "BASELINE: first complete source appears", Delta: "+const readyJS = true;\n", Want: []string{"const readyJS = true"}, Absent: []string{"const boundaryJS"}},
				{Label: "HOLD: incomplete source must stay hidden", Delta: "+const boundaryJS = `first; not a boundary\n", Want: []string{"const readyJS = true"}, Absent: []string{"const boundaryJS"}},
				{Label: "HOLD: more text is not a source boundary", Delta: "+second quoted line; still inside\n", Want: []string{"const readyJS = true"}, Absent: []string{"const boundaryJS"}},
				{Label: "RELEASE: completed source appears before call completion", Delta: "+last quoted line`;\n", Want: []string{"const readyJS = true", "const boundaryJS"}},
			},
			Final: "*** Begin Patch\n*** Add File: boundary.js\n+const readyJS = true;\n+const boundaryJS = `first; not a boundary\n+second quoted line; still inside\n+last quoted line`;\n*** End Patch\n",
		},
		{
			Name: "TypeScript: Code Mode patch holds an open array",
			Path: "boundary.ts",
			Kind: "exec",
			Steps: []previewBoundaryStep{
				{Label: "HEADER ONLY: previous case remains visible", Delta: "await tools.apply_patch(\"*** Begin Patch\\n*** Add File: boundary.ts\\n"},
				{Label: "BASELINE: first complete source appears", Delta: "+const readyTS = true;\\n", Want: []string{"const readyTS = true"}, Absent: []string{"const boundaryTS"}},
				{Label: "HOLD: incomplete source must stay hidden", Delta: "+const boundaryTS: string[] = [\\n", Want: []string{"const readyTS = true"}, Absent: []string{"const boundaryTS"}},
				{Label: "HOLD: more text is not a source boundary", Delta: "+  \\\"quoted; separator\\\",\\n", Want: []string{"const readyTS = true"}, Absent: []string{"const boundaryTS"}},
				{Label: "RELEASE: completed source appears before call completion", Delta: "+  \\\"done\\\",\\n+];\\n", Want: []string{"const readyTS = true", "const boundaryTS"}},
			},
			Final: "await tools.apply_patch(\"*** Begin Patch\\n*** Add File: boundary.ts\\n+const readyTS = true;\\n+const boundaryTS: string[] = [\\n+  \\\"quoted; separator\\\",\\n+  \\\"done\\\",\\n+];\\n*** End Patch\\n\");",
		},
		{
			Name: "Shell: native cat preserves quoted newlines",
			Path: "boundary.sh",
			Kind: nativeExecCommandToolName,
			Steps: []previewBoundaryStep{
				{Label: "HEADER ONLY: previous case remains visible", Delta: "{\"cmd\":\"cat > boundary.sh <<'PREVIEW_SH'\\n"},
				{Label: "BASELINE: first complete source appears", Delta: "ready_shell=yes\\n", Want: []string{"ready_shell=yes"}, Absent: []string{"boundary_shell="}},
				{Label: "HOLD: incomplete source must stay hidden", Delta: "boundary_shell='first; not a boundary\\n", Want: []string{"ready_shell=yes"}, Absent: []string{"boundary_shell="}},
				{Label: "HOLD: more text is not a source boundary", Delta: "second quoted line; still inside\\n", Want: []string{"ready_shell=yes"}, Absent: []string{"boundary_shell="}},
				{Label: "RELEASE: completed source appears before call completion", Delta: "last quoted line'\\n", Want: []string{"ready_shell=yes", "boundary_shell="}},
			},
			Final: "{\"cmd\":\"cat > boundary.sh <<'PREVIEW_SH'\\nready_shell=yes\\nboundary_shell='first; not a boundary\\nsecond quoted line; still inside\\nlast quoted line'\\nPREVIEW_SH\\n\"}",
		},
		{
			Name: "Markdown burst: one complete line per frame",
			Path: "boundary-burst.md",
			Kind: applyPatchToolName,
			Steps: []previewBoundaryStep{
				{Label: "HEADER ONLY: previous case remains visible", Delta: "*** Begin Patch\n*** Add File: boundary-burst.md\n"},
				{Label: "BURST: watch twelve lines reveal progressively", Delta: "+burst line 01 complete\n+burst line 02 complete\n+burst line 03 complete\n+burst line 04 complete\n+burst line 05 complete\n+burst line 06 complete\n+burst line 07 complete\n+burst line 08 complete\n+burst line 09 complete\n+burst line 10 complete\n+burst line 11 complete\n+burst line 12 complete\n", Want: []string{"burst line 12 complete"}, Absent: []string{"pending tail"}},
				{Label: "HOLD: partial tail must stay hidden", Delta: "+pending tail", Want: []string{"burst line 12 complete"}, Absent: []string{"pending tail"}},
				{Label: "RELEASE: tail newline appears before completion", Delta: "\n", Want: []string{"burst line 12 complete", "pending tail"}},
			},
			Final: "*** Begin Patch\n*** Add File: boundary-burst.md\n+burst line 01 complete\n+burst line 02 complete\n+burst line 03 complete\n+burst line 04 complete\n+burst line 05 complete\n+burst line 06 complete\n+burst line 07 complete\n+burst line 08 complete\n+burst line 09 complete\n+burst line 10 complete\n+burst line 11 complete\n+burst line 12 complete\n+pending tail\n*** End Patch\n",
		},
	}
	// Continue each language beyond its initial quoted-delimiter example, so
	// testers can distinguish several independent releases from one final fade.
	extensions := []struct {
		index int
		steps []previewBoundaryStep
	}{
		{1, []previewBoundaryStep{
			{Label: "RELEASE: separate declaration", Delta: "+var goCount = len(boundaryGo)\n", Want: []string{"var goCount"}},
			{Label: "HOLD: raw-string newline is not a boundary", Delta: "+var goMessage = `raw; && || { text\n+second raw line; still quoted\n", Want: []string{"var goCount"}, Absent: []string{"var goMessage"}},
			{Label: "RELEASE: raw string closes", Delta: "+last raw line`\n", Want: []string{"var goMessage"}},
			{Label: "HOLD: composite literal remains open", Delta: "+var goOptions = map[string]string{\n+  \"mode\": \"fast; safe\",\n", Want: []string{"var goMessage"}, Absent: []string{"var goOptions"}},
			{Label: "RELEASE: complete map and following declaration", Delta: "+}\n+var goReady = goCount > 0\n", Want: []string{"var goOptions", "var goReady"}},
			{Label: "RELEASE: two Go statements separated by semicolon", Delta: "+var goLeft = 1; var goRight = 2\n", Want: []string{"goLeft", "goRight"}},
			{Label: "HOLD: Go && lacks right operand", Delta: "+var goChoice = goReady &&\n", Want: []string{"goRight"}, Absent: []string{"var goChoice"}},
			{Label: "HOLD: Go || lacks right operand", Delta: "+  (goLeft > 0 ||\n", Absent: []string{"var goChoice"}},
			{Label: "RELEASE: && and || operands complete", Delta: "+   goRight > 0)\n", Want: []string{"var goChoice", "||"}},
			{Label: "HOLD: Go opening brace has no complete inner statement", Delta: "+func goRun() {\n", Want: []string{"var goChoice"}, Absent: []string{"func goRun"}},
			{Label: "RELEASE: complete inner statement before closing brace", Delta: "+  goCount++\n", Want: []string{"func goRun", "goCount++"}},
			{Label: "RELEASE: Go block closes", Delta: "+}\n", Want: []string{"func goRun", "goCount++"}},
		}},
		{2, []previewBoundaryStep{
			{Label: "RELEASE: independent quoted statement", Delta: "+python_mode = 'fast; safe'\n", Want: []string{"python_mode ="}},
			{Label: "HOLD: multiline call remains open", Delta: "+python_items = sorted(\n+    ['z; quoted',\n+     'a'],\n", Want: []string{"python_mode ="}, Absent: []string{"python_items ="}},
			{Label: "RELEASE: call closes", Delta: "+)\n", Want: []string{"python_items ="}},
			{Label: "HOLD: dictionary remains open", Delta: "+python_options = {\n+    'message': 'semi; colon',\n", Want: []string{"python_items ="}, Absent: []string{"python_options ="}},
			{Label: "RELEASE: dictionary and trailing statement", Delta: "+}\n+python_count = len(python_items)\n", Want: []string{"python_options =", "python_count ="}},
			{Label: "RELEASE: two Python statements separated by semicolon", Delta: "+python_left = 1; python_right = 2\n", Want: []string{"python_left", "python_right"}},
			{Label: "HOLD: parenthesized and/or lacks final operand", Delta: "+python_choice = (python_left and\n+                 python_right or\n", Want: []string{"python_right ="}, Absent: []string{"python_choice ="}},
			{Label: "RELEASE: parenthesized and/or completes", Delta: "+                 python_count)\n", Want: []string{"python_choice =", " or"}},
		}},
		{3, []previewBoundaryStep{
			{Label: "RELEASE: separate quoted statement", Delta: "+const jsMode = 'fast; safe';\n", Want: []string{"const jsMode"}},
			{Label: "HOLD: nested object remains open", Delta: "+const jsOptions = {\n+  label: 'semi; colon',\n+  values: [1, 2, 3],\n", Want: []string{"const jsMode"}, Absent: []string{"const jsOptions"}},
			{Label: "RELEASE: complete object", Delta: "+};\n", Want: []string{"const jsOptions"}},
			{Label: "HOLD: multiline call remains open", Delta: "+const jsResult = Math.max(\n+  ...jsOptions.values,\n", Want: []string{"const jsOptions"}, Absent: []string{"const jsResult"}},
			{Label: "RELEASE: call and trailing statement", Delta: "+);\n+const jsReady = jsResult > 0;\n", Want: []string{"const jsResult", "const jsReady"}},
			{Label: "RELEASE: two JS statements separated by semicolon", Delta: "+let jsLeft = 1; let jsRight = 2;\n", Want: []string{"jsLeft", "jsRight"}},
			{Label: "HOLD: JS && lacks right operand", Delta: "+const jsChoice = jsReady &&\n", Want: []string{"jsRight"}, Absent: []string{"const jsChoice"}},
			{Label: "HOLD: JS || lacks right operand", Delta: "+  (jsLeft > 0 ||\n", Absent: []string{"const jsChoice"}},
			{Label: "RELEASE: JS && and || complete", Delta: "+   jsRight > 0);\n", Want: []string{"const jsChoice", "||"}},
			{Label: "HOLD: JS opening brace has no complete inner statement", Delta: "+if (jsChoice) {\n", Want: []string{"const jsChoice"}, Absent: []string{"if (jsChoice)"}},
			{Label: "RELEASE: complete inner statement before closing brace", Delta: "+  jsLeft += 1;\n", Want: []string{"if (jsChoice)", "jsLeft += 1"}},
			{Label: "RELEASE: JS block closes", Delta: "+}\n", Want: []string{"if (jsChoice)", "jsLeft += 1"}},
		}},
		{4, []previewBoundaryStep{
			{Label: "RELEASE: typed independent statement", Delta: "+const tsMode: string = 'fast; safe';\n", Want: []string{"const tsMode"}},
			{Label: "HOLD: template literal contains newlines", Delta: "+const tsMessage: string = `first; && || { quoted\n+second; still quoted\n", Want: []string{"const tsMode"}, Absent: []string{"const tsMessage"}},
			{Label: "RELEASE: template literal closes", Delta: "+last line`;\n", Want: []string{"const tsMessage"}},
			{Label: "HOLD: typed object remains open", Delta: "+const tsOptions: Record<string, string> = {\n+  mode: 'fast; safe',\n", Want: []string{"const tsMessage"}, Absent: []string{"const tsOptions"}},
			{Label: "RELEASE: object and trailing statement", Delta: "+};\n+const tsCount: number = boundaryTS.length;\n", Want: []string{"const tsOptions", "const tsCount"}},
			{Label: "RELEASE: two TS statements separated by semicolon", Delta: "+let tsLeft = 1; let tsRight = 2;\n", Want: []string{"tsLeft", "tsRight"}},
			{Label: "HOLD: TS && lacks right operand", Delta: "+const tsChoice: boolean = tsCount > 0 &&\n", Want: []string{"tsRight"}, Absent: []string{"const tsChoice"}},
			{Label: "HOLD: TS || lacks right operand", Delta: "+  (tsLeft > 0 ||\n", Absent: []string{"const tsChoice"}},
			{Label: "RELEASE: TS && and || complete", Delta: "+   tsRight > 0);\n", Want: []string{"const tsChoice", "||"}},
			{Label: "HOLD: TS opening brace has no complete inner statement", Delta: "+if (tsChoice) {\n", Want: []string{"const tsChoice"}, Absent: []string{"if (tsChoice)"}},
			{Label: "RELEASE: complete inner statement before closing brace", Delta: "+  tsLeft += 1;\n", Want: []string{"if (tsChoice)", "tsLeft += 1"}},
			{Label: "RELEASE: TS block closes", Delta: "+}\n", Want: []string{"if (tsChoice)", "tsLeft += 1"}},
		}},
		{5, []previewBoundaryStep{
			{Label: "RELEASE: separate shell assignment", Delta: "shell_mode='fast; safe'\n", Want: []string{"shell_mode="}},
			{Label: "HOLD: double quote spans lines", Delta: "shell_message=\"first; && || { quoted\nsecond; still quoted\n", Want: []string{"shell_mode="}, Absent: []string{"shell_message="}},
			{Label: "RELEASE: double quote closes", Delta: "last line\"\n", Want: []string{"shell_message="}},
			{Label: "HOLD: another single quote spans lines", Delta: "shell_details='alpha; quoted\nbeta; still quoted\n", Want: []string{"shell_message="}, Absent: []string{"shell_details="}},
			{Label: "RELEASE: quote and trailing command", Delta: "gamma'\nprintf '%s\\n' \"$shell_mode\"\n", Want: []string{"shell_details=", "printf"}},
			{Label: "RELEASE: two shell commands separated by semicolon", Delta: "shell_left=1; shell_right=2\n", Want: []string{"shell_left=", "shell_right="}},
			{Label: "HOLD: shell && lacks right command", Delta: "test \"$shell_left\" -eq 1 &&\n", Want: []string{"shell_right="}, Absent: []string{"test \"$shell_left\""}},
			{Label: "RELEASE: shell && and || command completes", Delta: "  printf 'yes && no || maybe; { quoted }' || printf fallback\n", Want: []string{"test \"$shell_left\"", "|| printf fallback"}},
			{Label: "HOLD: shell opening brace has no complete inner statement", Delta: "{\n", Want: []string{"|| printf fallback"}, Absent: []string{"shell_left=3"}},
			{Label: "RELEASE: complete inner statement before closing brace", Delta: "  shell_left=3; shell_right=4\n", Want: []string{"shell_left=3", "shell_right=4"}},
			{Label: "RELEASE: shell brace group closes", Delta: "}\n", Want: []string{"shell_left=3", "shell_right=4"}},
		}},
	}
	for _, extension := range extensions {
		fixture := &cases[extension.index]
		var prefix strings.Builder
		for _, step := range fixture.Steps {
			prefix.WriteString(step.Delta)
		}
		suffix := strings.TrimPrefix(fixture.Final, prefix.String())
		for _, step := range extension.steps {
			if fixture.Kind != applyPatchToolName {
				quoted := strconv.Quote(step.Delta)
				step.Delta = quoted[1 : len(quoted)-1]
			}
			fixture.Steps = append(fixture.Steps, step)
			prefix.WriteString(step.Delta)
		}
		fixture.Final = prefix.String() + suffix
	}
	return append(cases, previewPythonReplacementCase())
}

func previewPythonReplacementCase() previewBoundaryCase {
	_, replacement, command := previewPythonEditCommand()
	command = strings.ReplaceAll(command, "internal/pane/launch.go", "boundary-python-target.go")
	literal := strconv.Quote(replacement)
	start := strings.Index(command, literal)
	firstEnd := start + strings.Index(literal, `\n`) + 2
	secondEnd := start + strings.Index(literal, `\n\treturn frame`) + 2
	encodePrefix := func(command string) string {
		encoded, err := json.Marshal(&struct {
			Cmd string `json:"cmd"`
		}{Cmd: command})
		if err != nil {
			panic(err)
		}
		return strings.TrimSuffix(string(encoded), `"}`)
	}
	fixture := previewBoundaryCase{
		Name:  "Python replacement: Go statements stream inside an open literal",
		Path:  "boundary-python-target.go",
		Kind:  nativeExecCommandToolName,
		Final: encodePrefix(command) + `"}`,
	}
	previous := ""
	for _, checkpoint := range []struct {
		end          int
		label        string
		want, absent []string
	}{
		{start, "HEADER ONLY: before replacement text, preceding diff stays visible", nil, nil},
		{firstEnd, "RELEASE: first Go statement before Python literal closes", []string{"frame := <-frames"}, []string{"open: "}},
		{secondEnd, "RELEASE: complete Go block while Python literal is still open", []string{"frame := <-frames", "open: "}, nil},
		{len(command), "COMPLETE SCRIPT: final target return and Python write call", []string{"frame := <-frames", "return frame"}, nil},
	} {
		prefix := encodePrefix(command[:checkpoint.end])
		fixture.Steps = append(fixture.Steps, previewBoundaryStep{Label: checkpoint.label, Delta: strings.TrimPrefix(prefix, previous), Want: checkpoint.want, Absent: checkpoint.absent})
		previous = prefix
	}
	return fixture
}
