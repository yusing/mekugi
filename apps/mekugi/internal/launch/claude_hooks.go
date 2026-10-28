package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/contracts/observation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/notifications/notice"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type claudeToolCapture struct {
	operation  *executionobservation.ClaudeToolOperation
	background bool
	completed  bool
	failed     bool
}

// claudeBridge joins authenticated native hooks to the existing request,
// coordination, approval and capture owners. It owns no execution ledger.
type claudeBridge struct {
	ctx             context.Context
	stopUpdates     context.CancelFunc
	execution       *threadExecution
	info            control.ThreadInfo
	settings        control.Settings
	runtime         *sessioncontrol.ClaudeRuntime
	selected        changes.Scope
	session         history.Session
	owners          *composition.Runtime
	checkpoints     *changecapture.Workspaces
	windows         *executionobservation.Windows
	policy          *claude.Policy
	mu              sync.Mutex
	captures        map[string]*claudeToolCapture
	changes         chan []changes.Update
	pendingReceipts []string
}

type claudeHook struct {
	Session     string         `json:"session_id"`
	Cwd         string         `json:"cwd"`
	Event       string         `json:"hook_event_name"`
	Tool        string         `json:"tool_name"`
	ID          string         `json:"tool_use_id"`
	Input       jsontext.Value `json:"tool_input"`
	Response    jsontext.Value `json:"tool_response"`
	Error       string         `json:"error"`
	Interrupted bool           `json:"is_interrupt"`
	Calls       []struct {
		ID       string         `json:"tool_use_id"`
		Tool     string         `json:"tool_name"`
		Response jsontext.Value `json:"tool_response"`
	} `json:"tool_calls"`
}

func (b *claudeBridge) identity(call string) (control.Identity, error) {
	if b.runtime == nil || call == "" {
		return control.Identity{}, errors.New("Claude tool has no admitted native runtime or call identity")
	}
	turn := b.runtime.CurrentTurn()
	if turn == "" {
		return control.Identity{}, errors.New("Claude tool has no active native turn")
	}
	return control.Identity{Thread: b.info.Thread, Session: b.info.Session, Turn: turn, Call: call, Item: call}, nil
}

