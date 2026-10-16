package router

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func nativePatchesInCall(name, input, workspace string) []changecapture.PatchObservation {
	var patches []string
	if name == applyPatchToolName {
		patches = []string{input}
	} else if name == "exec" {
		patches = stockLiteralPatchInputs(input)
	}
	return changecapture.Patches(patches, workspace)
}

// Inspect the entire exec syntax tree rather than only transparent
// one-call wrappers. This recognizes literal patch arguments in ordinary JS
// sequencing and Promise batches, plus immutable top-level literal bindings
// used by a top-level expression. It never evaluates JavaScript or takes over
// the host tool. Dynamic arguments have no trustworthy pre-edit baseline.
func stockLiteralPatchInputs(source string) []string {
	if len(source) > commandanalysis.MaxScriptBytes {
		return nil
	}
	bytes := []byte(source)
	tree, err := sourceparse.Parse(bytes, sourceparse.JavaScript, nil)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil
	}
	bindings := make(map[string]string)
	for index := range root.NamedChildCount() {
		statement := root.NamedChild(uint(index))
		if statement.Kind() != "lexical_declaration" || statement.ChildCount() == 0 ||
			statement.Child(0).Kind() != "const" {
			continue
		}
		for childIndex := range statement.NamedChildCount() {
			declaration := statement.NamedChild(uint(childIndex))
			if declaration.Kind() != "variable_declarator" {
				continue
			}
			name, value := declaration.ChildByFieldName("name"), declaration.ChildByFieldName("value")
			if name == nil || name.Kind() != "identifier" {
				continue
			}
			literal, ok := sourceparse.JavaScriptValue(value, bytes)
			if text, isText := literal.(string); ok && isText {
				bindings[name.Utf8Text(bytes)] = text
			}
		}
	}
	var patches []string
	for rootIndex := range root.NamedChildCount() {
		rootStatement := root.NamedChild(uint(rootIndex))
		allowBinding := rootStatement.Kind() == "expression_statement" ||
			rootStatement.Kind() == "lexical_declaration" || rootStatement.Kind() == "variable_declaration"
		type visit struct {
			node         *sitter.Node
			allowBinding bool
		}
		stack := []visit{{node: rootStatement, allowBinding: allowBinding}}
		for len(stack) != 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			node := current.node
			if node != rootStatement {
				switch node.Kind() {
				case "arrow_function", "function_expression", "function_declaration",
					"generator_function", "generator_function_declaration", "method_definition", "class":
					current.allowBinding = false
				}
			}
			if args, ok := sourceparse.JavaScriptCallArguments(node, bytes, "tools", applyPatchToolName); ok && len(args) == 1 {
				if value, ok := sourceparse.JavaScriptValue(args[0], bytes); ok {
					if patch, ok := value.(string); ok {
						patches = append(patches, patch)
					}
				} else if current.allowBinding && args[0].Kind() == "identifier" {
					if patch, ok := bindings[args[0].Utf8Text(bytes)]; ok {
						patches = append(patches, patch)
					}
				}
			}
			if len(patches) > 32 {
				return nil
			}
			for index := int(node.NamedChildCount()) - 1; index >= 0; index-- {
				stack = append(stack, visit{node: node.NamedChild(uint(index)), allowBinding: current.allowBinding})
			}
		}
	}
	return patches
}

// Decode arriving patch text without requiring the closing quote, call, or
// envelope. This is display-only and must never feed capture or execution.
func stockPatchFragment(source string) string {
	if len(source) > commandanalysis.MaxScriptBytes {
		return ""
	}
	ranges, _, _ := codeModePreviewSyntax(source)
	rangeIndex := 0
	patch := ""
	for at := 0; at < len(source); {
		if end := codeModeRangeEnd(ranges, &rangeIndex, at); end > at {
			at = end
			continue
		}
		if next := codeModeSkipComment(source, at); next > at {
			at = next
			continue
		}
		if source[at] == '\'' || source[at] == '"' || source[at] == '`' {
			value, consumed := toolActivityJavaScriptStringFragment(source[at:])
			if _, ok := nativePatchPreview(value); ok {
				patch = value
			}
			at += max(1, consumed)
			continue
		}
		at++
	}
	return patch
}

func toolActivityJavaScriptStringFragment(source string) (string, int) {
	quote := source[0]
	var value strings.Builder
	for offset := 1; offset < len(source); {
		if quote == '`' && strings.HasPrefix(source[offset:], "${") {
			return value.String(), codeModeSkipString(source, 0)
		}
		if source[offset] == quote {
			return value.String(), offset + 1
		}
		if quote == '`' && (source[offset] == '\r' || source[offset] == '\n') {
			value.WriteByte(source[offset])
			offset++
			continue
		}
		if source[offset] == '\r' || source[offset] == '\n' {
			return value.String(), offset
		}
		if source[offset] == '\\' && offset+1 < len(source) && (strings.ContainsRune(`'"/\\`, rune(source[offset+1])) || quote == '`' && (source[offset+1] == '`' || source[offset+1] == '$')) {
			value.WriteByte(source[offset+1])
			offset += 2
			continue
		}
		char, _, tail, err := strconv.UnquoteChar(source[offset:], quote)
		if err != nil {
			// Go and JavaScript accept different escape sets. If this display-only
			// decoder cannot interpret one, recover at the string's lexical end so
			// a later patch literal is still inspected.
			for cursor := offset; cursor < len(source); {
				if source[cursor] == '\\' {
					cursor = min(len(source), cursor+2)
					continue
				}
				if source[cursor] == quote {
					return value.String(), cursor + 1
				}
				if source[cursor] == '\r' || source[cursor] == '\n' {
					return value.String(), cursor
				}
				_, size := utf8.DecodeRuneInString(source[cursor:])
				cursor += max(1, size)
			}
			return value.String(), len(source)
		}
		value.WriteRune(char)
		offset = len(source) - len(tail)
	}
	return value.String(), len(source)
}

