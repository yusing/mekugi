package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type shellCatStep struct {
	command string
	patch   string
	guard   string
}

// splitShellCatWrites accepts only independent simple statements. In particular,
// splitting a cd, assignment, option change, function, or expansion across host
// executions would lose shell state, even when its separator is just a semicolon.
func splitShellCatWrites(body, directory string, variant syntax.LangVariant) ([]shellCatStep, bool) {
	// The shell parser normalizes CRLF; a projection must not silently change
	// heredoc bytes that cat would otherwise receive.
	if strings.ContainsAny(body, "\r\x00") {
		return nil, false
	}
	program, err := syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(body), "")
	if err != nil || !filepath.IsAbs(directory) {
		return nil, false
	}
	steps := make([]shellCatStep, 0, len(program.Stmts))
	writes := false
	for _, statement := range program.Stmts {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 || len(call.Assigns) != 0 || statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
			return nil, false
		}
		static := true
		syntax.Walk(statement, func(node syntax.Node) bool {
			switch node.(type) {
			case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp, *syntax.ExtGlob, *syntax.BraceExp:
				static = false
				return false
			}
			return true
		})
		name, literal := shellCatLiteral(call.Args[0])
		if !static || !literal || name == commentaryArgumentName {
			return nil, false
		}
		if interp.IsBuiltin(name) {
			switch name {
			case "echo", "printf", "true", "false", "pwd", "test", "[":
			default:
				return nil, false
			}
		}
		var command bytes.Buffer
		if err := syntax.NewPrinter().Print(&command, statement); err != nil {
			return nil, false
		}
		step := shellCatStep{command: command.String()}
		if name == "cat" && len(call.Args) == 1 && len(statement.Redirs) == 2 {
			var input, output *syntax.Redirect
			for _, redirect := range statement.Redirs {
				switch {
				case (redirect.Op == syntax.Hdoc || redirect.Op == syntax.DashHdoc) && (redirect.N == nil || redirect.N.Value == "0"):
					input = redirect
				case redirect.Op == syntax.RdrOut && (redirect.N == nil || redirect.N.Value == "1"):
					output = redirect
				}
			}
			if input != nil && output != nil {
				path, literal := shellCatLiteral(output.Word)
				// A quoted delimiter makes the body literal, including dollar signs
				// and backslashes. Unquoted documents retain shell expansion semantics.
				quoted := false
				for _, part := range input.Word.Parts {
					switch part.(type) {
					case *syntax.SglQuoted, *syntax.DblQuoted:
						quoted = true
					}
				}
				var content strings.Builder
				var parts []syntax.WordPart
				if input.Hdoc != nil {
					parts = input.Hdoc.Parts
				}
				for _, part := range parts {
					lit, ok := part.(*syntax.Lit)
					if !ok {
						literal = false
						break
					}
					content.WriteString(lit.Value)
				}
				if literal && quoted && path != "" && path != "." && !strings.HasSuffix(path, "/") && !strings.HasSuffix(path, "/.") {
					// Lexically cleaning symlink/.. would select a different target
					// from shell pathname traversal.
					for component := range strings.SplitSeq(filepath.ToSlash(path), "/") {
						if component == ".." {
							return nil, false
						}
					}
					if !filepath.IsAbs(path) {
						path = filepath.Join(directory, path)
					}
					text := content.String()
					if input.Op == syntax.DashHdoc {
						lines := strings.Split(text, "\n")
						for index := range lines {
							lines[index] = strings.TrimLeft(lines[index], "\t")
						}
						text = strings.Join(lines, "\n")
					}
					if patch, err := mekugi.RenderFileWritePatch(path, text); err == nil {
						step.patch = patch
						// Add File can create missing parents, unlike cat redirection.
						// Check at execution time, after preceding commands, and leave
						// symlinks/special files and redirection failures to the shell.
						quotedPath := shellQuoteArgument(path)
						step.guard = "test -d " + shellQuoteArgument(filepath.Dir(path)) +
							" && test ! -L " + quotedPath + " && { test ! -e " + quotedPath + " || test -f " + quotedPath + "; }"
						writes = true
					}
				}
			}
		}
		steps = append(steps, step)
	}
	return steps, writes
}

func shellCatLiteral(word *syntax.Word) (string, bool) {
	// Validate before expansion: process substitutions require an execution
	// callback, and presentation/planning must never invoke one.
	if word == nil || !shellCatLiteralParts(word.Parts, false) {
		return "", false
	}
	value, err := expand.Literal(nil, word)
	return value, err == nil
}

