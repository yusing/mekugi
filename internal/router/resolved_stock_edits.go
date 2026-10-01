package router

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// This is a bounded pre-cell baseline, not a workspace diff or an ownership
// sweep. Only paths named by resolved native calls may consume it. In
// particular, another agent's writes to unrelated files are never compared.
// Its inventory is the cell's only one: when the cell's command observation
// holds an inventory, the baseline has none of its own and reads that one.
// Older records list every file in Files instead.
type resolvedStockBaseline struct {
	Root      string
	Shell     string
	Files     []execFileSnapshot `json:",omitempty"`
	Omitted   []execOmission     `json:",omitempty"`
	Inventory *execInventory     `json:",omitempty"`
}

// boundStockInventory keeps a Code Mode cell's record within the store bound,
// so its inventory never blocks an otherwise supported carrier. The result
// record may hold the cell's one inventory twice, for the resolved baseline
// and the command observation, so the bound counts it twice. Content is
// dropped largest first; dropped files keep their stamps and blob ids.
func boundStockInventory(history *mekugiHistory, workspace, callID string) {
	if history.ResolvedBaseline == nil {
		return
	}
	baseline := *history.ResolvedBaseline
	var observation *execObservation
	inventory := baseline.Inventory
	if inventory == nil && history.ExecObservation != nil {
		copy := *history.ExecObservation
		observation, inventory = &copy, copy.Inventory
	}
	if inventory == nil {
		return
	}
	bounded := *inventory
	measured := *history
	measured.ResolvedBaseline = &baseline
	baseline.Inventory = &bounded
	if observation != nil {
		observation.Inventory = &bounded
		measured.ExecObservation = observation
	} else {
		var copy execObservation
		if measured.ExecObservation != nil {
			copy = *measured.ExecObservation
		}
		copy.Inventory = &bounded
		measured.ExecObservation = &copy
	}
	record := replayRecord{Version: 1, Workspace: workspace, CallID: callID, History: durableHistory(measured)}
	excess := len(mustMarshalJSON(record)) - maxReplayRecordBytes
	if excess <= 0 {
		return
	}
	boundExecInventory(&bounded, len(mustMarshalJSON(&bounded))-(excess+1)/2)
	if observation != nil {
		history.ExecObservation = observation
	} else {
		history.ResolvedBaseline = &baseline
	}
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

// newResolvedBaseline is a baseline without an inventory of its own, for a
// cell whose command observation captures one.
func newResolvedBaseline(workspace, shell string) *resolvedStockBaseline {
	baseline := &resolvedStockBaseline{Root: workspace, Shell: shell}
	if !filepath.IsAbs(workspace) {
		baseline.Omitted = []execOmission{{Path: workspace, Reason: "resolved edit baseline requires a selected workspace"}}
	}
	return baseline
}

func captureResolvedBaseline(workspace, shell string) *resolvedStockBaseline {
	baseline := newResolvedBaseline(workspace, shell)
	if baseline.Omitted != nil {
		return baseline
	}
	gap := func(reason string) *resolvedStockBaseline {
		baseline.Omitted = []execOmission{{Path: workspace, Reason: reason}}
		return baseline
	}
	deadline := time.Now().Add(execCaptureHold)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	done := make(chan *execInventory, 1)
	select {
	case execCaptureSlots <- struct{}{}:
		go func() {
			defer func() { <-execCaptureSlots }()
			budget := maxExecContentBytes
			inventory := captureExecInventory(workspace, deadline.Add(-execInventoryMargin), &budget)
			boundExecInventory(inventory, maxExecObservationBytes)
			done <- inventory
		}()
	case <-timer.C:
		return gap("resolved edit baseline capture deadline")
	}
	select {
	case inventory := <-done:
		if time.Now().Before(deadline) {
			baseline.Inventory = inventory
			return baseline
		}
	case <-timer.C:
	}
	return gap("resolved edit baseline capture deadline")
}

// file is the pre-cell state of one resolved path: captured content, then a
// byte-verified Git blob, then current bytes with an unchanged strong stamp.
// A changed file without either is a gap, and a path the complete
// enumeration did not list was absent.
func (b *resolvedStockBaseline) file(path string) execFileSnapshot {
	path = filepath.Clean(path)
	if b == nil {
		return execFileSnapshot{Path: path, Error: "resolved path has no pre-cell baseline"}
	}
	inventory := b.Inventory
	files := b.Files
	if inventory != nil {
		files = inventory.Files
	}
	for _, file := range files {
		if file.Path == path {
			return file
		}
		if file.Kind == execFileSymlink && execPathWithin(path, file.Path) {
			return execFileSnapshot{Path: path, Error: "resolved edit baseline does not follow symlink directories"}
		}
	}
	omitted := b.Omitted
	if inventory != nil {
		omitted = append(slices.Clone(omitted), inventory.Omitted...)
		if relative, err := filepath.Rel(inventory.Root, path); err == nil && execPathWithin(path, inventory.Root) {
			if snapshot, ok := b.inventoryFile(inventory, path, relative); ok {
				return snapshot
			}
		}
	} else if len(b.Files) == 0 && len(b.Omitted) == 0 {
		return execFileSnapshot{Path: path, Error: "resolved path has no pre-cell baseline"}
	}
	for _, omission := range omitted {
		if execPathWithin(path, omission.Path) {
			return execFileSnapshot{Path: path, Error: omission.Reason}
		}
	}
	if !filepath.IsAbs(b.Root) || !execPathWithin(path, b.Root) {
		return execFileSnapshot{Path: path, Error: "resolved path is outside the pre-cell baseline"}
	}
	return execFileSnapshot{Path: path} // Complete enumeration proves absence.
}

func (b *resolvedStockBaseline) inventoryFile(inventory *execInventory, path, relative string) (execFileSnapshot, bool) {
	stamp, listed := inventory.Entries[relative]
	if !listed {
		for parent := filepath.Dir(relative); parent != "."; parent = filepath.Dir(parent) {
			if _, link := inventory.Entries[parent]; link {
				return execFileSnapshot{Path: path, Error: "resolved edit baseline does not follow symlink directories"}, true
			}
		}
		if inventory.prunedWithin(relative) {
			return execFileSnapshot{Path: path, Error: "resolved edit baseline excludes metadata and dependency trees"}, true
		}
		return execFileSnapshot{}, false
	}
	if inventory.Version == 1 && inventory.Blobs[relative] != "" {
		ctx, cancel := context.WithTimeout(context.Background(), execInventoryQueryTimeout)
		defer cancel()
		budget := maxExecContentBytes
		if snapshot, ok := inventory.blobs(ctx, []string{relative}, &budget)[relative]; ok {
			return snapshot, true
		}
	}
	if inventory.Version == 1 && stamp != "" {
		current, unchanged := snapshotInventoryBaseline(inventory.Root, relative, stamp)
		if unchanged {
			return current, true
		}
	}
	return execFileSnapshot{Path: path, Error: "before content not captured"}, true
}

// Resolved inputs come from host tracing, never from evaluating JavaScript.
// They share the original cell window, not an invented per-nested-call window.
func resolveStockEdits(history *mekugiHistory, workspace string) {
	b := history.ResolvedBaseline
	if b == nil && (history.nativeCell == nil || !history.nativeCell.ended) {
		return
	}
	var inventory *execInventory
	if history.ExecObservation != nil {
		inventory = history.ExecObservation.Inventory
	}
	if b != nil && b.Inventory != nil {
		inventory = b.Inventory
	} else if b != nil && inventory != nil {
		copy := *b
		copy.Inventory = inventory
		b = &copy
	}
	// The pre-cell carrier is immutable replay evidence. Resolve on a copy;
	// completed derived records retain the actual inputs and comparisons.
	history.NativePatches = slices.Clone(history.NativePatches)
	if original := history.ExecObservation; original != nil {
		copy := *original
		copy.Commands = slices.Clone(original.Commands)
		copy.CommandClasses = original.commandClasses()
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
		history.ExecObservation.Inventory = inventory
		return
	}
	patches := slices.Clone(history.NativePatches)
	literalPatches := slices.Clone(patches)
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
	literalCommands := slices.Clone(commands)
	literalClasses := slices.Clone(history.ExecObservation.CommandClasses)
	// Only a local call with unknown write scope compares the inventory.
	// Fully scoped, remote-only, and reader-only cells keep their named scope.
	inventoryNeeded := false
	for _, tool := range history.nativeCell.tools {
		switch tool.Tool {
		case applyPatchToolName:
			if index := slices.IndexFunc(patches, func(p nativePatchObservation) bool { return p.Input == tool.input }); index >= 0 {
				// A literal baseline covers one call, not all calls with the
				// same input. Additional resolved attempts remain observable.
				patches = slices.Delete(patches, index, index+1)
				continue
			}
			if index := slices.IndexFunc(literalPatches, func(p nativePatchObservation) bool { return p.Input == tool.input }); index >= 0 {
				// Repeated execution of a literal call site shares its original
				// cell-window baseline; it does not need a workspace inventory.
				history.NativePatches = append(history.NativePatches, literalPatches[index])
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
			if !execLocalEnvironment(tool.environmentID) {
				history.ExecObservation.Class = execOpaque.String()
				history.ExecObservation.Reason = "resolved command targets another environment; local capture unavailable"
				continue
			}
			command := tool.command
			if command.Workdir == "" {
				command.Workdir = workspace
			} else if !filepath.IsAbs(command.Workdir) {
				command.Workdir = filepath.Join(workspace, command.Workdir)
			}
			command.Workdir = filepath.Clean(command.Workdir)
			if b != nil {
				command.Shell = cmp.Or(command.Shell, b.Shell)
			} else if command.Shell == "" {
				// Without a dynamic inventory, recover the default shell only
				// when the pre-captured matching inputs agree on it.
				var shells []string
				for _, literal := range literalCommands {
					if literal.Command == command.Command && literal.Workdir == command.Workdir && !slices.Contains(shells, literal.Shell) {
						shells = append(shells, literal.Shell)
					}
				}
				if len(shells) == 1 {
					command.Shell = shells[0]
				}
			}
			if index := slices.Index(commands, command); index >= 0 {
				inventoryNeeded = inventoryNeeded || literalClasses[slices.Index(literalCommands, command)] == execOpaque.String()
				commands = slices.Delete(commands, index, index+1)
				continue
			}
			if history.lowersJournalCommand(nativeJournalCommand.FindStringSubmatch(command.Command)) {
				continue
			}
			if index := slices.Index(literalCommands, command); index >= 0 {
				history.ExecObservation.Commands = append(history.ExecObservation.Commands, command)
				class := literalClasses[index]
				inventoryNeeded = inventoryNeeded || class == execOpaque.String()
				history.ExecObservation.CommandClasses = append(history.ExecObservation.CommandClasses, class)
				history.ExecObservation.RepeatedPaths = history.ExecObservation.RepeatedPaths || class != execNeutral.String()
				continue
			}
			if b == nil {
				inventoryNeeded = true
				history.ExecObservation.Commands = append(history.ExecObservation.Commands, command)
				history.ExecObservation.CommandClasses = append(history.ExecObservation.CommandClasses, execOpaque.String())
				history.ExecObservation.Class = execOpaque.String()
				history.ExecObservation.Reason = "resolved command scope has no pre-cell baseline"
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
			observation.CommandClasses = append(observation.CommandClasses, plan.Class.String())
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
				inventoryNeeded = true
				observation.Class = execOpaque.String()
				observation.Reason = "resolved command has unsupported or state-dependent write scope"
			}
		case "write_stdin":
			if !tool.stdinPoll {
				inventoryNeeded = inventoryNeeded || execLocalEnvironment(tool.environmentID)
				history.ExecObservation.Class = execOpaque.String()
				history.ExecObservation.Reason = "non-polling stdin has unsupported write scope"
			}
		default:
			inventoryNeeded = inventoryNeeded || execLocalEnvironment(tool.environmentID)
			history.ExecObservation.Class = execOpaque.String()
			history.ExecObservation.Reason = "resolved tool has unsupported write scope"
		}
	}
	// Patch paths are compared once by their patch records, even when the
	// dynamically resolved command also names them.
	if history.ExecObservation != nil {
		history.ExecObservation.Inventory = nil
		if inventoryNeeded {
			history.ExecObservation.Inventory = inventory
		}
		for _, patch := range history.NativePatches {
			for _, file := range patch.Files {
				history.ExecObservation.Excluded = append(history.ExecObservation.Excluded, file.BeforePath, file.AfterPath)
			}
		}
	}
}
