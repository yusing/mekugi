package codexbinding

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/yusing/mekugi/modules/livepreview/projection"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// Source: apps/mekugi/internal/router/live_diff_preview.go:152:181,317:409@72549ff1.
const (
	applyPatchToolName        = "apply_patch"
	nativeExecCommandToolName = "exec_command"
)

func projectStockPreview(ctx context.Context, input, workspace string, final bool, readPatch func(string) (string, bool, error)) (previewmodel.Preview, bool) {
	preview, ok := nativePatchPreview(input)
	if !ok {
		return previewmodel.Preview{}, false
	}
	projectionContext := ctx
	if final {
		projectionContext = context.WithoutCancel(projectionContext)
	}
	ctx, cancel := context.WithTimeout(projectionContext, time.Second)
	defer cancel()
	if !final {
		// Decode first: an outer JavaScript newline need not terminate a
		// patch line inside a string literal.
		tip := strings.LastIndexByte(input, '\n') + 1
		if strings.HasSuffix(input, ";") && strings.HasPrefix(input[tip:], "+") {
			preview.Input = input
		} else {
			preview.Input = input[:tip]
		}
		if preview.Input == "" {
			preview.Input = "\n"
			return preview, true
		}
	}
	preview.Complete = final
	preview.Tool = applyPatchToolName
	projected := projection.AssessPatch(ctx, workspace, preview, readPatch)
	return projected, true
}

// Project recognizes the edit in a call's received input. Command and script
// text that is not an edit has no preview.
func Project(ctx context.Context, workspace, kind, input string, final bool, readPatch func(string) (string, bool, error)) (previewmodel.Preview, bool) {
	shell := func(call codeModeShellCall) (previewmodel.Preview, bool) {
		files, tool, recognized, err := projection.Shell(ctx, call.cmd, liveDiffWorkdir(workspace, call.workdir), final)
		switch {
		case recognized && call.dynamicWorkdir:
			return previewmodel.Preview{Status: previewmodel.PreviewUnavailable + "edit target depends on a computed workdir"}, true
		case !recognized || err != nil:
			return previewmodel.Preview{}, recognized
		}
		return previewmodel.Preview{Files: files, Tool: tool, Status: previewmodel.PreviewEdit}, true
	}
	switch kind {
	case applyPatchToolName:
		return projectStockPreview(ctx, input, workspace, final, readPatch)
	case nativeExecCommandToolName:
		call, ok := liveDiffExecArguments(input)
		if !ok {
			return previewmodel.Preview{}, false
		}
		return shell(call)
	case "exec":
		if calls, ok := binding.ExecutionCalls(input, false); ok {
			for _, call := range slices.Backward(calls) {
				switch responses.StringField(call, "name") {
				case applyPatchToolName:
					return projectStockPreview(ctx, responses.StringField(call, "input"), workspace, final, readPatch)
				case nativeExecCommandToolName:
					var arguments map[string]jsonv1.RawMessage
					if json.Unmarshal([]byte(responses.StringField(call, "arguments")), &arguments, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)) != nil {
						return previewmodel.Preview{}, false
					}
					return shell(codeModeShellCall{cmd: responses.StringField(arguments, "cmd"), workdir: responses.StringField(arguments, "workdir")})
				}
			}
			return previewmodel.Preview{}, false
		}
		patches := binding.LiteralPatchInputs(input)
		if fragment := stockPatchFragment(input); fragment != "" {
			patches = append(patches, fragment)
		}
		if len(patches) != 0 {
			if projected, ok := projectStockPreview(ctx, patches[len(patches)-1], workspace, final, readPatch); ok {
				return projected, true
			}
		}
		calls := codeModeShellFragments(input)
		if len(calls) == 0 {
			return previewmodel.Preview{}, false
		}
		return shell(calls[len(calls)-1])
	default:
		program := input
		if programs, err := shellsyntax.Split(input); err == nil {
			// Preview the current program, not earlier shell framing.
			program = programs[len(programs)-1]
		}
		return shell(codeModeShellCall{cmd: program})
	}
}

// liveDiffExecArguments reads exec_command arguments, including an unfinished
// JSON object whose cmd string is still arriving.
func liveDiffExecArguments(input string) (codeModeShellCall, bool) {
	var arguments map[string]jsonv1.RawMessage
	if json.Unmarshal([]byte(input), &arguments, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)) == nil {
		call := codeModeShellCall{cmd: responses.StringField(arguments, "cmd"), workdir: responses.StringField(arguments, "workdir")}
		if raw, exists := arguments["workdir"]; exists && json.Unmarshal(raw, new(string), jsontext.AllowInvalidUTF8(true)) != nil {
			call.dynamicWorkdir = true
		}
		return call, call.cmd != ""
	}
	start := strings.IndexFunc(input, func(char rune) bool { return !unicode.IsSpace(char) })
	if start < 0 || input[start] != '{' {
		return codeModeShellCall{}, false
	}
	call, _, _ := codeModeShellObject(input, start)
	return call, call.cmd != ""
}

// Codex resolves a relative workdir against the turn directory.
func liveDiffWorkdir(workspace, workdir string) string {
	switch {
	case workdir == "":
		return workspace
	case filepath.IsAbs(workdir):
		return workdir
	case filepath.IsAbs(workspace):
		return filepath.Join(workspace, workdir)
	}
	return workspace
}
