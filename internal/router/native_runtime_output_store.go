package router

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/session"
)

// The native transcript selects calls. This record adds only immutable output
// evidence, not a transcript, process, task controller or live tracker.
type retainedNativeOutput struct {
	Reference string
	TaskID    string
	Failed    bool
}

func (u *appServerUI) runtimeOutputScope(id string) (context.Context, ObservationCall, error) {
	if u.runtime.observations == nil {
		return nil, ObservationCall{}, errors.New("native output storage is unavailable")
	}
	o := u.runtime.observations.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	call := ObservationCall{Binding: ObservationBinding{Runtime: o.runtime, Workspace: u.session.cwd, Session: u.thread}, ID: id, Tool: "Bash"}
	ctx, err := o.callContext(u.ctx, call)
	return ctx, call, err
}

func (u *appServerUI) retainRuntimeCommandOutput(e session.Event) (string, string, error) {
	task, ok := u.runtime.tasks[e.Output.TaskID]
	if !ok || task.Kind != "local_bash" || task.ToolID != e.ID || !e.Output.Done {
		return "", "", errors.New("native output task identity mismatch")
	}
	ctx, call, err := u.runtimeOutputScope(e.ID)
	if err != nil {
		return "", "", err
	}
	store := u.runtime.observations.owner.store
	key := observationKey(call) + "/output"
	prior, found, err := store.lookup(ctx, call.Binding.Workspace, key)
	if err != nil {
		return "", "", err
	}
	if found {
		if prior.NativeOutput == nil || prior.NativeOutput.TaskID != e.Output.TaskID || prior.NativeOutput.Failed != e.Failed {
			return "", "", errors.New("native output receipt changed")
		}
		text, err := store.readOutputChunks(ctx, prior.NativeOutput.Reference)
		return text, prior.NativeOutput.Reference, err
	}
	info, err := os.Lstat(e.Output.OutputFile)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxShellOutputStoreBytes {
		return "", "", errors.New("native output file exceeds managed storage capacity or is not regular")
	}
	file, err := os.Open(e.Output.OutputFile)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxShellOutputStoreBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > maxShellOutputStoreBytes || !utf8.Valid(data) {
		return "", "", errors.New("native output exceeds managed storage capacity or is not UTF-8")
	}
	text := string(data)
	reference, err := store.putOutputChunks(ctx, text)
	if err != nil {
		return "", "", err
	}
	history := mekugiHistory{ToolName: "Bash", Root: call.Binding.Workspace, ExecutingThread: observationThread(call.Binding),
		NativeOutput: &retainedNativeOutput{Reference: reference, TaskID: e.Output.TaskID, Failed: e.Failed}}
	if err := store.put(ctx, call.Binding.Workspace, map[string]mekugiHistory{key: history}); err != nil {
		return "", "", err
	}
	return text, reference, nil
}

func (u *appServerUI) restoreRuntimeCommandOutput(id string) {
	ctx, call, err := u.runtimeOutputScope(strings.TrimPrefix(id, "history/"))
	if err != nil {
		return // No observation capability means no retained native evidence.
	}
	store := u.runtime.observations.owner.store
	history, found, err := store.lookup(ctx, call.Binding.Workspace, observationKey(call)+"/output")
	if err == nil && (!found || history.NativeOutput == nil) {
		return
	}
	var text string
	if err == nil {
		text, err = store.readOutputChunks(ctx, history.NativeOutput.Reference)
	}
	if err != nil {
		u.setNotice("Saved native command output unavailable: "+err.Error(), true)
		return
	}
	o := history.NativeOutput
	u.runtimeCommandOutput(session.Event{Kind: "command_output", ID: id, Text: text, Failed: o.Failed, Historical: true,
		Output: &session.CommandOutput{TaskID: o.TaskID, Reference: o.Reference, Done: true}})
}
