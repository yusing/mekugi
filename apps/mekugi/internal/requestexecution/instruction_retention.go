package requestexecution

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"sync"
	"time"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

const instructionRetentionTimeout = 2 * time.Second

// Admitted request JSON is selected, not revalidated: duplicate names or
// invalid UTF-8 in an unrelated item must not change the selection.
var tolerantJSON = json.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))

// A decoder reading a bytes.Buffer parses the borrowed input in place; pooling
// both keeps the per-request scan free of allocations.
type instructionScanner struct {
	input   bytes.Buffer
	decoder jsontext.Decoder
}

var instructionScanners = sync.Pool{New: func() any { return new(instructionScanner) }}

// scanArray visits each element of a JSON array; values are borrowed until
// visit returns. A non-array value has no elements.
func scanArray(value jsontext.Value, visit func(*jsontext.Decoder, jsontext.Value) error) error {
	if value = bytes.TrimSpace(value); len(value) == 0 || value[0] != '[' {
		return nil
	}
	scanner := instructionScanners.Get().(*instructionScanner)
	defer func() {
		scanner.input.Reset()
		scanner.decoder.Reset(&scanner.input)
		instructionScanners.Put(scanner)
	}()
	scanner.input = *bytes.NewBuffer(value)
	decoder := &scanner.decoder
	decoder.Reset(&scanner.input, tolerantJSON)
	if _, err := decoder.ReadToken(); err != nil {
		return err
	}
	for decoder.PeekKind() != ']' {
		if err := visit(decoder, value); err != nil {
			return err
		}
	}
	_, err := decoder.ReadToken()
	return err
}

// jsonStringIs compares a raw JSON string, unquoting only escaped text.
func jsonStringIs(raw jsontext.Value, want string) bool {
	if len(raw) < 2 || raw[0] != '"' {
		return false
	}
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw[1:len(raw)-1]) == want
	}
	unquoted, err := jsontext.AppendUnquote(nil, raw)
	return err == nil && string(unquoted) == want
}

// visitInstructionItems visits developer messages and additional tool items
// in input order, reading only each item's top-level role and type.
func visitInstructionItems(input jsontext.Value, visit func(diagnostic.InstructionField, jsontext.Value)) error {
	return scanArray(input, func(decoder *jsontext.Decoder, input jsontext.Value) error {
		start := decoder.InputOffset()
		if decoder.PeekKind() != '{' {
			return decoder.SkipValue()
		}
		if _, err := decoder.ReadToken(); err != nil {
			return err
		}
		developer, additional := false, false
		for decoder.PeekKind() != '}' {
			name, err := decoder.ReadValue()
			if err != nil {
				return err
			}
			switch {
			case jsonStringIs(name, "role"):
				value, err := decoder.ReadValue()
				if err != nil {
					return err
				}
				developer = jsonStringIs(value, "developer")
			case jsonStringIs(name, "type"):
				value, err := decoder.ReadValue()
				if err != nil {
					return err
				}
				additional = jsonStringIs(value, "additional_tools")
			default:
				if err := decoder.SkipValue(); err != nil {
					return err
				}
			}
		}
		if _, err := decoder.ReadToken(); err != nil {
			return err
		}
		item := bytes.TrimLeft(input[start:decoder.InputOffset()], " \t\r\n,")
		if developer {
			visit(diagnostic.DeveloperMessage, item)
		}
		if additional {
			visit(diagnostic.AdditionalTool, item)
		}
		return nil
	})
}

// instructionContent visits the model-visible values of projected request
// fields. A decode failure is returned; callers discard what was visited.
type instructionContent map[string]jsontext.Value

func (fields instructionContent) visit(visitor diagnostic.InstructionVisitor) error {
	if instructions := bytes.TrimSpace(fields["instructions"]); len(instructions) != 0 && string(instructions) != "null" {
		visitor.Visit(diagnostic.InstructionText, instructions)
	}
	if err := visitInstructionItems(fields["input"], visitor.Visit); err != nil {
		return err
	}
	return scanArray(fields["tools"], func(decoder *jsontext.Decoder, _ jsontext.Value) error {
		tool, err := decoder.ReadValue()
		if err == nil {
			visitor.Visit(diagnostic.ToolDescription, tool)
		}
		return err
	})
}

// instructionItems copies the selected input items for debug dumps.
func instructionItems(input jsontext.Value) (developers, additional []jsontext.Value) {
	developers, additional = []jsontext.Value{}, []jsontext.Value{}
	if visitInstructionItems(input, func(field diagnostic.InstructionField, item jsontext.Value) {
		if field == diagnostic.DeveloperMessage {
			developers = append(developers, slices.Clone(item))
		} else {
			additional = append(additional, slices.Clone(item))
		}
	}) != nil {
		return []jsontext.Value{}, []jsontext.Value{}
	}
	return developers, additional
}

// retainInstructions keeps the final projection forwarded for a model turn.
// Retention is auxiliary: a failure notifies without failing the request.
func (a *requestAttempt) retainInstructions() {
	retention := a.executor.Instructions
	if retention == nil || a.prewarm || a.approvalReview || a.threadID == "" {
		return
	}
	captureID, sequence := transportcapture.RequestCorrelation(a.startCtx)
	ctx, cancel := context.WithTimeout(a.startCtx, instructionRetentionTimeout)
	defer cancel()
	err := retention.Observe(ctx, diagnostic.InstructionRequest{
		Thread: a.threadID, Model: a.request.Model(), TurnID: a.metadata.TurnID,
		RequestID: captureID, RequestSequence: sequence, Time: a.started,
		Content: instructionContent(a.request.Fields).visit,
	})
	if err != nil && a.startCtx.Err() == nil {
		a.executor.Issues.AddThreadNotice(a.sessionID, a.threadID, "instruction_storage", "Mekugi could not retain this thread's model instructions for inspection: "+err.Error())
	}
}
