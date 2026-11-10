package executionobservation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/legacycall"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const maxExecIntervals = 64

type execIntervalCut struct {
	writer    string
	workspace string
	number    int
	codeMode  bool
	scope     changes.Scope
}

type execInterval struct {
	call, resume string
	before       mekugiHistory
	after        *changecapture.Observation
}

func execIntervalID(call string, codeMode bool, number int) string {
	return fmt.Sprintf("%s:interval:%d", legacycall.ExecCallID(call, codeMode), number)
}

// An intervening edit publishes the interrupted writer's continuation endpoint.
// When both results first arrive together, host input order can put the yielded
// writer first. Finish the edit before reconciling that writer, keeping sibling
// groups together just as ordinary completion does.
func orderExecCompletions(windows *Windows, members []execCompletion) []execCompletion {
	if windows == nil || len(members) < 2 {
		return members
	}
	// Most requests contain only already-completed history. Scan the bounded
	// window registry once, without allocating per retained historical call.
	type interruption struct{ writer, edit string }
	var cuts []interruption
	windows.state().mu.Lock()
	for _, window := range windows.state().windows {
		for _, cut := range window.cuts {
			cuts = append(cuts, interruption{writer: cut.writer, edit: window.ref})
		}
	}
	windows.state().mu.Unlock()
	if len(cuts) == 0 {
		return members
	}
	groups := make(map[string][]execCompletion)
	callGroups := make(map[string]string)
	var keys []string
	for _, member := range members {
		key := "group:" + execSiblingKey(member.history)
		if key == "group:" {
			key = "call:" + member.callID
		}
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], member)
		callGroups[member.callID] = key
	}
	dependencies := make(map[string][]string)
	for _, cut := range cuts {
		edit, ready := callGroups[cut.edit]
		if writer, writerReady := callGroups[cut.writer]; ready && writerReady && writer != edit {
			dependencies[writer] = append(dependencies[writer], edit)
		}
	}
	if len(dependencies) == 0 {
		return members
	}
	var ordered []execCompletion
	state := make(map[string]uint8)
	var visit func(string) bool
	visit = func(key string) bool {
		if state[key] != 0 {
			return state[key] == 2
		}
		state[key] = 1
		for _, dependency := range dependencies[key] {
			if !visit(dependency) {
				return false
			}
		}
		state[key] = 2
		ordered = append(ordered, groups[key]...)
		return true
	}
	for _, key := range keys {
		if !visit(key) {
			// Conflicting group order cannot establish an endpoint. Preserve
			// the original incomplete-evidence behavior rather than invent it.
			return members
		}
	}
	return ordered
}

