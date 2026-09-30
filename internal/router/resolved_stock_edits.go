package router

import (
	"cmp"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// This inventory is a bounded pre-cell baseline, not a workspace diff or an
// ownership sweep. Only paths named by resolved native calls may consume it.
// In particular, another agent's writes to unrelated files are never compared.
type resolvedStockBaseline struct {
	Root    string
	Shell   string
	Files   []execFileSnapshot
	Omitted []execOmission
}

func boundResolvedStockBaseline(history *mekugiHistory, workspace, callID string) {
	baseline := history.ResolvedBaseline
	if baseline == nil {
		return
	}
	record := replayRecord{Version: 1, Workspace: workspace, CallID: callID, History: *history}
	if len(mustMarshalJSON(record)) <= maxReplayRecordBytes {
		return
	}
	// The inventory must not push a supported stock carrier over the store's
	// bound and block host execution. Preserve its named omission instead.
	history.ResolvedBaseline = &resolvedStockBaseline{Root: baseline.Root, Shell: baseline.Shell,
		Omitted: []execOmission{{Path: baseline.Root, Reason: "combined stock observation exceeds the record bound"}}}
}

func stockDynamicPatchInputs(source string, literalCount int) bool {
	if !strings.Contains(source, "apply_patch") {
		return false
	}
	if len(source) > maxMekugiScriptBytes {
		return true
	}
	data := []byte(source)
	tree, err := parseSourceTree(data, codeModeJavaScriptLanguage, nil)
	if err != nil || tree == nil {
		return true
	}
	defer tree.Close()
	count := 0
	stack := []*sitter.Node{tree.RootNode()}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if toolActivityMemberPath(node, data, "tools", applyPatchToolName) {
			count++
		}
		for i := range node.NamedChildCount() {
			stack = append(stack, node.NamedChild(uint(i)))
		}
	}
	return count > literalCount
}

func captureResolvedBaseline(workspace, shell string) *resolvedStockBaseline {
	baseline := &resolvedStockBaseline{Root: workspace, Shell: shell}
	gap := func(reason string) *resolvedStockBaseline {
		baseline.Omitted = []execOmission{{Path: workspace, Reason: reason}}
		return baseline
	}
	if !filepath.IsAbs(workspace) {
		return gap("resolved edit baseline requires a selected workspace")
	}
	deadline := time.Now().Add(execCaptureHold)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	done := make(chan *resolvedStockBaseline, 1)
	select {
	case execCaptureSlots <- struct{}{}:
		go func() {
			defer func() { <-execCaptureSlots }()
			capture := newExecCapture(deadline)
			visited := 0
			_ = filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					capture.omit(path, err.Error())
					return nil
				}
				visited++
				if time.Now().After(deadline) || visited > maxExecListingEntries {
					capture.omit(workspace, "resolved edit baseline enumeration reached its bound")
					return filepath.SkipAll
				}
				if entry.IsDir() && path != workspace && (slices.Contains([]string{".git", ".hg", ".svn", ".jj"}, entry.Name()) || execBuiltinPruned(path, entry.Name())) {
					capture.omit(path, "resolved edit baseline excludes metadata and dependency trees")
					return filepath.SkipDir
				}
				// Keep enumerating after the byte bound: a complete inventory can
				// still prove absence of a new path, but not an unread file's content.
				capture.add(path)
				return nil
			})
			bounded := &execObservation{Files: capture.files, Omitted: capture.omitted}
			boundExecObservation(bounded)
			done <- &resolvedStockBaseline{Root: workspace, Shell: shell, Files: bounded.Files, Omitted: bounded.Omitted}
		}()
	case <-timer.C:
		return gap("resolved edit baseline capture deadline")
	}
	select {
	case result := <-done:
		if time.Now().Before(deadline) {
			return result
		}
	case <-timer.C:
	}
	return gap("resolved edit baseline capture deadline")
}

func (b *resolvedStockBaseline) file(path string) execFileSnapshot {
	path = filepath.Clean(path)
	for _, file := range b.Files {
		if file.Path == path {
			return file
		}
		if file.Kind == execFileSymlink && execPathWithin(path, file.Path) {
			return execFileSnapshot{Path: path, Error: "resolved edit baseline does not follow symlink directories"}
		}
	}
	for _, omission := range b.Omitted {
		if execPathWithin(path, omission.Path) {
			return execFileSnapshot{Path: path, Error: omission.Reason}
		}
	}
	if !filepath.IsAbs(b.Root) || !execPathWithin(path, b.Root) {
		return execFileSnapshot{Path: path, Error: "resolved path is outside the pre-cell baseline"}
	}
	return execFileSnapshot{Path: path} // Complete enumeration proves absence.
}