func (b *claudeBridge) request(ctx context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
	switch method {
	case "tool":
		var request struct {
			Name      string         `json:"name"`
			ID        string         `json:"toolUseID"`
			Arguments jsontext.Value `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		identity, err := b.identity(request.ID)
		if err != nil {
			return nil, err
		}
		value, err := b.execution.driver.Call(ctx, identity, request.Name, request.Arguments)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(value)}}})
	case "permission":
		var request struct {
			Name      string         `json:"name"`
			ID        string         `json:"toolUseID"`
			Arguments jsontext.Value `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		identity, err := b.identity(request.ID)
		if err != nil {
			return nil, err
		}
		decision := b.policy.CheckApproval()
		allowed, reason, err := b.approve(ctx, identity, request.Name, request.Arguments, decision)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return json.Marshal(map[string]any{"behavior": "deny", "message": reason})
		}
		return json.Marshal(map[string]any{"behavior": "allow"})
	case "hook":
		var hook claudeHook
		if err := json.Unmarshal(raw, &hook); err != nil {
			return nil, err
		}
		if hook.Session != b.info.Session {
			return nil, errors.New("Claude hook native session identity mismatch")
		}
		if hook.Event == "SessionStart" {
			return jsontext.Value(`{"hookSpecificOutput":{"hookEventName":"SessionStart"}}`), nil
		}
		if hook.Event == "PostToolBatch" {
			return b.batch(ctx, hook)
		}
		identity, err := b.identity(hook.ID)
		if err != nil {
			return nil, err
		}
		switch hook.Event {
		case "PreToolUse":
			decision, err := b.policy.CheckTool(hook.Tool, hook.Input)
			if err != nil {
				return nil, err
			}
			// Frozen role reductions apply before the native permission engine.
			var config map[string]jsontext.Value
			if len(b.settings.Config) != 0 && json.Unmarshal(b.settings.Config, &config) != nil {
				return nil, errors.New("invalid frozen Claude capability configuration")
			}
			if (hook.Tool == "Bash" || hook.Tool == "PowerShell") && string(config["features.shell_tool"]) == "false" {
				decision = claude.Decision{Reason: "Shell tools are disabled by the captured role"}
			}
			if hook.Tool == "WebSearch" && string(config["web_search"]) == `"disabled"` {
				decision = claude.Decision{Reason: "Web search is disabled by the captured role"}
			}
			permission := ""
			if decision.Allow || decision.Ask || decision.Reason != "" {
				allow, reason, err := b.approve(ctx, identity, hook.Tool, hook.Input, decision)
				if err != nil {
					return nil, err
				}
				if !allow {
					return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": hook.Event, "permissionDecision": "deny", "permissionDecisionReason": reason}})
				}
				permission = "allow"
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, exists := b.captures[hook.ID]; !exists {
				caller := ""
				if member, ok := b.execution.shared.Member(b.info.Thread); ok {
					caller = member.Task
				}
				operation, err := executionobservation.PrepareClaudeTool(ctx, b.selected, b.owners.Changes.Service, b.checkpoints, b.owners.Recovery, func(updates []changes.Update) {
					select {
					case b.changes <- updates:
					case <-b.ctx.Done():
					}
				}, b.windows, nil, nil, executionobservation.ClaudeToolInput{Thread: identity.Thread, Turn: identity.Turn, Namespace: b.selected.Namespace, Workspace: b.settings.Workspace, Workdir: hook.Cwd, Caller: caller, ToolUseID: hook.ID, ToolName: hook.Tool, Shell: "bash", Arguments: hook.Input})
				if err != nil {
					return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": hook.Event, "additionalContext": err.Error()}})
				}
				b.captures[hook.ID] = &claudeToolCapture{operation: operation}
			}
			output := map[string]string{"hookEventName": hook.Event}
			if permission != "" {
				output["permissionDecision"] = permission
			}
			return json.Marshal(map[string]any{"hookSpecificOutput": output})
		case "PostToolUse", "PostToolUseFailure":
			receipt, err := b.complete(hook)
			if err != nil {
				receipt = err.Error()
			}
			b.mu.Lock()
			for _, pending := range b.pendingReceipts {
				receipt += "\n" + pending
			}
			b.pendingReceipts = nil
			b.mu.Unlock()
			return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": hook.Event, "additionalContext": strings.TrimSpace(receipt)}})
		default:
			return nil, fmt.Errorf("unsupported Claude hook %q", hook.Event)
		}
	default:
		return nil, fmt.Errorf("unsupported Claude host request %q", method)
	}
}

func (b *claudeBridge) approve(ctx context.Context, identity control.Identity, tool string, args jsontext.Value, decision claude.Decision) (bool, string, error) {
	if !decision.Ask {
		return decision.Allow, decision.Reason, nil
	}
	member, err := b.execution.shared.Caller(identity)
	if err != nil {
		return false, "", err
	}
	source := observation.Thread{ID: member.Thread, Path: member.Task, Role: member.Settings.Role, NonAttachable: member.Settings.NonAttachable}
	choice, reason, err := b.execution.approvals.Request(ctx, source, &prompt.Approval{Thread: identity.Thread, Turn: identity.Turn, Item: identity.Item, Title: "Claude tool approval", Subject: tool,
		Details: []string{decision.Reason, string(args)}, Choices: []prompt.Choice{{ID: 0, Label: "Approve", Approve: true, Outcome: "Approved"}, {ID: 1, Label: "Deny", Deny: true, Outcome: "Denied"}}})
	if err != nil {
		return false, "", err
	}
	return choice.Approve && !choice.Deny, reason, nil
}

func (b *claudeBridge) complete(hook claudeHook) (string, error) {
	b.mu.Lock()
	capture := b.captures[hook.ID]
	if capture == nil {
		b.mu.Unlock()
		return "", nil
	}
	var output struct {
		Background  string `json:"backgroundTaskId"`
		Interrupted bool   `json:"interrupted"`
	}
	_ = json.Unmarshal(hook.Response, &output)
	capture.background = capture.background || output.Background != ""
	background := capture.background
	b.mu.Unlock()
	status, success := "completed", hook.Event == "PostToolUse"
	if !success {
		status = "failed"
	}
	if hook.Interrupted || output.Interrupted {
		status, success = "interrupted", false
	}
	text := string(hook.Response)
	if hook.Event == "PostToolUseFailure" {
		text = hook.Error
	}
	receipt, err := capture.operation.Complete(executionobservation.ClaudeToolOutcome{Terminal: !background, Succeeded: success, Status: status, Output: text})
	if err == nil && !background {
		b.mu.Lock()
		capture.completed = true
		capture.failed = !success
		b.mu.Unlock()
	}
	return receipt, err
}

func (b *claudeBridge) batch(ctx context.Context, hook claudeHook) (jsontext.Value, error) {
	b.mu.Lock()
	succeeded := true
	for _, call := range hook.Calls {
		capture := b.captures[call.ID]
		succeeded = succeeded && capture != nil && !capture.failed && (capture.completed || capture.background)
		if capture != nil && capture.completed {
			delete(b.captures, call.ID)
		}
	}
	b.mu.Unlock()
	var stopped bool
	for _, call := range hook.Calls {
		if call.Tool != "mcp__threadcontrol__wait_agent" {
			continue
		}
		identity, err := b.identity(call.ID)
		if err != nil {
			return nil, err
		}
		var result struct {
			IsError bool `json:"isError"`
		}
		if len(call.Response) == 0 || json.Unmarshal(call.Response, &result) != nil {
			continue
		}
		if err := b.execution.shared.Execution(ctx, identity.Thread, identity.Turn, identity.Item, "", succeeded && !result.IsError); err != nil {
			return nil, err
		}
		stop, err := b.execution.shared.ClaimLocalStop(ctx, identity.Thread, identity.Turn, identity.Item)
		if err != nil {
			return nil, err
		}
		stopped = stop != nil
	}
	if stopped {
		return json.Marshal(map[string]any{"continue": false, "stopReason": "Waiting for the admitted Thread Control outcome"})
	}
	b.mu.Lock()
	receipts := strings.Join(b.pendingReceipts, "\n")
	b.pendingReceipts = nil
	b.mu.Unlock()
	return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": hook.Event, "additionalContext": receipts}})
}

func (b *claudeBridge) event(ctx context.Context, raw jsontext.Value) error {
	var task struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Session string `json:"session_id"`
		ID      string `json:"tool_use_id"`
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		return err
	}
	if task.Session != "" && task.Session != b.info.Session {
		return errors.New("Claude event native session identity mismatch")
	}
	if task.Type == "system" && task.Subtype == "task_notification" && task.ID != "" {
		b.mu.Lock()
		capture := b.captures[task.ID]
		b.mu.Unlock()
		if capture != nil {
			receipt, err := capture.operation.Complete(executionobservation.ClaudeToolOutcome{Terminal: true, Succeeded: task.Status == "completed", Status: task.Status, Output: task.Summary})
			if err != nil {
				receipt = err.Error()
			}
			b.mu.Lock()
			if err == nil {
				delete(b.captures, task.ID)
			}
			if receipt != "" {
				b.pendingReceipts = append(b.pendingReceipts, receipt)
			}
			b.mu.Unlock()
		}
	}
	result, err := b.runtime.HandleEvent(ctx, raw)
	if err != nil {
		return err
	}
	if err := b.runtime.ObserveMetrics(ctx, raw); err != nil {
		b.owners.Notifications.Submit(notice.Input{Session: b.info.Session, Thread: b.info.Thread, Category: "claude-metrics", Text: "Claude metrics could not be retained: " + err.Error()})
	}
	if task.Type == "result" {
		_, report := b.runtime.Metrics()
		if report.Roundtrips > 0 || report.TotalsKnown {
			b.owners.Usage.ObserveCumulative(b.info.Thread, report)
		}
	}
	return b.reduceEvent(ctx, result)
}

