package sessioncontrol

import (
	"context"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (r *Requests[T]) Thread(ctx context.Context, thread string) (control.ThreadInfo, error) {
	raw, err := r.call(ctx, thread, "thread/read", map[string]any{"threadId": thread, "includeTurns": false}, nil)
	var response struct {
		Thread control.ThreadInfo `json:"thread"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &response)
	}
	if err == nil && (response.Thread.Thread != thread || response.Thread.Session == "" || response.Thread.Path == "") {
		err = errors.New("host omitted the admitted thread identity or history source")
	}
	return response.Thread, err
}

func (r *Requests[T]) Configuration(ctx context.Context, thread, workspace string) (jsontext.Value, error) {
	return r.call(ctx, thread, "config/read", map[string]any{"cwd": workspace, "includeLayers": true}, nil)
}

func (r *Requests[T]) Interrupt(ctx context.Context, thread, turn string) error {
	_, err := r.call(ctx, thread, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, nil)
	return err
}

// NativeMessage is the retained host envelope for first-party attributed input.
// Its delivery projection requires the complete generated shape, not printed prose.
type NativeMessage struct {
	Type     string              `json:"type"`
	ID       string              `json:"id"`
	Role     string              `json:"role"`
	Phase    string              `json:"phase"`
	Content  []nativeMessageText `json:"content"`
	Metadata struct {
		Kinds []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

type nativeMessageText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Delivery decodes retained assignments, messages and results. The caller must supply
// an identity-validated host rollout; decoding grants no dispatch authority.
func (item NativeMessage) Delivery() (input control.NativeInput, ok bool) {
	id, owned := strings.CutPrefix(item.ID, "threadcontrol_")
	if _, err := hex.DecodeString(id); !owned || len(id) != 64 || err != nil || len(item.Content) != 1 {
		return input, false
	}
	input, ok = nativeAttributedInput(item.Content[0].Text)
	if !ok {
		return input, false
	}
	expected, _, _ := nativeContent(input)
	expected.ID = item.ID
	if !reflect.DeepEqual(item, expected) {
		return input, false
	}
	return input, true
}

// FollowupDelivery decodes a named client-authored followup_task continuation.
// Its caller validates the retained output's host provenance and owning thread.
func FollowupDelivery(output string) (input control.NativeInput, ok bool) {
	text, ok := strings.CutPrefix(output, "Operation ID: ")
	if !ok {
		return input, false
	}
	id, text, ok := strings.Cut(text, "\n")
	if !ok || id == "" {
		return input, false
	}
	input, ok = nativeAttributedInput(text)
	if !ok || input.Kind != "NEW_TASK" {
		return input, false
	}
	input.ID, input.Run = id, true
	_, _, expected := nativeContent(input)
	return input, output == expected
}

func nativeAttributedInput(text string) (input control.NativeInput, ok bool) {
	text, ok = strings.CutPrefix(text, "Message Type: ")
	if !ok {
		return input, false
	}
	input.Kind, text, ok = strings.Cut(text, "\nTask name: ")
	if !ok || input.Kind != "NEW_TASK" && input.Kind != "MESSAGE" && input.Kind != "FINAL_ANSWER" {
		return input, false
	}
	input.Recipient, text, ok = strings.Cut(text, "\nSender: ")
	if !ok || input.Recipient == "" {
		return input, false
	}
	header, payload, ok := strings.Cut(text, "\nPayload:\n")
	if !ok {
		return input, false
	}
	input.Sender, input.Message = header, payload
	if input.Kind == "FINAL_ANSWER" {
		if sender, fields, structured := strings.Cut(header, "\nOutcome: "); structured {
			outcome, fields, ok := strings.Cut(fields, "\nSource generation: ")
			if !ok || outcome != string(control.Completed) && outcome != string(control.Interrupted) && outcome != string(control.Failed) {
				return input, false
			}
			generation, turn, ok := strings.Cut(fields, "\nSource turn: ")
			parsed, err := strconv.ParseUint(generation, 10, 64)
			if !ok || err != nil || parsed == 0 {
				return input, false
			}
			input.Sender, input.Outcome, input.SourceGeneration, input.ParentTurn = sender, control.Status(outcome), parsed, turn
		}
	}
	if input.Sender == "" || strings.ContainsAny(input.Sender, "\r\n") || strings.ContainsAny(input.ParentTurn, "\r\n") {
		return input, false
	}
	return input, true
}

func nativeCompletionText(input control.NativeInput) string {
	return fmt.Sprintf("Message Type: %s\nTask name: %s\nSender: %s\nOutcome: %s\nSource generation: %d\nSource turn: %s\nPayload:\n%s", input.Kind, input.Recipient, input.Sender, input.Outcome, input.SourceGeneration, input.ParentTurn, input.Message)
}

// nativeContent preserves attribution and stable retained identity for both live
// delivery and subsequent confirmation. No agent text acquires a human role.
// Source: codex-rs/core/src/context/inter_agent_{message,completion_message}.rs
// @0df4aa677.
func nativeContent(input control.NativeInput) (item NativeMessage, name, output string) {
	metadata := "multi_agent.inter_agent_message"
	if input.Kind == "FINAL_ANSWER" {
		metadata = "multi_agent.inter_agent_completion_message"
	}
	output = fmt.Sprintf("Message Type: %s\nTask name: %s\nSender: %s\nPayload:\n%s", input.Kind, input.Recipient, input.Sender, input.Message)
	if input.Kind == "FINAL_ANSWER" && input.Outcome != "" {
		output = nativeCompletionText(input)
	}
	item = NativeMessage{
		Type: "message", ID: control.MessageItemID(input.ID), Role: "assistant", Phase: "commentary",
		Content: []nativeMessageText{{Type: "output_text", Text: output}},
	}
	item.Metadata.Kinds = []string{metadata}
	name = "followup_task"
	if input.Run {
		output = "Operation ID: " + input.ID + "\n" + output
		if input.Settings != nil && input.Message == "" {
			name, output = "spawn_agent", "Assignment retained. Operation ID: "+input.ID
		}
	}
	if input.Kind == "COMPLETION_GUARD" {
		name, output = "completion_guard", "Operation ID: "+input.ID+"\n"+input.Message
	}
	if input.Wake != nil {
		name, output = "wait_agent", input.RetainedWaitOutput()
		if input.Kind == "COMPACT_CONTINUE" {
			name = "compact"
		}
	}
	return
}

func (r *Requests[T]) Input(ctx context.Context, input control.NativeInput) (string, error) {
	if input.Kind == "COMPACT" {
		_, err := r.call(ctx, input.Thread, "thread/compact/start", map[string]any{"threadId": input.Thread}, &input)
		return "", err
	}
	if input.Kind == "HUMAN_INPUT" {
		return "", &binding.DispatchError{Outcome: binding.NotDispatched, Cause: errors.New("human input belongs to the ordinary input request owner")}
	}
	if input.Run {
		if err := r.ConfirmPreparedInput(ctx, input.Thread); err != nil {
			return "", err
		}
	}
	item, name, output := nativeContent(input)
	params := map[string]any{"threadId": input.Thread}
	method := "thread/inject_items"
	if input.Run {
		method = "turn/start"
		params["input"], params["parentTurnId"] = []any{}, input.ParentTurn
		// Source: codex-rs/app-server/src/request_processors/turn_processor.rs:
		// 599:619,663:694@0df4aa677. Retained output starts or steers work.
		params["toolOutput"] = map[string]any{"namespace": "threadcontrol", "name": name, "output": output, "retain": true}
		if s := input.Settings; s != nil {
			params["cwd"], params["runtimeWorkspaceRoots"] = s.Workspace, s.RuntimeRoots
			params["model"], params["effort"], params["summary"] = s.Model, s.Effort, s.Summary
			params["approvalPolicy"], params["approvalsReviewer"] = s.ApprovalPolicy, s.ApprovalsReviewer
			if s.PermissionID != "" {
				params["permissions"] = s.PermissionID
			}
			if s.Tier != nil {
				params["serviceTier"] = *s.Tier
			}
		}
	} else {
		params["items"] = []any{item}
	}
	raw, err := r.call(ctx, input.Thread, method, params, &input)
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err == nil && input.Run {
		err = json.Unmarshal(raw, &result)
		if err != nil || result.Turn.ID == "" {
			err = &binding.DispatchError{Outcome: binding.Uncertain, Method: method, Cause: errors.New("host accepted native input without a confirmed turn identity")}
		}
	}
	return result.Turn.ID, err
}

// ConfirmPreparedInput runs on a request worker before either agent or human
// input may execute a prepared child. The host event loop remains available.
func (r *Requests[T]) ConfirmPreparedInput(ctx context.Context, thread string) error {
	if prepared := r.preparation.Load(); prepared != nil {
		info, err := r.Thread(ctx, thread)
		if err == nil {
			_, err = prepared.Confirm(info)
		}
		if err == nil {
			err = confirmNativeSettings(info.Thread, info.Path, prepared.Settings)
		}
		if err != nil {
			return &binding.DispatchError{Outcome: binding.NotDispatched, Cause: err}
		}
		r.preparation.CompareAndSwap(prepared, nil)
	}
	return nil
}

func (r *Requests[T]) Confirm(ctx context.Context, input control.NativeInput) error {
	if input.Kind == "COMPACT" {
		return errors.New("native compaction dispatch requires exact host settlement; automatic redispatch is disabled")
	}
	info, err := r.Thread(ctx, input.Thread)
	if err != nil {
		return err
	}
	return ConfirmNativeInput(ctx, info.Path, input)
}

// ConfirmNativeInput reads retained host facts without starting a runtime or work.
func ConfirmNativeInput(ctx context.Context, path string, input control.NativeInput) error {
	if input.Kind == "HUMAN_INPUT" {
		return binding.ConfirmUserInput(ctx, input.Thread, path, input.ID)
	}
	if input.Kind == "INTERRUPT" {
		return binding.ConfirmTerminalTurn(ctx, input.Thread, path, input.TargetTurn)
	}
	item, name, output := nativeContent(input)
	if input.Run {
		return binding.ConfirmRetainedOutput(ctx, input.Thread, path, "threadcontrol", name, output)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return binding.ConfirmHistoryInjection(ctx, input.Thread, path, raw)
}

func (p *PreparedThread) parameters() (map[string]any, error) {
	s := p.Settings
	var config map[string]jsontext.Value
	if len(s.Config) != 0 {
		if err := json.Unmarshal(s.Config, &config); err != nil {
			return nil, err
		}
	}
	if config == nil {
		config = make(map[string]jsontext.Value)
	}
	for key, value := range map[string]*string{"model_reasoning_effort": s.Effort, "model_reasoning_summary": s.Summary} {
		if value != nil {
			config[key], _ = json.Marshal(*value)
		}
	}
	params := map[string]any{
		"model": s.Model, "modelProvider": s.Provider, "cwd": s.Workspace,
		"runtimeWorkspaceRoots": s.RuntimeRoots, "approvalPolicy": s.ApprovalPolicy,
		"approvalsReviewer": s.ApprovalsReviewer,
		"baseInstructions":  s.BaseInstructions, "developerInstructions": s.DeveloperInstructions,
		"config": config, "experimentalRawEvents": true,
	}
	if s.Tier != nil {
		params["serviceTier"] = *s.Tier
	}
	if s.PermissionID != "" {
		params["permissions"] = s.PermissionID
	} else {
		var policy struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(s.PermissionProfile, &policy); err != nil {
			return nil, err
		}
		if policy.Type != "disabled" {
			return nil, errors.New("captured permission profile has no host-selectable identity")
		}
		params["sandbox"] = "danger-full-access"
	}
	return params, nil
}

// Confirm requires independent identity. Retaining the assignment checkpoints
// canonical authority, which Input confirms before starting execution.
func (p *PreparedThread) Confirm(info control.ThreadInfo) (control.Receipt, error) {
	receipt := control.Receipt{Thread: info.Thread, Session: info.Session, Path: info.Path, Uncertain: true}
	if retained := p.Resume; retained != nil {
		if info.Thread != retained.Thread || info.Session != retained.Session || info.Path != retained.Path || info.Parent != nil {
			return receipt, errors.New("host did not confirm the retained independent thread identity")
		}
		receipt.Uncertain = false
		return receipt, nil
	}
	lineage := info.Fork == nil
	if p.Import != "" {
		lineage = info.Fork != nil && *info.Fork == p.Parent
	}
	if info.Thread == "" || info.Session == "" || info.Path == "" || info.Thread == p.Parent || info.Parent != nil || !lineage {
		return receipt, errors.New("host did not confirm a new independent thread identity")
	}
	receipt.Uncertain = false
	return receipt, nil
}

func confirmNativeSettings(thread, path string, s control.Settings) error {
	confirmed, err := binding.ReadAdmissionSettings(thread, path)
	if err != nil {
		return err
	}
	want := binding.AdmissionSettings{
		Model: s.Model, Provider: s.Provider, Workspace: s.Workspace, RuntimeRoots: s.RuntimeRoots,
		Effort: s.Effort, Summary: s.Summary, Tier: s.Tier,
		PermissionProfile: s.PermissionProfile, ApprovalPolicy: s.ApprovalPolicy, ApprovalsReviewer: s.ApprovalsReviewer,
	}
	left, _ := json.Marshal(confirmed)
	right, _ := json.Marshal(want)
	if !sameNativeJSON(left, right) {
		return errors.New("host canonical child authority conflicts with frozen preparation; execution has not started")
	}
	return nil
}

func sameNativeJSON(left, right jsontext.Value) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}
