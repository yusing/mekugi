package router

import (
	"path/filepath"
	"slices"
)

// Legacy schema, decoded only when reading retained pre-redesign records.
type resolvedStockBaseline struct {
	Root      string
	Shell     string
	Files     []execFileSnapshot `json:",omitempty"`
	Omitted   []execOmission     `json:",omitempty"`
	Inventory *execInventory     `json:",omitempty"`
}

// Host tracing confirms outcomes and repeated literal call sites. It never
// learns a before-state after execution or inventories unresolved inputs.
func resolveStockEdits(history *mekugiHistory, workspace string) {
	if history.nativeCell == nil || !history.nativeCell.ended {
		return
	}
	if original := history.ExecObservation; original != nil {
		copy := *original
		copy.Commands = slices.Clone(original.Commands)
		classes := original.commandClasses()
		copy.CommandClasses = classes
		history.ExecObservation = &copy
		remaining := slices.Clone(original.Commands)
		for _, tool := range history.nativeCell.tools {
			if tool.Tool != nativeExecCommandToolName || !execLocalEnvironment(tool.environmentID) {
				continue
			}
			command := tool.command
			if command.Workdir == "" {
				command.Workdir = workspace
			}
			if !filepath.IsAbs(command.Workdir) {
				command.Workdir = filepath.Join(workspace, command.Workdir)
			}
			command.Workdir = filepath.Clean(command.Workdir)
			i := slices.IndexFunc(original.Commands, func(c execCommandInput) bool {
				return c.Command == command.Command && c.Workdir == command.Workdir && (command.Shell == "" || command.Shell == c.Shell)
			})
			if i < 0 {
				continue
			}
			command = original.Commands[i]
			if j := slices.Index(remaining, command); j >= 0 {
				remaining = slices.Delete(remaining, j, j+1)
				continue
			}
			copy.Commands = append(copy.Commands, command)
			copy.CommandClasses = append(copy.CommandClasses, classes[i])
			copy.RepeatedPaths = copy.RepeatedPaths || classes[i] != execNeutral.String()
		}
	}
	literal := slices.Clone(history.NativePatches)
	remaining := slices.Clone(literal)
	history.NativePatches = slices.Clone(literal)
	for _, tool := range history.nativeCell.tools {
		if tool.Tool != applyPatchToolName {
			continue
		}
		if i := slices.IndexFunc(remaining, func(p nativePatchObservation) bool { return p.Input == tool.input }); i >= 0 {
			remaining = slices.Delete(remaining, i, i+1)
			continue
		}
		if i := slices.IndexFunc(literal, func(p nativePatchObservation) bool { return p.Input == tool.input }); i >= 0 {
			history.NativePatches = append(history.NativePatches, literal[i])
		}
	}
}
