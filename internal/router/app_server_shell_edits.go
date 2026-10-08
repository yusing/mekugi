package router

import (
	"context"
	"crypto/rand"
	jsonv1 "encoding/json"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/internal/appserver"
)

type appServerShellEdit struct {
	ctx                     context.Context
	call, workspace, thread string
	history                 mekugiHistory
}

// Only locally submitted commands own observation windows. History restoration
// never captures a new baseline or revives an unfinished command.
type appServerShellEdits struct {
	shell   string
	pending *appServerShellEdit
	running map[[2]string]*appServerShellEdit
}

func (u *appServerUI) captureShellEdit(command string) *appServerShellEdit {
	if u.proxy == nil || u.proxy.replayStore == nil || u.replay != nil {
		return nil
	}
	ctx := u.session.waitContext
	if ctx == nil {
		ctx = u.ctx
	}
	t := &mekugiResponseTransform{ctx: ctx, proxy: u.proxy, directory: u.session.cwd,
		threadID: u.thread, shellThreadID: u.thread, shellTurnID: u.turn}
	input := execCommandInput{Command: command, Workdir: u.session.cwd, Shell: u.shellEdits.shell}
	observation, observed := t.snapshotExecObservation(captureExecObservation([]execCommandInput{input}, false, false, t.execCaptureEnvironment(nil)))
	if !observed {
		return nil
	}
	call := "shell:" + rand.Text()
	u.restoreDiffThread(appServerThreadInfo{ID: u.thread, Cwd: u.session.cwd}, true)
	t.openExecWindow(call, observation, nil)
	u.proxy.execWindows.setSession(call, call)
	return &appServerShellEdit{ctx: ctx, call: call, workspace: u.session.cwd, thread: u.thread,
		history: mekugiHistory{ToolName: nativeExecCommandToolName, ExecObservation: observation,
			ExecutingThread: u.thread, Caller: u.session.path(u.thread)}}
}

func (u *appServerUI) discardShellEdit(edit *appServerShellEdit) {
	if edit != nil {
		u.proxy.execWindows.close(edit.call)
	}
}

// Capture completion before presentation can discard an event from a thread
// the user has left. The observation retains its original workspace and scope.
func (u *appServerUI) observeShellMessage(message appserver.Message) {
	if message.Method != "item/started" && message.Method != "item/completed" || u.shellEdits.pending == nil && len(u.shellEdits.running) == 0 {
		return
	}
	var event appServerEvent
	if json.Unmarshal(message.Params, &event) == nil {
		u.observeShellEdit(event.ThreadID, message.Method, event.Item)
	}
}

func (u *appServerUI) observeShellEdit(thread, method string, item appServerItem) {
	if item.Type != "commandExecution" || item.Source != "userShell" || item.ID == "" {
		return
	}
	key := [2]string{thread, item.ID}
	if method == "item/started" {
		edit := u.shellEdits.pending
		if edit == nil || edit.thread != thread {
			return
		}
		u.shellEdits.pending = nil
		if u.shellEdits.running == nil {
			u.shellEdits.running = make(map[[2]string]*appServerShellEdit)
		}
		edit.history.UpstreamItem = map[string]jsonv1.RawMessage{"id": mustMarshalJSON(item.ID)}
		u.shellEdits.running[key] = edit
		return
	}
	if method != "item/completed" {
		return
	}
	edit := u.shellEdits.running[key]
	if edit == nil {
		return
	}
	delete(u.shellEdits.running, key)
	output := ""
	if item.AggregatedOutput != nil {
		output = *item.AggregatedOutput
	}
	host := &nativeToolResult{CallID: item.ID, Tool: item.Source, Status: item.Status, ExitCode: item.ExitCode}
	if err := u.proxy.finalizeExecObservations(context.WithoutCancel(edit.ctx), edit.workspace,
		[]execCompletion{{callID: edit.call, history: edit.history, output: mustMarshalJSON(output), host: host}}); err != nil {
		u.discardShellEdit(edit)
		u.setNotice("Shell change capture failed: "+err.Error(), true)
	}
}
