package executionobservation

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func nativeBoundaryID(call, item string) string { return call + ":native-boundary:" + item }

// ObserveNativeBoundary borrows the admitted window's authority. A hook cannot
// enroll a thread or choose a capture workspace. Unavailable observation never
// rejects the native tool or waits for an approval decision.
func (p *Preparer) ObserveNativeBoundary(ctx context.Context, request transport.Boundary) {
	if p.Windows == nil || p.NativeTrace == nil || request.Thread == "" || request.Turn == "" || ctx.Err() != nil {
		return
	}
	state := p.Windows.state()
	state.mu.Lock()
	var candidates []*execWindow
	for _, window := range state.windows {
		if window.thread == request.Thread && window.turn == request.Turn && window.closed.IsZero() && window.source != "" && window.scope.Store != nil {
			candidates = append(candidates, window)
		}
	}
	state.mu.Unlock()
	for _, incoming := range candidates {
		call, ok := p.NativeTrace.ReadTool(request.Thread, request.Turn, request.Item, incoming.ref, incoming.source)
		if !ok {
			continue
		}
		if !call.LocalEnvironment || (request.Tool == "Bash" && (call.Tool != nativeExecCommandToolName || call.Command.Command != request.Input)) || (request.Tool == applyPatchToolName && (call.Tool != applyPatchToolName || call.Input != request.Input)) {
			return
		}
		if request.Tool != "Bash" && request.Tool != applyPatchToolName {
			return
		}
		cuts := p.Windows.intervalCuts([]string{incoming.ref})
		if request.Tool == "Bash" && len(cuts) == 0 {
			return
		}
		var eager mekugiHistory
		eagerPatch := false
		if request.Tool == applyPatchToolName {
			var err error
			eager, _, err = lookupObservation(ctx, incoming.scope, incoming.workspace, incoming.ref)
			if err != nil {
				return
			}
			eagerPatch = slices.ContainsFunc(eager.NativePatches, func(p changecapture.PatchObservation) bool { return p.Input == call.Input })
		}
		directory := request.CWD
		if call.Command.Workdir != "" {
			directory = call.Command.Workdir
		}
		if !filepath.IsAbs(directory) {
			return
		}
		deadline, _ := ctx.Deadline()
		var paths []string
		if request.Tool == applyPatchToolName {
			var err error
			paths, err = changecapture.PatchTargets(call.Input, directory)
			if err != nil {
				return
			}
		} else {
			shell := call.Command.Shell
			if shell == "" {
				shell = incoming.shell
			}
			plan := execscope.Classify(execscope.Input{Command: call.Command.Command, Directory: directory, Shell: shell, Deadline: deadline, Changes: storeChangeResolver(ctx, incoming.scope, p.Changes)})
			for _, entry := range plan.Scope {
				if entry.Kind != execscope.File {
					continue
				}
				for _, operand := range entry.Operands {
					if !operand.Glob {
						paths = append(paths, operand.Path)
					}
				}
			}
		}
		if len(paths) == 0 || ctx.Err() != nil {
			return
		}
		// An earlier command in this same cell keeps the earliest cell baseline.
		// A terminal poll belongs to its original writer, not the incoming cell.
		priorWriter := false
		priorCalls := p.NativeTrace.ReadCell(request.Thread, incoming.ref, incoming.source).Calls()
		for _, prior := range priorCalls {
			if prior.ID == request.Item {
				break
			}
			if prior.Tool == nativeExecCommandToolName && prior.LocalEnvironment {
				workdir, shell := prior.Command.Workdir, prior.Command.Shell
				if workdir == "" {
					workdir = request.CWD
				}
				if shell == "" {
					shell = incoming.shell
				}
				plan := execscope.Classify(execscope.Input{Command: prior.Command.Command, Directory: workdir, Shell: shell, Deadline: deadline, Changes: storeChangeResolver(ctx, incoming.scope, p.Changes)})
				if plan.Class == execscope.Neutral {
					continue
				}
			}
			if prior.Tool == applyPatchToolName || prior.Tool == nativeExecCommandToolName {
				priorWriter = true
			}
		}
		var terminal []execIntervalCut
		var missing []execIntervalCut
		identities := make(changecapture.PathIdentities)
		physical := make(map[string]string, len(paths))
		for _, path := range paths {
			physical[path] = identities.File(path)
		}
		covers := func(writer *execWindow, path string) bool {
			return writer.coversInterval(path) || writer.coversInterval(physical[path])
		}
		ended := make(map[string]bool)
		var shared []string
		if !priorWriter {
			// Dynamic patch targets first become known at this boundary.
			// Reuse ordinary interval splitting for writers absent from the
			// eager pre-cell cut, rather than losing their preceding effect.
			if request.Tool == applyPatchToolName {
				state.mu.Lock()
				for _, writer := range state.windows {
					if writer.ref != incoming.ref && writer.group != incoming.group && writer.closed.IsZero() && writer.scope.Store != nil &&
						!slices.ContainsFunc(cuts, func(c execIntervalCut) bool { return c.writer == writer.ref }) && slices.ContainsFunc(paths, func(path string) bool { return covers(writer, path) }) {
						missing = append(missing, execIntervalCut{writer: writer.ref, workspace: writer.workspace, scope: writer.scope})
					}
				}
				state.mu.Unlock()
			}
			for _, cut := range append(slices.Clone(cuts), missing...) {
				state.mu.Lock()
				writer := p.Windows.find(cut.writer)
				var overlap []string
				if writer != nil {
					for _, path := range paths {
						if covers(writer, path) {
							overlap = append(overlap, path)
						}
					}
				}
				state.mu.Unlock()
				if writer == nil || writer.source == "" {
					continue
				}
				cell := p.NativeTrace.ReadCell(writer.thread, writer.ref, writer.source)
				ended[cut.writer] = cell.Ended() && !cell.Pending()
				newCut := slices.ContainsFunc(missing, func(c execIntervalCut) bool { return c.writer == cut.writer })
				if !ended[cut.writer] && !newCut {
					continue
				}
				if len(overlap) == 0 {
					continue
				}
				if !newCut {
					terminal = append(terminal, cut)
				}
				shared = append(shared, overlap...)
			}
		}
		if request.Tool != applyPatchToolName || eagerPatch {
			paths = shared
		}
		if len(paths) == 0 {
			return
		}
		paths = slices.Compact(slices.Sorted(slices.Values(paths)))
		var refs []string
		refs = append(refs, incoming.ref)
		for _, cut := range terminal {
			refs = append(refs, cut.writer)
		}
		for _, cut := range missing {
			refs = append(refs, cut.writer)
		}
		unlock := p.Windows.lockIntervals(refs)
		defer unlock()
		if ctx.Err() != nil {
			return
		}
		id := nativeBoundaryID(incoming.ref, request.Item)
		if _, found, err := lookupObservation(ctx, incoming.scope, incoming.workspace, id); err != nil || found {
			return
		}
		observed := changecapture.NamedUntil(paths, incoming.workspace, deadline, captureSource(incoming.scope))
		files := observed.Files
		for _, omitted := range observed.Omitted {
			files = append(files, changecapture.File{Path: omitted.Path, Error: omitted.Reason})
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return
		}
		for _, cut := range missing {
			if err := p.splitExecInterval(ctx, ctx, incoming.workspace, incoming.ref, files, cut, ended[cut.writer]); err != nil {
				return
			}
		}
		if priorWriter && !eagerPatch && eager.ExecObservation != nil {
			// The cell's earlier named shell writes are transferred with the
			// patch window. Keep their witnessed before-state before excluding
			// these paths from command reconciliation.
			earliest := eager.ExecObservation
			for _, prior := range priorCalls {
				if prior.ID == request.Item {
					break
				}
				if prior.Tool != nativeExecCommandToolName {
					continue
				}
				boundary, found, err := lookupObservation(ctx, incoming.scope, incoming.workspace, nativeBoundaryID(incoming.ref, prior.ID))
				if err != nil {
					return
				}
				if found && boundary.CorrelationID == incoming.ref+"\x00boundary:"+prior.ID && boundary.ExecObservation != nil {
					earliest.Files = rebaseNativeFiles(earliest.Files, boundary.ExecObservation.Files)
				}
			}
			for i := range files {
				for _, before := range earliest.Files {
					if before.Path == files[i].Path {
						files[i] = before
						break
					}
				}
			}
		}
		baseline := mekugiHistory{ToolName: call.Tool, Script: request.Input, Root: incoming.workspace, ExecutingThread: incoming.thread, CorrelationID: incoming.ref + "\x00boundary:" + request.Item}
		if request.Tool == applyPatchToolName {
			patch, err := changecapture.PatchBoundary(call.Input, directory, files)
			if err != nil {
				return
			}
			baseline.NativePatches = []changecapture.PatchObservation{patch}
			if len(shared) != 0 {
				baseline.ExecObservation = &changecapture.Observation{Excluded: shared}
			}
		} else {
			baseline.ExecObservation = &changecapture.Observation{Files: files}
		}
		// Publication precedes releasing the native pre-tool boundary. Each
		// terminal interval closes only shared paths; other scope remains open.
		var intervals []execIntervalContinuation
		for _, cut := range terminal {
			interval, err := p.prepareTerminalBoundary(ctx, incoming.workspace, cut, files)
			if err != nil {
				return
			}
			if interval != nil {
				intervals = append(intervals, *interval)
			}
		}
		err := incoming.scope.Store.Transaction(ctx, incoming.scope.Writes, func(tx *history.Transaction) error {
			if err := publishCapturedObservation(tx, incoming.scope, incoming.workspace, id, baseline); err != nil {
				return err
			}
			for _, interval := range intervals {
				if err := interval.publish(tx, p.Changes); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			p.notice("", incoming.thread, "capture_boundary", "Native writer boundary could not be retained: "+err.Error())
			return
		}
		state.mu.Lock()
		incoming.paths = slices.Compact(slices.Sorted(slices.Values(append(incoming.paths, paths...))))
		incoming.roots = changecapture.AddRoot(incoming.roots, incoming.workspace)
		state.mu.Unlock()
		return
	}
}

func (p *Preparer) prepareTerminalBoundary(ctx context.Context, workspace string, cut execIntervalCut, endpoints []changecapture.File) (*execIntervalContinuation, error) {
	workspace = cmp.Or(cut.workspace, workspace)
	parts, err := readExecIntervals(ctx, cut.scope, p.Changes, workspace, cut.writer, cut.codeMode)
	if err != nil || len(parts) == maxExecIntervals {
		return nil, err
	}
	if cut.number >= len(parts) {
		return nil, nil
	}
	boundary := parts[cut.number].before
	reviews, files := changecapture.ReviewInterval(boundary.ExecObservation.Files, endpoints, captureSource(cut.scope))
	if len(files) == 0 {
		return nil, nil
	}
	var paths []string
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	// The eager prefix is immutable. This new prefix records the remaining
	// B-to-C effect and closes those terminal-writer paths, even if a later
	// writer restores B before normal host-result reconciliation.
	closed := mekugiHistory{ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: boundary.ExecutingThread, Caller: boundary.Caller, Source: boundary.Source,
		ChangeID: boundary.ChangeID, CorrelationID: boundary.CorrelationID, Attempt: len(parts) + 1, reviewFiles: reviews,
		ExecObservation: &changecapture.Observation{Files: files, Excluded: paths, CodeMode: cut.codeMode}}
	id := execIntervalID(cut.writer, cut.codeMode, len(parts))
	return &execIntervalContinuation{scope: cut.scope, workspace: workspace, call: id, value: closed}, nil
}

// loadNativeBoundaries joins pre-execution evidence with actual native calls.
// Absent trace/baseline evidence never becomes a learned post-edit before-state.
func (p *Preparer) loadNativeBoundaries(ctx context.Context, workspace, callID string, h *mekugiHistory) error {
	if p.Retained.Store == nil {
		return nil
	}
	remaining := slices.Clone(h.NativePatches)
	var patches []changecapture.PatchObservation
	for _, tool := range h.nativeCell.Calls() {
		if tool.Tool != applyPatchToolName && tool.Tool != nativeExecCommandToolName {
			continue
		}
		baseline, found, err := lookupObservation(ctx, p.Retained, workspace, nativeBoundaryID(callID, tool.ID))
		if err != nil {
			return err
		}
		if found && baseline.CorrelationID != callID+"\x00boundary:"+tool.ID {
			found = false
		}
		if tool.Tool == applyPatchToolName {
			if i := slices.IndexFunc(remaining, func(p changecapture.PatchObservation) bool { return p.Input == tool.Input }); i >= 0 {
				patch := remaining[i]
				// Only a witnessed terminal-writer cut supersedes the eager
				// literal baseline; unrelated paths keep their earliest bytes.
				if found && baseline.ExecObservation != nil && len(baseline.NativePatches) == 1 {
					patch.Files = slices.Clone(patch.Files)
					for i, before := range patch.Files {
						for _, actual := range baseline.NativePatches[0].Files {
							if actual.BeforePath != before.BeforePath || actual.AfterPath != before.AfterPath {
								continue
							}
							path := before.BeforePath
							if path == "" {
								path = before.AfterPath
							}
							if slices.Contains(baseline.ExecObservation.Excluded, path) {
								patch.Files[i].Before, patch.Files[i].BeforeRef, patch.Files[i].Exists, patch.Files[i].Error = actual.Before, actual.BeforeRef, actual.Exists, actual.Error
							}
							if before.AfterPath != before.BeforePath && slices.Contains(baseline.ExecObservation.Excluded, before.AfterPath) {
								patch.Files[i].TargetBefore, patch.Files[i].TargetBeforeRef, patch.Files[i].TargetExists, patch.Files[i].TargetError = actual.TargetBefore, actual.TargetBeforeRef, actual.TargetExists, actual.TargetError
							}
						}
					}
				}
				patches = append(patches, patch)
				remaining = slices.Delete(remaining, i, i+1)
			} else if found && len(baseline.NativePatches) == 1 && baseline.NativePatches[0].Input == tool.Input {
				patches = append(patches, baseline.NativePatches[0])
			} else if tool.LocalEnvironment {
				// Actual targets with no witnessed before-state remain incomplete.
				// Never learn a baseline from bytes read after the native edit.
				patch, err := changecapture.PatchBoundary(tool.Input, workspace, nil)
				if err == nil {
					patches = append(patches, patch)
				}
			}
		} else if found && baseline.ExecObservation != nil && h.ExecObservation != nil {
			copy := *h.ExecObservation
			copy.Files = rebaseNativeFiles(copy.Files, baseline.ExecObservation.Files)
			h.ExecObservation = &copy
		}
	}
	h.NativePatches = append(patches, remaining...)
	if h.ExecObservation != nil {
		copy := *h.ExecObservation
		copy.Excluded = slices.Clone(copy.Excluded)
		for _, patch := range h.NativePatches {
			for _, file := range patch.Files {
				if file.BeforePath != "" {
					copy.Excluded = append(copy.Excluded, file.BeforePath)
				}
				if file.AfterPath != "" {
					copy.Excluded = append(copy.Excluded, file.AfterPath)
				}
			}
		}
		h.ExecObservation = &copy
	}
	return nil
}

func rebaseNativeFiles(files []changecapture.File, boundary []changecapture.File) []changecapture.File {
	files = slices.Clone(files)
	for _, file := range boundary {
		for i := range files {
			if files[i].Path == file.Path {
				before := file
				before.Origin, before.AlsoManaged = files[i].Origin, files[i].AlsoManaged
				files[i] = before
			}
		}
	}
	return files
}
