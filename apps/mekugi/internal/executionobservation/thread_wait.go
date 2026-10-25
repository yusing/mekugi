package executionobservation

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Consumption is observation, including when compaction or an execution-free
// request reads injected messages without starting logical work.
func (p *Preparer) consumeThreadInput(ctx context.Context, thread, session string, request *codexresponses.Request) error {
	if p == nil || p.threadControl == nil || !bytes.Contains(request.Fields["input"], []byte("threadcontrol_")) {
		return nil
	}
	var items []struct {
		Type string `json:"type"`
		Role string `json:"role"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return err
	}
	var consumed []string
	for _, item := range items {
		if item.Type == "message" && item.Role == "assistant" && strings.HasPrefix(item.ID, "threadcontrol_") {
			consumed = append(consumed, item.ID)
		}
	}
	return p.threadControl.Coordination().InputConsumed(ctx, control.Identity{Thread: thread, Session: session}, consumed)
}

// observeThreadWait runs after the normal authenticated request/history join.
// It admits only a trailing host result for the wait's exact enclosing exec
// item, never a nested MCP success or a caller-authored output lookalike.
func (p *Preparer) observeThreadWait(ctx context.Context, thread, turn, execName string, request *codexresponses.Request, visible map[string]mekugiHistory) (string, error) {
	if p.threadControl == nil {
		return "", nil
	}
	wait, err := p.threadControl.Coordination().WaitForTurn(thread, turn)
	if err != nil || wait == nil {
		return "", err
	}
	if wait.State != "registered" {
		return "", nil
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return "", err
	}
	trailing := len(items)
	for trailing > 0 && strings.HasSuffix(responses.StringField(items[trailing-1], "type"), "_output") {
		trailing--
	}
	calls := make(map[string]map[string]jsontext.Value)
	for _, item := range items[:trailing] {
		if kind := responses.StringField(item, "type"); kind == "custom_tool_call" || kind == "function_call" {
			calls[responses.StringField(item, "call_id")] = item
		}
	}
	for _, output := range items[trailing:] {
		kind := responses.StringField(output, "type")
		if kind != "custom_tool_call_output" && kind != "function_call_output" {
			continue
		}
		callID := responses.StringField(output, "call_id")
		call := calls[callID]
		if call == nil {
			call = visible[callID].UpstreamItem
		} else if responses.StringField(call, "id") == "" {
			// Codex omits response-item IDs when serializing historical calls
			// for inference. Recover only the exact retained host call, not an
			// item selected by its tool name or script resemblance alone.
			original := visible[callID].UpstreamItem
			if responses.StringField(original, "call_id") == callID && responses.StringField(original, "name") == responses.StringField(call, "name") && responses.StringField(original, "input") == responses.StringField(call, "input") {
				call = original
			}
		}
		if namespace := responses.StringField(call, "namespace"); namespace != "" && namespace != "functions" {
			continue
		}
		if kind == "custom_tool_call_output" {
			if responses.StringField(call, "type") != "custom_tool_call" || responses.StringField(call, "id") != wait.Item || responses.StringField(call, "name") != execName {
				continue
			}
		} else {
			if responses.StringField(call, "type") != "function_call" || strings.TrimPrefix(responses.StringField(call, "name"), "functions.") != "wait" || wait.ResumeHandle == "" {
				continue
			}
			var arguments struct {
				CellID string `json:"cell_id"`
			}
			if json.Unmarshal([]byte(responses.StringField(call, "arguments")), &arguments) != nil || "cell:"+arguments.CellID != wait.ResumeHandle {
				continue
			}
		}
		result := binding.DecodeExecutionResult(execName, output["output"])
		if result.ResumeHandle != "" {
			return "", p.threadControl.Coordination().Execution(ctx, thread, turn, wait.Item, result.ResumeHandle, false)
		}
		if !result.Terminal || !result.Succeeded {
			return "", nil
		}
		return wait.Item, p.threadControl.Coordination().Execution(ctx, thread, turn, wait.Item, "", true)
	}
	return "", nil
}

// LocalWaitItem binds local completion to the successful enclosing result in
// this request. A later human input cannot reuse an earlier eligible stop.
func (t *mekugiResponseTransform) LocalWaitItem() string { return t.localWaitItem }
