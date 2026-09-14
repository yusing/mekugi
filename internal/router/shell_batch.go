package router

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

// Prepare every program before emitting a carrier, so an invalid later header
// or interpreter cannot cause a valid prefix to execute.
func (t *mekugiResponseTransform) prepareShellBatch(contribution toolContribution, sources []string, pathPrefix string, callIDs ...string) ([]string, toolplugin.Translation, error) {
	if t.nativeTools {
		return nil, toolplugin.Translation{}, fmt.Errorf("shell batches require Code Mode; submit separate shell calls with this client")
	}
	programs := make([]string, 0, len(sources))
	for index, source := range sources {
		translation, err := t.proxy.registry.builtinTranslator.Translate(t.ctx, contribution.ModuleIndex, source, pathPrefix)
		if err != nil {
			return nil, toolplugin.Translation{}, fmt.Errorf("shell program %d: %w", index+1, err)
		}
		if translation.Rejected {
			translation.Diagnostic = fmt.Sprintf("shell program %d: %s", index+1, translation.Diagnostic)
			return nil, translation, nil
		}
		if shellTypeScriptMisuse(contribution, translation.Arguments) {
			return nil, toolplugin.Translation{Rejected: true, Diagnostic: fmt.Sprintf("shell program %d: %s", index+1, shellTypeScriptDiagnostic)}, nil
		}
		if translation.Carrier.Kind != "exec" {
			return nil, toolplugin.Translation{}, fmt.Errorf("shell program %d: expected an exec carrier", index+1)
		}
		var program strings.Builder
		steps, commands, catWrites := t.shellCatPlan(contribution, translation.Arguments, translation.Carrier.Template, translation.Carrier.Params, max(1, 8000/len(sources)), callIDs...)
		if catWrites {
			writeShellCatSequence(&program, steps, commands, translation.Carrier.Params)
		} else {
			command, err := t.proxy.registry.execCarrierCommand(contribution, source, translation.Arguments, translation.Carrier.Template, max(1, 8000/len(sources)), callIDs...)
			if err != nil {
				return nil, toolplugin.Translation{}, fmt.Errorf("shell program %d: %w", index+1, err)
			}
			for _, misuse := range shellInterpreterWrapperMisuses(contribution, source) {
				fmt.Fprintf(&program, "output += %s;\n", mustMarshalJSON(shellInterpreterWrapperWarning(misuse)+"\n"))
			}
			fmt.Fprintf(&program, "await run(%s);\n", mustMarshalJSON(execCommandArguments(command, translation.Carrier.Params)))
		}
		programs = append(programs, program.String())
	}
	retain := true
	return programs, toolplugin.Translation{Carrier: toolplugin.Carrier{Kind: "exec", RetainInput: &retain}}, nil
}

// Each result preserves one program's terminal native fields and ordered output,
// including nonzero exits. Host errors propagate after publishing the completed
// prefix and current partial output; no remaining program runs in that case.
func renderShellBatch(programs []string, metadata map[string]json.RawMessage, stopOnNonzero bool) string {
	var program strings.Builder
	program.WriteString(shellCatSequenceRuntime)
	program.WriteString("const results = [];\nlet stopped_reason = null;\nbatch: try {\n")
	for _, source := range programs {
		program.WriteString("output = ''; last = {output: ''};\ntry {\n")
		program.WriteString(source)
		program.WriteString("} finally { results.push(Object.assign({}, last, {output})); }\n")
		if stopOnNonzero {
			program.WriteString("if (last.exit_code !== undefined && last.exit_code !== null && last.exit_code !== 0) { stopped_reason = 'nonzero_exit'; break batch; }\n")
		}
	}
	policy := "continue"
	if stopOnNonzero {
		policy = "stop"
	}
	fmt.Fprintf(&program, "} catch (error) { stopped_reason = 'host_error'; throw error; } finally {\ntext(JSON.stringify(Object.assign({results, batch: {on_nonzero_exit: %q, program_count: %d, started_programs: results.length, not_started_programs: %d - results.length, stopped_reason}}, ", policy, len(programs), len(programs))
	program.Write(mustMarshalJSON(metadata))
	program.WriteString(")));\n}\n")
	return program.String()
}