func (p *mekugiProxy) finalizeNativePatches(ctx context.Context, workspace, thread, callID string, history mekugiHistory, result binding.ExecutionResult) error {
	if len(history.NativePatches) == 0 {
		return nil
	}
	if !result.Terminal {
		return nil
	}
	windowFiles := changecapture.PatchWindowFiles(history.NativePatches)
	occurrences := make(map[string]int)
	for index, observation := range history.NativePatches {
		occurrence := occurrences[observation.Input]
		occurrences[observation.Input]++
		if history.nativeCell.PatchPending(observation.Input, occurrence) {
			continue // Outer completion cannot finish a still-running nested edit.
		}
		derivedCallID := legacycall.PatchCallID(callID, index)
		if retained, found, err := p.replayStore.lookup(ctx, workspace, derivedCallID); err != nil {
			return err
		} else if found {
			if retained.CorrelationID != callID+"\x00"+strconv.Itoa(index) || retained.Script != observation.Input {
				return errors.New("retained stock apply_patch observation is inconsistent")
			}
			continue
		}
		correlation := callID + "\x00" + strconv.Itoa(index)
		reviews, complete := changecapture.ReviewPatch(windowFiles[index])
		// Attempts remain durable, but IDs select actual or incomplete file
		// evidence, not successful no-ops or rejected patches with no effects.
		changeID := ""
		if len(reviews) != 0 && p.replayStore != nil {
			var err error
			changeID, err = p.replayStore.changes.Reserve(ctx, p.replayStore.scoped(ctx).changeScope(), workspace, thread, correlation)
			if err != nil {
				return changeError(err)
			}
		}
		success := history.ToolName == applyPatchToolName && result.Succeeded && complete
		nested, confirmed := history.nativeCell.Patch(observation.Input, occurrence)
		if confirmed {
			success = nested.Status == "completed" && complete
		}
		attempt := mekugiHistory{
			ToolName:         applyPatchToolName,
			Script:           observation.Input,
			Root:             workspace,
			ExecutingThread:  thread,
			Caller:           history.Caller,
			ChangeID:         changeID,
			CorrelationID:    correlation,
			Attempt:          1,
			AlreadySatisfied: success && len(reviews) == 0 && len(history.NativePatches) == 1,
			ReviewFiles:      reviews,
			Report:           result.Text,
			CarrierKind:      codeModeCarrierCustom,
			CarrierName:      applyPatchToolName,
			CarrierPayload:   observation.Input,
			ReplayCarrier:    true,
			UpstreamItem: map[string]json.RawMessage{
				"type":    responses.MustMarshalJSON("custom_tool_call"),
				"name":    responses.MustMarshalJSON(applyPatchToolName),
				"call_id": responses.MustMarshalJSON(derivedCallID),
				"input":   responses.MustMarshalJSON(observation.Input),
				"status":  responses.MustMarshalJSON("completed"),
			},
		}
		if len(history.NativePatches) > 1 {
			attempt.Report += "\nFile evidence spans the exec cell; shared paths are recorded once at their first patch observation, not as per-patch effects."
		}
		if confirmed {
			attempt.HostResults = []binding.ToolResult{nested}
			if nested.Status != "completed" {
				attempt.TranslationError = "host tool " + nested.CallID + " " + nested.Tool + ": " + nested.Status
				if nested.ExitCode != nil {
					attempt.TranslationError += "; exit " + strconv.Itoa(*nested.ExitCode)
				}
			}
		}
		if history.ToolName == applyPatchToolName && !result.Succeeded {
			attempt.TranslationError = strings.TrimSpace(result.Text)
			if attempt.TranslationError == "" {
				attempt.TranslationError = "stock apply_patch failed"
			}
		}
		if !complete && attempt.Report != "" {
			attempt.Report += "\nMekugi could not capture complete file evidence."
		}
		if err := p.replayStore.put(context.WithoutCancel(ctx), workspace, map[string]mekugiHistory{derivedCallID: attempt}); err != nil {
			return changeError(err)
		}
		if len(reviews) != 0 {
			_ = p.replayStore.publishEditReceipt(context.WithoutCancel(ctx), workspace, thread, derivedCallID, p.activity, responses.StringField(history.UpstreamItem, "id"))
		}
	}
	return nil
}

func nativePatchPreview(input string) (previewmodel.Preview, bool) {
	// Incomplete input is useful provisional display, never application evidence.
	if input != "*** Begin Patch" && !strings.HasPrefix(input, "*** Begin Patch\n") && !strings.HasPrefix(input, "*** Begin Patch\r\n") {
		return previewmodel.Preview{}, false
	}
	return previewmodel.Preview{Input: input, DiffText: true, Status: previewmodel.PreviewEdit}, true
}