func shellCatLiteralParts(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch value := part.(type) {
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			if !shellCatLiteralParts(value.Parts, true) {
				return false
			}
		case *syntax.Lit:
			if !quoted && strings.ContainsAny(value.Value, "~*?[") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (t *mekugiResponseTransform) shellCatPlan(contribution toolContribution, arguments []string, template string, params map[string]json.RawMessage, callIDs ...string) ([]shellCatStep, []string, bool) {
	if contribution.PluginID != builtinToolsPluginID || contribution.Name != "shell" || template != "" || len(arguments) != 2 {
		return nil, nil, false
	}
	variant := syntax.LangBash
	switch shellInterpreterName(arguments[0]) {
	case "bash":
	case "sh":
		variant = syntax.LangPOSIX
	default:
		return nil, nil, false
	}
	for key, value := range params {
		switch key {
		case "workdir", "max_output_tokens", "yield_time_ms":
		case "login", "tty":
			if string(value) != "false" {
				return nil, nil, false
			}
		default:
			// Execution-scoped permissions and environments cannot be silently
			// transferred to a different native tool.
			return nil, nil, false
		}
	}
	directory := t.directory
	if workdir, exists := params["workdir"]; exists {
		if json.Unmarshal(workdir, &directory) != nil {
			return nil, nil, false
		}
	}
	steps, ok := splitShellCatWrites(arguments[1], directory, variant)
	if !ok {
		return nil, nil, false
	}
	commands := make([]string, len(steps))
	for index, step := range steps {
		command, err := t.proxy.registry.execCarrierCommand(contribution, step.command, []string{arguments[0], step.command}, "", callIDs...)
		if err != nil {
			return nil, nil, false
		}
		commands[index] = command
	}
	return steps, commands, true
}

func (t *mekugiResponseTransform) shellCatCarrier(contribution toolContribution, kind codeModeCarrierKind, arguments []string, template string, params, metadata map[string]json.RawMessage, callIDs ...string) (string, bool) {
	steps, commands, ok := t.shellCatPlan(contribution, arguments, template, params, callIDs...)
	if !ok {
		return "", false
	}
	if kind == codeModeCarrierFunction {
		for index, step := range steps {
			if step.patch != "" {
				commands[index] = "if " + step.guard + "; then\n" + mekugiNativeCommand(mekugiHistory{patch: step.patch}) + "\nelse\n" + commands[index] + "\nfi"
			}
		}
		command := strings.Join(commands, "\n")
		if len(metadata) != 0 {
			command += "\nmekugi_status=$?\nprintf '\\n%s\\n' " + shellQuoteArgument(string(mustMarshalJSON(metadata))) + "\nexit \"$mekugi_status\""
		}
		return string(mustMarshalJSON(execCommandArguments(command, params))), true
	}
	var program strings.Builder
	program.WriteString(shellCatSequenceRuntime)
	program.WriteString("try {\n")
	writeShellCatSequence(&program, steps, commands, params)
	// Keep prefix diagnostics even when a later tool refuses or fails. Finally
	// does not catch the host error, advance the sequence, or retry a write.
	program.WriteString("} finally {\ntext(JSON.stringify(Object.assign({}, last, {output}, ")
	program.Write(mustMarshalJSON(metadata))
	program.WriteString(")));\n}\n")
	return program.String(), true
}

func writeShellCatSequence(program *strings.Builder, steps []shellCatStep, commands []string, params map[string]json.RawMessage) {
	for index, step := range steps {
		encoded := string(mustMarshalJSON(execCommandArguments(commands[index], params)))
		if step.patch == "" {
			fmt.Fprintf(program, "await run(%s);\n", encoded)
			continue
		}
		guard := string(mustMarshalJSON(execCommandArguments(step.guard, params)))
		fmt.Fprintf(program, "if ((await finish(%s)).exit_code === 0) {\nawait tools.apply_patch(%s);\nlast = {output: '', exit_code: 0};\n} else { await run(%s); }\n", guard, mustMarshalJSON(step.patch), encoded)
	}
}

// Await host-owned sessions before the next statement, without exposing helper
// results or patch success text in the original shell result. An enclosing Code
// Mode cell may yield normally while these awaited calls remain in flight.
const shellCatSequenceRuntime = `let output = '';
let last = {output: '', exit_code: 0};
async function finish(args, forward = false) {
  let result = await tools.exec_command(args);
  if (forward) { last = result; output += result.output || ''; }
  while (result.session_id != null) {
    const continuation = {session_id: result.session_id, chars: '', yield_time_ms: 300000};
    if (args.max_output_tokens != null) continuation.max_output_tokens = args.max_output_tokens;
    result = await tools.write_stdin(continuation);
    if (forward) { last = result; output += result.output || ''; }
  }
  return result;
}
async function run(args) {
  await finish(args, true);
}
`
