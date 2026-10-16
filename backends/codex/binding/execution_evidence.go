package binding

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/execution_continuation.go:31:40@[bb24aed2] annotation wire types.
// Source: internal/router/execution_continuation.go:165:265@[bb24aed2] execution evidence.
// Source: internal/router/execution_continuation.go:396:412@[bb24aed2] annotation recognition.

// ExecutionNextCall is advice encoded in a continuation annotation, not a request
// to execute a tool. The application selects it from the current host catalog.
type ExecutionNextCall struct {
	Tool  string `json:"tool"`
	Input any    `json:"input"`
}

// ExecutionContinuation is the retained annotation wire shape shared by the
// producing projection and exact annotation recognizer.
type ExecutionContinuation struct {
	Handle   map[string]any     `json:"handle"`
	NextCall *ExecutionNextCall `json:"next_call"`
	Reason   string             `json:"reason,omitempty"`
}

// RetainedExecution supplies validated carrier facts with a normalized kind.
// A nil value leaves classification to the original host input.
type RetainedExecution struct {
	CarrierKind string
	ToolName    string
	Script      string
}

// ExecutionCall describes recognized protocol evidence, not execution or success.
type ExecutionCall struct {
	CodeMode      bool
	Native        bool
	NativePayload bool
	CellID        string
	ResumeHandle  string
}

// ExecutionResumeHandle recognizes only literal host continuation arguments.
func ExecutionResumeHandle(item map[string]jsonv1.RawMessage, execName string) string {
	if namespace := responses.StringField(item, "namespace"); namespace != "" && namespace != "functions" {
		return ""
	}
	name := strings.TrimPrefix(responses.StringField(item, "name"), "functions.")
	if name == strings.TrimPrefix(execName, "functions.") {
		source := responses.StringField(item, "input")
		nested, ok := SingleExecutionCall(source, false)
		if !ok {
			return ""
		}
		item = nested
		name = responses.StringField(item, "name")
	}
	var args struct {
		CellID    string `json:"cell_id"`
		SessionID int64  `json:"session_id"`
	}
	if json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args, jsonv1.DefaultOptionsV1()) != nil {
		return ""
	}
	if name == "wait" && args.CellID != "" {
		return "cell:" + args.CellID
	}
	if name == "write_stdin" && args.SessionID > 0 {
		return "session:" + strconv.FormatInt(args.SessionID, 10)
	}
	return ""
}

// ExecutionOutputTexts decodes host output while preserving content-part positions.
func ExecutionOutputTexts(raw jsonv1.RawMessage) []string {
	var text string
	if json.Unmarshal(raw, &text, jsonv1.DefaultOptionsV1()) == nil {
		return []string{text}
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts, jsonv1.DefaultOptionsV1()) != nil || len(parts) == 0 || parts[0].Type != "input_text" {
		return nil
	}
	texts := make([]string, len(parts))
	for i, part := range parts {
		if part.Type == "input_text" {
			texts[i] = part.Text
		}
	}
	return texts
}

// ClassifyExecutionCall interprets original input and optional retained carrier
// facts. Callers validate retained identity before supplying those facts.
func ClassifyExecutionCall(item map[string]jsonv1.RawMessage, retained *RetainedExecution, execName string) ExecutionCall {
	if namespace := responses.StringField(item, "namespace"); namespace != "" && namespace != "functions" {
		return ExecutionCall{}
	}
	name := strings.TrimPrefix(responses.StringField(item, "name"), "functions.")
	call := ExecutionCall{ResumeHandle: ExecutionResumeHandle(item, execName)}
	if after, ok := strings.CutPrefix(call.ResumeHandle, "cell:"); ok {
		call.CellID = after
	}
	if retained != nil {
		call.CodeMode = retained.CarrierKind == "custom"
		call.Native = retained.CarrierKind == "function"
		call.NativePayload = retained.ToolName == "exec_command"
		if call.CodeMode && retained.ToolName == "exec" {
			if nested, ok := SingleExecutionCall(retained.Script, true); ok {
				name := responses.StringField(nested, "name")
				call.NativePayload = name == "exec_command" || name == "write_stdin"
			}
		}
		if call.CodeMode || call.Native {
			return call
		}
	}
	switch name {
	case "exec_command", "write_stdin":
		call.Native = true
	case strings.TrimPrefix(execName, "functions."):
		call.CodeMode = true
		if nested, ok := SingleExecutionCall(responses.StringField(item, "input"), true); ok {
			call.NativePayload = responses.StringField(nested, "name") == "exec_command" || responses.StringField(nested, "name") == "write_stdin"
		}
	case "wait":
		var args struct {
			CellID string `json:"cell_id"`
		}
		if json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args, jsonv1.DefaultOptionsV1()) == nil {
			call.CellID = args.CellID
			call.CodeMode = args.CellID != ""
		}
	}
	return call
}

// ExecutionAnnotationMatches accepts only the complete annotation wire shape
// for the supplied handle. Extra fields and unrelated text remain untouched.
func ExecutionAnnotationMatches(part jsonv1.RawMessage, handle map[string]any) bool {
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(part, &content, jsonv1.DefaultOptionsV1()) != nil || content.Type != "input_text" || !responses.SameJSONValue(part, responses.MustMarshalJSON(content)) {
		return false
	}
	var envelope struct {
		Continuation *ExecutionContinuation `json:"continuation"`
	}
	if json.Unmarshal([]byte(content.Text), &envelope, jsonv1.DefaultOptionsV1()) != nil || envelope.Continuation == nil {
		return false
	}
	return responses.SameJSONValue([]byte(content.Text), responses.MustMarshalJSON(envelope)) &&
		responses.SameJSONValue(responses.MustMarshalJSON(envelope.Continuation.Handle), responses.MustMarshalJSON(handle))
}
