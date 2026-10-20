package executionobservation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/legacycall"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const maxExecIntervals = 64

type execIntervalCut struct {
	writer   string
	number   int
	codeMode bool
	scope    changes.Scope
}

type execInterval struct {
	call, resume string
	before       mekugiHistory
	after        *changecapture.Observation
}

func execIntervalID(call string, codeMode bool, number int) string {
	return fmt.Sprintf("%s:interval:%d", legacycall.ExecCallID(call, codeMode), number)
}

// Intervals are immutable observations under the original writer's correlation.
// Membership is deferred until that writer's real terminal result. Their earlier
// capture order still places the prefix before the independently completed patch.
func readExecIntervals(ctx context.Context, selected changes.Scope, source changes.Service, workspace, call string, codeMode bool) ([]execInterval, error) {
	if selected.Store == nil {
		return nil, nil
	}
	index, err := source.Inspect(selected, workspace)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]string)
	for changeID, change := range index.Changes {
		if change.Correlation != call+"\x00exec" {
			continue
		}
		for _, id := range change.Dependencies {
			if !strings.HasSuffix(id, ":resume") {
				expected[id] = changeID
			}
		}
	}
	if len(expected) > maxExecIntervals {
		return nil, errors.New("named capture interval limit exceeded")
	}
	var intervals []execInterval
	bytes := 0
	for n := range len(expected) {
		id := execIntervalID(call, codeMode, n)
		if expected[id] == "" {
			return nil, errors.New("invalid named capture interval membership")
		}
		before, found, err := selected.Store.LookupHostCall(ctx, workspace, id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("named capture interval missing")
		}
		if before.CorrelationID != call+"\x00exec" || before.ExecObservation == nil || before.ChangeID != expected[id] {
			return nil, errors.New("invalid named capture interval")
		}
		resume := id + ":resume"
		after, resumed, err := selected.Store.LookupHostCall(ctx, workspace, resume)
		if err != nil {
			return nil, err
		}
		if resumed && (after.CorrelationID != before.CorrelationID || after.ChangeID != before.ChangeID || after.ExecObservation == nil) {
			return nil, errors.New("invalid named capture continuation")
		}
		value := execInterval{call: id, resume: resume, before: legacyHistory(before)}
		if resumed {
			value.after = legacyPointer(after.ExecObservation, legacyObservation)
		}
		for _, file := range before.ReviewFiles {
			bytes += len(file.Diff)
		}
		for _, observation := range []*changecapture.Observation{value.before.ExecObservation, value.after} {
			if observation != nil {
				for _, file := range observation.Files {
					bytes += len(file.Content)
				}
			}
		}
		if bytes > changes.MaxReadBytes {
			return nil, errors.New("named capture intervals exceed retained read bound")
		}
		intervals = append(intervals, value)
	}
	return intervals, nil
}

func rebaseExecIntervals(observation *changecapture.Observation, intervals []execInterval) *changecapture.Observation {
	copy := *observation
	copy.Files = slices.Clone(observation.Files)
	for _, interval := range intervals {
		for _, boundary := range interval.before.ExecObservation.Files {
			for i, file := range copy.Files {
				if file.Path != boundary.Path {
					continue
				}
				next := boundary
				next.Error = "overlapping patch endpoint unavailable"
				if interval.after != nil {
					for _, endpoint := range interval.after.Files {
						if endpoint.Path == file.Path {
							next = endpoint
						}
					}
				}
				next.Origin, next.AlsoManaged = file.Origin, file.AlsoManaged
				copy.Files[i] = next
			}
		}
	}
	if len(intervals) == maxExecIntervals {
		for i := range copy.Files {
			copy.Files[i].Error = "named capture interval limit exceeded"
		}
	}
	return &copy
}