// Resolved inputs come from host tracing, never from evaluating JavaScript.
// They share the original cell window, not an invented per-nested-call window.
func resolveStockEdits(history *mekugiHistory, workspace string) {
	b := history.ResolvedBaseline
	if b == nil {
		return
	}
	// The pre-cell carrier is immutable replay evidence. Resolve on a copy;
	// completed derived records retain the actual inputs and comparisons.
	history.NativePatches = slices.Clone(history.NativePatches)
	if original := history.ExecObservation; original != nil {
		copy := *original
		copy.Commands = slices.Clone(original.Commands)
		copy.Files = slices.Clone(original.Files)
		copy.Omitted = slices.Clone(original.Omitted)
		copy.Labels = slices.Clone(original.Labels)
		copy.Excluded = slices.Clone(original.Excluded)
		history.ExecObservation = &copy
	} else {
		// Even a patch-only cell retains a no-effect command attempt. It
		// prevents a later replay without the trace from inventing a new gap.
		history.ExecObservation = &execObservation{CodeMode: true, Class: execDeclared.String()}
	}
	if history.nativeCell == nil || !history.nativeCell.ended {
		history.ExecObservation.Class = execOpaque.String()
		history.ExecObservation.Reason = "resolved nested host inputs unavailable"
		return
	}
	patches := slices.Clone(history.NativePatches)
	commands := []execCommandInput(nil)
	if history.ExecObservation != nil {
		commands = slices.Clone(history.ExecObservation.Commands)
		if history.ExecObservation.Reason == "Code Mode cell has a non-literal command" {
			history.ExecObservation.Class = cmp.Or(history.ExecObservation.LiteralClass, execOpaque.String())
			history.ExecObservation.Reason = ""
			if history.ExecObservation.Class == execOpaque.String() {
				history.ExecObservation.Reason = "cell includes unsupported command scope"
			}
		}
	}
	for _, tool := range history.nativeCell.tools {
		switch tool.Tool {
		case applyPatchToolName:
			if index := slices.IndexFunc(patches, func(p nativePatchObservation) bool { return p.Input == tool.input }); index >= 0 {
				// A literal baseline covers one call, not all calls with the
				// same input. Additional resolved attempts remain observable.
				patches = slices.Delete(patches, index, index+1)
				continue
			}
			paths, err := nativePatchPaths(tool.input, workspace)
			if err != nil {
				continue // Host-rejected malformed input names no reliable paths.
			}
			patch := nativePatchObservation{Input: tool.input}
			for _, path := range paths {
				beforePath := path.before
				if beforePath == "" {
					beforePath = path.after
				}
				before := b.file(beforePath)
				file := nativePatchFileSnapshot{BeforePath: beforePath, AfterPath: path.after, Before: before.Content, Exists: execFilePresent(before), Error: before.Error}
				if before.Kind != execFileText && before.Kind != execFileAbsent && file.Error == "" {
					file.Error = "resolved patch baseline is not UTF-8 regular text"
				}
				if path.after != "" && path.after != beforePath {
					target := b.file(path.after)
					file.TargetBefore, file.TargetExists, file.TargetError = target.Content, execFilePresent(target), target.Error
					if target.Kind != execFileText && target.Kind != execFileAbsent && file.TargetError == "" {
						file.TargetError = "resolved move target baseline is not UTF-8 regular text"
					}
				}
				patch.Files = append(patch.Files, file)
			}
			history.NativePatches = append(history.NativePatches, patch)
		case nativeExecCommandToolName:
			command := tool.command
			if command.Workdir == "" {
				command.Workdir = workspace
			} else if !filepath.IsAbs(command.Workdir) {
				command.Workdir = filepath.Join(workspace, command.Workdir)
			}
			command.Shell = cmp.Or(command.Shell, b.Shell)
			if index := slices.Index(commands, command); index >= 0 {
				commands = slices.Delete(commands, index, index+1)
				continue
			}
			if history.lowersJournalCommand(nativeJournalCommand.FindStringSubmatch(command.Command)) {
				continue
			}
			// Scope classification is read-only. State-dependent destinations,
			// globs and providers cannot be reclassified exactly after execution.
			plan := classifyExecShellWithin(command.Command, command.Workdir, command.Shell, time.Now().Add(execProviderBudget), 0, nil)
			observation := history.ExecObservation
			if observation == nil {
				observation = &execObservation{CodeMode: true, Class: execDeclared.String()}
				history.ExecObservation = observation
			}
			observation.Commands = append(observation.Commands, command)
			observation.Labels = append(observation.Labels, plan.Labels...)
			if plan.Class == execDeclared && execClassRank(observation.Class) < execClassRank(execDeclared.String()) {
				observation.Class = execDeclared.String()
			}
			for _, entry := range plan.Scope {
				for _, operand := range entry.Operands {
					info, _ := os.Lstat(operand.Path)
					throughLink := entry.Through && (b.file(operand.Path).Kind == execFileSymlink || info != nil && info.Mode()&os.ModeSymlink != 0)
					if entry.Kind != execScopeFile || operand.Glob || throughLink {
						observation.Omitted = append(observation.Omitted, execOmission{Path: operand.Path, Reason: "resolved command scope depends on pre-execution filesystem state"})
						continue
					}
					file := b.file(operand.Path)
					file.Origin = entry.Origin
					if !slices.ContainsFunc(observation.Files, func(f execFileSnapshot) bool { return f.Path == file.Path }) {
						observation.Files = append(observation.Files, file)
					} else {
						observation.RepeatedPaths = true
					}
				}
			}
			if plan.Class > execDeclared {
				observation.Class = execOpaque.String()
				observation.Reason = "resolved command has unsupported or state-dependent write scope"
			}
		case "write_stdin":
			if !tool.stdinPoll {
				history.ExecObservation.Class = execOpaque.String()
				history.ExecObservation.Reason = "non-polling stdin has unsupported write scope"
			}
		default:
			history.ExecObservation.Class = execOpaque.String()
			history.ExecObservation.Reason = "resolved tool has unsupported write scope"
		}
	}
	// Patch paths are compared once by their patch records, even when the
	// dynamically resolved command also names them.
	if history.ExecObservation != nil {
		for _, patch := range history.NativePatches {
			for _, file := range patch.Files {
				history.ExecObservation.Excluded = append(history.ExecObservation.Excluded, file.BeforePath, file.AfterPath)
			}
		}
	}
}
