// Source: apps/mekugi/internal/router/app_server_shell_edits.go@[c6bad96c].
package executionobservation

import (
	"context"
	"crypto/rand"
	jsonv1 "encoding/json"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

type ShellInput struct {
	Thread, Turn, Namespace, Workspace, Caller, Command, Shell string
}

// ShellOperation retains one locally submitted operation's original authority.
// The native session supplies host facts without reading capture working state.
type ShellOperation struct {
	ctx                           context.Context
	call, workspace, thread, turn string
	history                       mekugiHistory
	options                       changecapture.Options
	retained                      changes.Scope
	changeSource                  changes.Service
	snapshots                     *changecapture.Workspaces
	recovered                     *recovery.Service
	publishChanges                func([]changes.Update)
	windows                       *Windows
	audience                      *sessioncontrol.Audience
	notice                        func(string, string, string, string)
}

func PrepareShell(ctx context.Context, retained changes.Scope, source changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publish func([]changes.Update), windows *Windows, audience *sessioncontrol.Audience, notice func(string, string, string, string), input ShellInput) *ShellOperation {
	ctx = context.WithValue(ctx, storageSessionKey{}, storageSessionIdentity{Thread: input.Thread, Namespace: input.Namespace})
	options := executionCaptureEnvironment(ctx, input.Workspace, retained, source, snapshots, nil)
	command := commandanalysis.Command{Command: input.Command, Workdir: input.Workspace, Shell: input.Shell}
	observation, observed := changecapture.Capture(ctx, []commandanalysis.Command{command}, false, false, options)
	if !observed {
		return nil
	}
	return &ShellOperation{ctx: ctx, call: "shell:" + rand.Text(), workspace: input.Workspace, thread: input.Thread, turn: input.Turn,
		history: mekugiHistory{ToolName: nativeExecCommandToolName, ExecObservation: observation, ExecutingThread: input.Thread, Caller: input.Caller},
		options: options, retained: retained, changeSource: source, snapshots: snapshots, recovered: recovered, publishChanges: publish, windows: windows, audience: audience, notice: notice,
	}
}

// Admission follows native review-scope publication, preserving its event order.
func (o *ShellOperation) Begin(review *sessionreview.Review) {
	admitExecutionWindow(o.windows, review, &execWindow{ref: o.call, thread: o.thread, turn: o.turn, group: rand.Text()}, o.history.ExecObservation, nil, o.options, o.thread, "/root")
	o.windows.setSession(o.call, o.call)
}

func (o *ShellOperation) Thread() string { return o.thread }

func (o *ShellOperation) BindItem(item string) {
	o.history.UpstreamItem = map[string]jsonv1.RawMessage{"id": responses.MustMarshalJSON(item)}
}

func (o *ShellOperation) Discard() {
	if o != nil {
		o.windows.close(o.call)
	}
}

func (o *ShellOperation) Complete(host binding.ToolResult, output string) error {
	err := finalizeExecObservations(o.retained, o.changeSource, o.snapshots, o.recovered, o.publishChanges, o.windows, o.audience, o.notice, context.WithoutCancel(o.ctx), o.workspace,
		[]execCompletion{{callID: o.call, history: o.history, result: binding.ExecutionResult{Terminal: true, Succeeded: host.Status == "completed" && host.ExitCode != nil && *host.ExitCode == 0, ExitCode: host.ExitCode, Text: output}, host: &host}})
	if err != nil {
		o.Discard()
	}
	return err
}
