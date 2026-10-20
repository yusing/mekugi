// Source: internal/router/session_inspect.go and session_corpus.go@[d9f28a74] host evidence interpretation.
package codexbinding

import (
	"cmp"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type Bindings = binding.Bindings[*query.Service]

var Codex = Bindings{Requires: []capability.Requirement{{Mechanism: capability.Rollout, Required: true}}, Rollout: bindRollout}

func bindRollout(service *query.Service, source binding.RolloutSource) error {
	read := source.Inspect
	return service.BindSource(func(ctx context.Context, path string, observe func(inspection.Event) error) (query.Rollout, error) {
		var observer func(binding.RolloutEvent) error
		if observe != nil {
			observer = func(event binding.RolloutEvent) error { return observe(inspection.Event(event)) }
		}
		decoded, err := read(ctx, path, observer)
		if err != nil {
			return query.Rollout{}, err
		}
		result := query.Rollout{ThreadID: decoded.ThreadID, Cwd: decoded.Cwd, Models: decoded.Models}
		var origin string
		_ = json.Unmarshal(decoded.Source, &origin, jsonv1.DefaultOptionsV1())
		switch origin {
		case "cli":
			result.Origin = "interactive"
		case "exec":
			result.Origin = "batch"
		}
		if result.Origin == "" {
			var child struct {
				Subagent jsonv1.RawMessage `json:"subagent"`
			}
			if json.Unmarshal(decoded.Source, &child, jsonv1.DefaultOptionsV1()) == nil && len(child.Subagent) > 0 {
				result.Origin = "child"
			}
		}
		for _, call := range decoded.Calls {
			tool := call.Item.Name
			if call.Item.Namespace != "" {
				tool = call.Item.Namespace + "." + tool
			}
			projected := query.Call{ID: call.Item.CallID, Tool: tool, Input: sessionInspectionPayload(call.Item), Workspace: call.Workspace, Timestamp: call.Timestamp, Model: call.Model, Line: call.Line, Evidence: callEvidence{call}}
			if len(call.Outputs) != 0 {
				projected.HasOutput = true
				output := call.Outputs[len(call.Outputs)-1].Output
				if json.Unmarshal(output, &projected.Output, jsonv1.DefaultOptionsV1()) != nil {
					projected.Output = string(output)
				}
			}
			for _, output := range call.Outputs {
				if strings.Contains(string(output.Output), "read: incomplete; next_call: mread ") {
					projected.ReadContinuation = true
				}
			}
			result.Calls = append(result.Calls, projected)
		}
		return result, nil
	})
}

type callEvidence struct{ call binding.RolloutCall }

func (e callEvidence) Verify(history legacycall.History) error {
	call := e.call
	if history.ToolName == "" || history.CarrierName == "" ||
		(history.CarrierKind != "" && history.CarrierKind != "custom" &&
			history.CarrierKind != "function") {
		return fmt.Errorf("call %q: invalid replay history identity", call.Item.CallID)
	}
	if call.HasCall {
		kind := cmp.Or(history.CarrierKind, "custom")
		carrierMatches := call.Item.Type == itemType(kind) &&
			call.Item.Name == history.CarrierName &&
			call.Item.Namespace == responses.StringField(history.UpstreamItem, "namespace") &&
			sessionInspectionPayload(call.Item) == history.CarrierPayload
		originalMatches := call.Item.Type == responses.StringField(history.UpstreamItem, "type") &&
			call.Item.Name == responses.StringField(history.UpstreamItem, "name") &&
			call.Item.Namespace == responses.StringField(history.UpstreamItem, "namespace") &&
			sessionInspectionPayload(call.Item) == responses.StringField(history.UpstreamItem, carrierPayloadFieldForItem(call.Item.Type))
		if !carrierMatches && !originalMatches {
			return fmt.Errorf("call %q: replay payload does not match session", call.Item.CallID)
		}
	}

	return nil
}
func itemType(kind string) string {
	if kind == "function" {
		return "function_call"
	}
	return "custom_tool_call"
}
func retainedExecution(history *legacycall.History) *binding.RetainedExecution {
	if history == nil || history.TranslationError != "" {
		return nil
	}
	return &binding.RetainedExecution{CarrierKind: cmp.Or(history.CarrierKind, "custom"), ToolName: history.ToolName, Script: history.Script}
}
func sessionInspectionPayload(item binding.RolloutItem) string {
	if item.Type == "function_call" {
		return item.Arguments
	}
	return item.Input
}

func carrierPayloadFieldForItem(itemType string) string {
	if itemType == "function_call" {
		return "arguments"
	}
	return "input"
}

func (e callEvidence) Commands() []string {
	call := e.call
	if call.Item.Namespace != "" && call.Item.Namespace != "functions" {
		return nil
	}
	readCommand := func(input string) []string {
		var arguments struct {
			Command string `json:"cmd"`
		}
		if json.Unmarshal([]byte(input), &arguments, jsonv1.DefaultOptionsV1()) != nil {
			return nil
		}
		return []string{arguments.Command}
	}
	name := strings.TrimPrefix(call.Item.Name, "functions.")
	if call.Item.Type == "function_call" && name == "exec_command" {
		return readCommand(call.Item.Arguments)
	}
	if call.Item.Type != "custom_tool_call" || name != "exec" {
		return nil
	}
	calls, ok := binding.ExecutionCalls(call.Item.Input, false)
	if !ok {
		return nil
	}
	var selections []string
	for _, nested := range calls {
		if responses.StringField(nested, "name") == "exec_command" {
			selections = append(selections, readCommand(responses.StringField(nested, "arguments"))...)
		}
	}
	return selections
}

func (e callEvidence) EmptyPoll(history *legacycall.History) bool {
	call := e.call
	if len(call.Outputs) == 0 {
		return false
	}
	var item map[string]jsonv1.RawMessage
	if json.Unmarshal(responses.MustMarshalJSON(call.Item), &item, jsonv1.DefaultOptionsV1()) != nil {
		return false
	}
	if !inspectionEmptyInput(item) {
		return false
	}
	classified := binding.ClassifyExecutionCall(item, retainedExecution(history), "exec")
	texts := binding.ExecutionOutputTexts(call.Outputs[len(call.Outputs)-1].Output)
	if len(texts) == 0 {
		return false
	}
	var session int64
	if classified.Native {
		session = binding.NativeExecutionSession(texts[0])
	} else if classified.CodeMode {
		state, _, body := binding.CodeModeExecutionHeader(texts[0])
		if state == "" || state == "running" || state == "Script terminated" {
			return false
		}
		texts[0] = body
		for _, text := range texts {
			if id := binding.NativeJSONSession(text); id != 0 {
				if session != 0 {
					return false
				}
				session = id
			}
		}
	}
	if session == 0 {
		return false
	}
	return inspectionEmptyOutput(classified, texts, session)
}

// Empty-poll recognition belongs to offline evidence analysis, not continuation policy.
func inspectionEmptyInput(item map[string]jsonv1.RawMessage) bool {
	if namespace := responses.StringField(item, "namespace"); namespace != "" && namespace != "functions" {
		return false
	}
	name := strings.TrimPrefix(responses.StringField(item, "name"), "functions.")
	if name == "exec" {
		source := responses.StringField(item, "input")
		nested, ok := binding.SingleExecutionCall(source, false)
		if !ok {
			return false
		}
		item, name = nested, responses.StringField(nested, "name")
	}
	var args struct {
		Chars string `json:"chars"`
	}
	return name == "write_stdin" && json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args, jsonv1.DefaultOptionsV1()) == nil && args.Chars == ""
}

func inspectionEmptyOutput(call binding.ExecutionCall, texts []string, session int64) bool {
	if call.ResumeHandle != "session:"+strconv.FormatInt(session, 10) {
		return false
	}
	if call.Native {
		_, body := binding.NativeExecutionHeader(texts[0])
		return body == "" && binding.NativeExecutionSession(texts[0]) == session
	}
	// Only transparent native output is evidence, not unrelated printed text.
	found := false
	for _, text := range texts {
		if text == "" {
			continue
		}
		part := responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": text})
		if binding.ExecutionAnnotationMatches(part, map[string]any{"session_id": session}) {
			continue
		}
		var output struct {
			Output *string `json:"output"`
		}
		if binding.NativeJSONSession(text) != session || json.Unmarshal([]byte(text), &output, jsonv1.DefaultOptionsV1()) != nil ||
			output.Output == nil || *output.Output != "" || found {
			return false
		}
		found = true
	}
	return found
}