func (t *mekugiResponseTransform) splitExecIntervals(patchCall string, patches []changecapture.PatchObservation) error {
	if len(patches) == 0 || t.execWindows == nil || t.retained.Store == nil {
		return nil
	}
	var boundaries []changecapture.File
	for _, files := range changecapture.PatchWindowFiles(patches) {
		for _, file := range files {
			add := func(path, content string, exists bool, reason string) {
				if path == "" {
					return
				}
				boundary := changecapture.File{Path: path, Content: content, Size: int64(len(content)), Error: reason}
				if exists {
					boundary.Kind = changecapture.Text
				}
				boundaries = append(boundaries, boundary)
			}
			add(file.BeforePath, file.Before, file.Exists, file.Error)
			if file.AfterPath != file.BeforePath {
				add(file.AfterPath, file.TargetBefore, file.TargetExists, file.TargetError)
			}
		}
	}
	var candidates []execIntervalCut
	t.execWindows.mu.Lock()
	for _, window := range t.execWindows.windows {
		if window.ref != patchCall && window.group != t.execGroup && window.closed.IsZero() && window.scope.Store != nil {
			candidates = append(candidates, execIntervalCut{writer: window.ref, scope: window.scope})
		}
	}
	t.execWindows.mu.Unlock()
	for _, cut := range candidates {
		unlock := t.execWindows.lockIntervals([]string{cut.writer})
		err := t.splitExecInterval(patchCall, boundaries, cut)
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *mekugiResponseTransform) splitExecInterval(patchCall string, boundaries []changecapture.File, cut execIntervalCut) error {
	t.execWindows.mu.Lock()
	window := t.execWindows.find(cut.writer)
	active := window != nil && window.closed.IsZero()
	t.execWindows.mu.Unlock()
	if !active {
		return nil
	}
	original, found, err := cut.scope.Store.LookupHostCall(t.ctx, t.directory, cut.writer)
	if err != nil {
		return err
	}
	if !found || original.ExecObservation == nil {
		return nil
	}
	cut.codeMode = original.ExecObservation.CodeMode
	intervals, err := readExecIntervals(t.ctx, cut.scope, t.changeSource, t.directory, cut.writer, cut.codeMode)
	if err != nil {
		return err
	}
	cut.number = len(intervals)
	if cut.number == maxExecIntervals {
		return nil
	}
	observation := rebaseExecIntervals(legacyPointer(original.ExecObservation, legacyObservation), intervals)
	reviews, files := changecapture.ReviewInterval(observation.Files, boundaries)
	if len(files) == 0 {
		return nil
	}
	correlation := cut.writer + "\x00exec"
	id, err := t.changeSource.Reserve(t.ctx, cut.scope, t.directory, original.ExecutingThread, correlation)
	if err != nil {
		return err
	}
	prefix := mekugiHistory{ToolName: nativeExecCommandToolName, Script: execObservationScript(*observation), Root: t.directory,
		ExecutingThread: original.ExecutingThread, Caller: original.Caller, Source: observation.SourceLabel(),
		ChangeID: id, CorrelationID: correlation, Attempt: cut.number + 1, ReviewFiles: reviews,
		ExecObservation: &changecapture.Observation{Files: files, CodeMode: cut.codeMode}}
	if err := cut.scope.Store.Transaction(t.ctx, cut.scope.Writes, func(tx *history.Transaction) error {
		callID := execIntervalID(cut.writer, cut.codeMode, cut.number)
		if err := publishObservation(tx, cut.scope, legacycall.Record{Version: 1, Workspace: t.directory, CallID: callID, History: retainedHistory(prefix)}); err != nil {
			return err
		}
		_, err := t.changeSource.Publish(tx, cut.scope, t.directory, map[string]changes.Publication{callID: {ChangeID: id, CorrelationID: correlation, Dependency: true}})
		return err
	}); err != nil {
		return err
	}
	t.execWindows.mu.Lock()
	if window := t.execWindows.find(patchCall); window != nil {
		window.cuts = append(window.cuts, cut)
	}
	t.execWindows.mu.Unlock()
	return nil
}

func (p *Preparer) resumeExecIntervals(ctx context.Context, workspace, patchCall string, endpoints []changecapture.File) error {
	if p.execWindows == nil || len(endpoints) == 0 {
		return nil
	}
	p.execWindows.mu.Lock()
	var cuts []execIntervalCut
	if window := p.execWindows.find(patchCall); window != nil {
		cuts = slices.Clone(window.cuts)
	}
	p.execWindows.mu.Unlock()
	for _, cut := range cuts {
		unlock := p.execWindows.lockIntervals([]string{cut.writer})
		err := resumeExecInterval(ctx, p.changeSource, workspace, cut, endpoints)
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func resumeExecInterval(ctx context.Context, source changes.Service, workspace string, cut execIntervalCut, endpoints []changecapture.File) error {
	id := execIntervalID(cut.writer, cut.codeMode, cut.number)
	prefix, found, err := cut.scope.Store.LookupHostCall(ctx, workspace, id)
	if err != nil {
		return err
	}
	if !found || prefix.ExecObservation == nil {
		return errors.New("named capture boundary missing")
	}
	if _, resumed, err := cut.scope.Store.LookupHostCall(ctx, workspace, id+":resume"); err != nil {
		return err
	} else if resumed {
		return nil
	}
	var files []changecapture.File
	budget := changecapture.MaxContentBytes
	for _, boundary := range prefix.ExecObservation.Files {
		for _, endpoint := range endpoints {
			if boundary.Path == endpoint.Path {
				if len(endpoint.Content) > budget {
					endpoint.Content, endpoint.Error = "", "overlapping patch endpoint exceeds capture bound"
				} else {
					budget -= len(endpoint.Content)
				}
				files = append(files, endpoint)
			}
		}
	}
	resume := mekugiHistory{ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: prefix.ExecutingThread,
		ChangeID: prefix.ChangeID, CorrelationID: prefix.CorrelationID, Attempt: prefix.Attempt,
		ExecObservation: &changecapture.Observation{Files: files, CodeMode: cut.codeMode}}
	if err := cut.scope.Store.Transaction(ctx, cut.scope.Writes, func(tx *history.Transaction) error {
		if err := publishObservation(tx, cut.scope, legacycall.Record{Version: 1, Workspace: workspace, CallID: id + ":resume", History: retainedHistory(resume)}); err != nil {
			return err
		}
		_, err := source.Publish(tx, cut.scope, workspace, map[string]changes.Publication{id + ":resume": {ChangeID: prefix.ChangeID, CorrelationID: prefix.CorrelationID, Dependency: true}})
		return err
	}); err != nil {
		return err
	}
	return nil
}