func (b *claudeBridge) reduceEvent(ctx context.Context, result sessioncontrol.ClaudeEvent) error {
	identity := control.Identity{Thread: b.info.Thread, Session: b.info.Session, Turn: result.Turn}
	if result.Started {
		if err := b.execution.driver.AdmitRequest(ctx, b.session, identity, false); err != nil {
			return err
		}
	}
	if err := b.execution.shared.InputConsumed(ctx, identity, result.Consumed); err != nil {
		return err
	}
	if result.Completed {
		if result.Answer != "" {
			if err := b.execution.shared.AnswerCompleted(ctx, identity.Thread, identity.Turn, identity.Turn, result.Answer); err != nil {
				return err
			}
		}
		b.execution.driver.Completed(identity.Thread, identity.Turn, result.Status, result.Failure, func(err error) {
			b.runtime.RetireCompleted(identity.Turn, err)
		})
	}
	return nil
}

// close reconciles only after native cleanup has joined. Missing tool hooks
// narrow outcome knowledge, not the observed file evidence.
func (b *claudeBridge) close(client interface{ Close() error }) error {
	// The event pump has stopped. Durable publication remains synchronous, but
	// its former UI queue must not hold cleanup when multiple tools settle.
	if b.stopUpdates != nil {
		b.stopUpdates()
	}
	closeErr := client.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, capture := range b.captures {
		if capture.completed {
			continue
		}
		_, err := capture.operation.Complete(executionobservation.ClaudeToolOutcome{Terminal: true, Unknown: true})
		closeErr = errors.Join(closeErr, err)
		capture.operation.Discard()
	}
	clear(b.captures)
	return closeErr
}

func (b *claudeBridge) discard() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, capture := range b.captures {
		capture.operation.Discard()
	}
	clear(b.captures)
}