// Intervals are immutable observations under the original writer's correlation.
// Membership is deferred until that writer's real terminal result. Their earlier
// capture order still places the prefix before the independently completed edit.
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
		before, found, err := lookupObservation(ctx, selected, workspace, id)
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
		after, resumed, err := lookupObservation(ctx, selected, workspace, resume)
		if err != nil {
			return nil, err
		}
		if resumed && (after.CorrelationID != before.CorrelationID || after.ChangeID != before.ChangeID || after.ExecObservation == nil) {
			return nil, errors.New("invalid named capture continuation")
		}
		value := execInterval{call: id, resume: resume, before: before}
		if resumed {
			value.after = after.ExecObservation
		}
		for _, file := range before.reviews() {
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
		copy.Excluded = append(slices.Clone(copy.Excluded), interval.before.ExecObservation.Excluded...)
		for _, boundary := range interval.before.ExecObservation.Files {
			// Checkpoint-backed paths become named only at this boundary. Their
			// retained continuation, not the old tree, owns subsequent comparison.
			if !slices.ContainsFunc(copy.Files, func(file changecapture.File) bool { return file.Path == boundary.Path }) {
				copy.Files = append(copy.Files, boundary)
			}
			for i, file := range copy.Files {
				if file.Path != boundary.Path {
					continue
				}
				next := boundary
				next.Error = "overlapping writer endpoint unavailable"
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

func (t *mekugiResponseTransform) splitExecIntervals(patchCall string, observation *changecapture.Observation, patches []changecapture.PatchObservation) error {
	if t.owners.Windows == nil || t.owners.Retained.Store == nil {
		return nil
	}
	var boundaries []changecapture.File
	if observation != nil {
		boundaries = append(boundaries, observation.Files...)
	}
	for _, files := range changecapture.PatchWindowFiles(patches) {
		for _, file := range files {
			add := func(path, content string, ref *evidence.Text, exists bool, reason string) {
				if path == "" {
					return
				}
				boundary := changecapture.File{Path: path, Content: content, ContentRef: ref, Size: int64(len(content)), Error: reason}
				if exists {
					boundary.Kind = changecapture.Text
				}
				boundaries = append(boundaries, boundary)
			}
			add(file.BeforePath, file.Before, file.BeforeRef, file.Exists, file.Error)
			if file.AfterPath != file.BeforePath {
				add(file.AfterPath, file.TargetBefore, file.TargetBeforeRef, file.TargetExists, file.TargetError)
			}
		}
	}
	if len(boundaries) == 0 {
		return nil
	}
	identities := make(changecapture.PathIdentities)
	physical := make(map[string]string, len(boundaries))
	for _, boundary := range boundaries {
		physical[boundary.Path] = identities.File(boundary.Path)
	}
	var candidates []execIntervalCut
	t.owners.Windows.state().mu.Lock()
	for _, window := range t.owners.Windows.state().windows {
		if window.ref != patchCall && window.group != t.execGroup && window.closed.IsZero() && window.scope.Store != nil && slices.ContainsFunc(boundaries, func(file changecapture.File) bool {
			return window.coversInterval(file.Path) || window.coversInterval(physical[file.Path])
		}) {
			candidates = append(candidates, execIntervalCut{writer: window.ref, workspace: window.workspace, scope: window.scope})
		}
	}
	t.owners.Windows.state().mu.Unlock()
	acquisition, cancel := context.WithTimeout(t.ctx, segmenttransport.BoundaryHold)
	defer cancel()
	for _, cut := range candidates {
		unlock := t.owners.Windows.lockIntervals([]string{cut.writer})
		err := t.owners.splitExecInterval(t.ctx, acquisition, t.directory, patchCall, boundaries, cut, false)
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Preparer) splitExecInterval(ctx, acquisition context.Context, workspace, patchCall string, boundaries []changecapture.File, cut execIntervalCut, terminal bool) error {
	p.Windows.state().mu.Lock()
	window := p.Windows.find(cut.writer)
	active := window != nil && window.closed.IsZero()
	fallback := active && !window.background
	if active {
		// Physical scope can overlap another authored workspace. Retained
		// identity always remains the interrupted writer's, not the edit's.
		workspace = cmp.Or(cut.workspace, window.workspace, workspace)
		cut.workspace = workspace
	}
	p.Windows.state().mu.Unlock()
	if !active {
		return nil
	}
	original, found, err := lookupObservation(ctx, cut.scope, workspace, cut.writer)
	if err != nil {
		return err
	}
	if !found || original.ExecObservation == nil {
		return nil
	}
	cut.codeMode = original.ExecObservation.CodeMode
	intervals, err := readExecIntervals(ctx, cut.scope, p.Changes, workspace, cut.writer, cut.codeMode)
	if err != nil {
		return err
	}
	cut.number = len(intervals)
	if cut.number == maxExecIntervals {
		return nil
	}
	observation := rebaseExecIntervals(original.ExecObservation, intervals)
	// Same-cell patches own these paths. Cutting the command's excluded
	// baseline would record those already attributed patch effects again.
	observation.Files = slices.DeleteFunc(observation.Files, func(file changecapture.File) bool {
		return slices.Contains(observation.Excluded, file.Path)
	})
	var claimed changecapture.Claims
	if fallback && p.Checkpoints != nil && slices.ContainsFunc(boundaries, func(boundary changecapture.File) bool {
		return !slices.ContainsFunc(original.ExecObservation.Files, func(file changecapture.File) bool { return file.Path == boundary.Path })
	}) {
		// Prefix publication participates in the same first-finisher claims as
		// terminal fallback, including concurrently interrupted opaque writers.
		var claims changecapture.Claims
		unlock, err := p.Windows.serializeContext(acquisition, workspace, observation.Roots...)
		if err == nil {
			defer unlock()
			p.Windows.state().mu.Lock()
			claims = maps.Clone(window.claims)
			p.Windows.state().mu.Unlock()
		}
		// Expired acquisition retains explicit incomplete before-state without
		// waiting on the busy workspace or claiming another writer's effect.
		observation.Files, claimed = p.Checkpoints.IntervalBefore(acquisition, workspace, *observation, original.ExecObservation.Files, boundaries, claims, captureSource(cut.scope))
	}
	reviews, files := changecapture.ReviewInterval(observation.Files, boundaries, captureSource(cut.scope))
	if len(files) == 0 {
		return nil
	}
	correlation := cut.writer + "\x00exec"
	id, err := p.Changes.Reserve(ctx, cut.scope, workspace, original.ExecutingThread, correlation)
	if err != nil {
		return err
	}
	prefix := mekugiHistory{legacy: original.legacy, ToolName: nativeExecCommandToolName, Script: execObservationScript(*observation), Root: workspace,
		ExecutingThread: original.ExecutingThread, Caller: original.Caller, Source: observation.SourceLabel(),
		ChangeID: id, CorrelationID: correlation, Attempt: cut.number + 1, reviewFiles: reviews,
		ExecObservation: &changecapture.Observation{Files: files, CodeMode: cut.codeMode}}
	if terminal {
		for _, file := range files {
			prefix.ExecObservation.Excluded = append(prefix.ExecObservation.Excluded, file.Path)
		}
	}
	if err := cut.scope.Store.Transaction(ctx, cut.scope.Writes, func(tx *history.Transaction) error {
		callID := execIntervalID(cut.writer, cut.codeMode, cut.number)
		if err := publishCapturedObservation(tx, cut.scope, workspace, callID, prefix); err != nil {
			return err
		}
		_, err := p.Changes.Publish(tx, cut.scope, workspace, map[string]changes.Publication{callID: {ChangeID: id, CorrelationID: correlation, Dependency: true}})
		return err
	}); err != nil {
		return err
	}
	p.Windows.claim([]string{cut.writer}, claimed)
	p.Windows.state().mu.Lock()
	if window := p.Windows.find(patchCall); window != nil && !terminal {
		window.cuts = append(window.cuts, cut)
	}
	p.Windows.state().mu.Unlock()
	return nil
}

func (w *Windows) forgetIntervalClaims(cuts []execIntervalCut, endpoints []changecapture.File) {
	if w == nil || len(cuts) == 0 {
		return
	}
	w.state().mu.Lock()
	defer w.state().mu.Unlock()
	for _, cut := range cuts {
		if window := w.find(cut.writer); window != nil {
			window.claims.Forget(endpoints)
		}
	}
}

type execIntervalContinuation struct {
	scope           changes.Scope
	workspace, call string
	value           mekugiHistory
}

func prepareExecIntervalContinuations(ctx context.Context, workspace string, cuts []execIntervalCut, endpoints []changecapture.File) ([]execIntervalContinuation, error) {
	if len(endpoints) == 0 {
		return nil, nil
	}
	var continuations []execIntervalContinuation
	for _, cut := range cuts {
		continuation, err := prepareExecIntervalContinuation(ctx, workspace, cut, endpoints)
		if err != nil {
			return nil, err
		}
		if continuation != nil {
			continuations = append(continuations, *continuation)
		}
	}
	return continuations, nil
}

func prepareExecIntervalContinuation(ctx context.Context, workspace string, cut execIntervalCut, endpoints []changecapture.File) (*execIntervalContinuation, error) {
	workspace = cmp.Or(cut.workspace, workspace)
	id := execIntervalID(cut.writer, cut.codeMode, cut.number)
	prefix, found, err := lookupObservation(ctx, cut.scope, workspace, id)
	if err != nil {
		return nil, err
	}
	if !found || prefix.ExecObservation == nil {
		return nil, errors.New("named capture boundary missing")
	}
	if _, resumed, err := lookupObservation(ctx, cut.scope, workspace, id+":resume"); err != nil {
		return nil, err
	} else if resumed {
		return nil, nil
	}
	var files []changecapture.File
	budget := changecapture.MaxContentBytes
	for _, boundary := range prefix.ExecObservation.Files {
		for _, endpoint := range endpoints {
			if boundary.Path == endpoint.Path {
				if len(endpoint.Content) > budget {
					endpoint.Content, endpoint.Error = "", "overlapping writer endpoint exceeds capture bound"
				} else {
					budget -= len(endpoint.Content)
				}
				files = append(files, endpoint)
			}
		}
	}
	resume := mekugiHistory{legacy: prefix.legacy, ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: prefix.ExecutingThread,
		ChangeID: prefix.ChangeID, CorrelationID: prefix.CorrelationID, Attempt: prefix.Attempt,
		ExecObservation: &changecapture.Observation{Files: files, CodeMode: cut.codeMode}}
	return &execIntervalContinuation{scope: cut.scope, workspace: workspace, call: id + ":resume", value: resume}, nil
}

func (c execIntervalContinuation) publish(tx *history.Transaction, source changes.Service) error {
	if err := publishCapturedObservation(tx, c.scope, c.workspace, c.call, c.value); err != nil {
		return err
	}
	_, err := source.Publish(tx, c.scope, c.workspace, map[string]changes.Publication{c.call: {
		ChangeID: c.value.ChangeID, CorrelationID: c.value.CorrelationID, Dependency: true,
	}})
	return err
}
