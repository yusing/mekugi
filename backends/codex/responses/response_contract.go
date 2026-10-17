package responses

import (
	"bytes"
	jsonv1 "encoding/json"
)

// ResponseContract retains native contract fields before model-facing projection.
// Source: apps/mekugi/internal/router/mekugi_response_transform.go:286:301@874a6e86.
type ResponseContract struct {
	tools, toolChoice               jsonv1.RawMessage
	toolsPresent, toolChoicePresent bool
}

func CaptureResponseContract(fields map[string]jsonv1.RawMessage) ResponseContract {
	tools, toolsPresent := fields["tools"]
	choice, choicePresent := fields["tool_choice"]
	return ResponseContract{tools: bytes.Clone(tools), toolChoice: bytes.Clone(choice), toolsPresent: toolsPresent, toolChoicePresent: choicePresent}
}

func (c ResponseContract) Restore(object map[string]jsonv1.RawMessage) {
	if _, ok := object["tools"]; ok {
		if !c.toolsPresent {
			delete(object, "tools")
		} else {
			object["tools"] = bytes.Clone(c.tools)
		}
	}
	if _, ok := object["tool_choice"]; ok {
		if !c.toolChoicePresent {
			delete(object, "tool_choice")
		} else {
			object["tool_choice"] = bytes.Clone(c.toolChoice)
		}
	}
}
