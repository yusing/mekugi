package executionobservation

import (
	"context"
	"crypto/rand"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

// ClaudeToolInput combines admitted host identity with the untouched native input.
// Tool arguments never supply retained namespace or caller authority.
type ClaudeToolInput struct {
	Thread, Turn, Namespace, Workspace, Caller, ToolUseID, ToolName, Shell string
	// Workdir is the authenticated native cwd, separate from retained workspace identity.
	Workdir   string
	Arguments []byte
}

// ClaudeToolOutcome contains host facts. A background acknowledgement is not
// Terminal; interruption can be terminal without an observed exit code.
type ClaudeToolOutcome struct {
	Terminal, Succeeded bool
	// Unknown settles a joined process window without claiming a tool outcome.
	Unknown        bool
	ExitCode       *int
	Output, Status string
}

// ClaudeToolOperation owns one native pre-tool/terminal-tool capture window.
// A joined runtime can complete unfinished windows with an unknown outcome.
type ClaudeToolOperation struct {
	mu                 sync.Mutex
	operation          ShellOperation
	toolUseID          string
	settled, discarded bool
	receipt            string
}

// PrepareClaudeTool observes native inputs before execution and admits their
// capture window. Unsupported tools need no observation and return nil, nil.
func PrepareClaudeTool(ctx context.Context, retained changes.Scope, source changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publish func([]changes.Update), windows *Windows, audience *sessioncontrol.Audience, notice func(string, string, string, string), input ClaudeToolInput) (*ClaudeToolOperation, error) {
	switch input.ToolName {
	case "Bash", "PowerShell", "Edit", "Write", "NotebookEdit":
	default:
		return nil, nil
	}
	if retained.Store == nil || input.Thread == "" || input.Namespace == "" || input.ToolUseID == "" || !filepath.IsAbs(input.Workspace) {
		return nil, errors.New("native tool capture requires admitted retained identity and an absolute workspace")
	}
	workdir := input.Workdir
	if workdir == "" {
		workdir = input.Workspace
	}
	if !filepath.IsAbs(workdir) {
		return nil, errors.New("native tool capture requires an absolute working directory")
	}
	var args struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(input.Arguments, &args); err != nil {
		return nil, fmt.Errorf("decode native %s input: %w", input.ToolName, err)
	}
	ctx = context.WithValue(ctx, storageSessionKey{}, storageSessionIdentity{Thread: input.Thread, Namespace: input.Namespace})
	options := executionCaptureEnvironment(ctx, input.Workspace, retained, source, snapshots, nil)
	var observation *changecapture.Observation
	// The retained input field also carries patch/native source. Keep named
	// edits as their original JSON instead of constructing a shell command.
	script := string(input.Arguments)
	if input.ToolName == "Bash" || input.ToolName == "PowerShell" {
		if args.Command == "" {
			return nil, fmt.Errorf("native %s input has no command", input.ToolName)
		}
		script = args.Command
		shell := input.Shell
		if input.ToolName == "PowerShell" {
			// Preserve the actual interpreter. The capture owner provides bounded
			// workspace comparison for shells whose operands it cannot parse.
			shell = "pwsh"
		}
		observation, _ = changecapture.Capture(ctx, []commandanalysis.Command{{Command: script, Workdir: workdir, Shell: shell}}, false, false, options)
		if observation == nil {
			if input.ToolName == "PowerShell" {
				return nil, errors.New("native PowerShell capture has no available workspace baseline")
			}
			return nil, nil
		}
	} else {
		path := args.FilePath
		field := "file_path"
		if input.ToolName == "NotebookEdit" {
			path, field = args.NotebookPath, "notebook_path"
		}
		if path == "" {
			return nil, fmt.Errorf("native %s input has no %s", input.ToolName, field)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workdir, path)
		}
		observation = changecapture.Named([]string{path}, input.Workspace)
		observation.Labels = []string{input.ToolName}
	}
	call := "claude:" + input.Thread + ":" + input.ToolUseID
	history := mekugiHistory{ToolName: input.ToolName, Script: script, ExecObservation: observation, ExecutingThread: input.Thread, Caller: input.Caller,
		CarrierName: input.ToolName, CarrierPayload: string(input.Arguments), UpstreamItem: map[string]jsonv1.RawMessage{
			"id": responses.MustMarshalJSON(input.ToolUseID), "name": responses.MustMarshalJSON(input.ToolName), "arguments": responses.MustMarshalJSON(string(input.Arguments)),
		}}
	operation := &ClaudeToolOperation{toolUseID: input.ToolUseID, operation: ShellOperation{ctx: ctx, call: call, workspace: input.Workspace, thread: input.Thread, turn: input.Turn,
		history: history, options: options, retained: retained, changeSource: source, snapshots: snapshots, recovered: recovered, publishChanges: publish, windows: windows, audience: audience, notice: notice}}
	windows.markBackground(input.Thread, input.Turn)
	admitExecutionWindow(windows, nil, &execWindow{ref: call, thread: input.Thread, turn: input.Turn, group: rand.Text()}, observation, nil, options, input.Thread, input.Caller)
	windows.setSession(call, call)
	return operation, nil
}

// Complete publishes actual terminal effects before returning a separate receipt.
// It never changes the native result. Repeated delivery reuses durable evidence.
func (o *ClaudeToolOperation) Complete(outcome ClaudeToolOutcome) (string, error) {
	if o == nil {
		return "", nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.discarded {
		return "", errors.New("native tool capture was discarded without a terminal outcome")
	}
	if o.settled || !outcome.Terminal {
		return o.receipt, nil
	}
	operation := &o.operation
	ctx := context.WithoutCancel(operation.ctx)
	if outcome.Unknown {
		outcome.Status, outcome.ExitCode = "", nil
	}
	host := binding.ToolResult{CallID: o.toolUseID, Tool: operation.history.ToolName, Status: outcome.Status, ExitCode: outcome.ExitCode}
	err := finalizeExecObservations(operation.retained, operation.changeSource, operation.snapshots, operation.recovered, operation.publishChanges, operation.windows, operation.audience, operation.notice, ctx, operation.workspace,
		[]execCompletion{{callID: operation.call, history: operation.history, native: true, unknown: outcome.Unknown, host: &host, result: binding.ExecutionResult{Terminal: true, Succeeded: outcome.Succeeded, ExitCode: outcome.ExitCode, Text: outcome.Output}}})
	if err != nil {
		return "", err
	}
	receipt, err := agentEditNotice(ctx, operation.retained, operation.changeSource, operation.workspace, operation.call, operation.history)
	if err != nil {
		return "", err
	}
	o.receipt, o.settled = receipt, true
	return receipt, nil
}

// Discard releases an unfinished window without guessing process status or
// publishing a post-state after its observation lifetime has ended.
func (o *ClaudeToolOperation) Discard() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.operation.Discard()
	if !o.settled {
		o.discarded = true
	}
}
