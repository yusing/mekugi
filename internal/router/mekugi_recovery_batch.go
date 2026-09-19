package router

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/yusing/mekugi"
)

// recoveryBatchCommands flattens per-file command frames into the one handle
// sequence persisted for an invocation. Command numbers remain invocation-wide,
// while each reference retains the original file path for diagnostics.
func recoveryBatchCommands(edits []mekugi.FileEdit, handles []string) []recoveryCommandReference {
	commands := make([]recoveryCommandReference, 0)
	handleIndex := 0
	for scriptIndex, edit := range edits {
		count := len(recoveryCommands(edit.Script, nil))
		var localHandles []string
		if handleIndex < len(handles) {
			localHandles = handles[handleIndex:min(handleIndex+count, len(handles))]
		}
		local := recoveryCommands(edit.Script, localHandles)
		handleIndex += count
		for _, command := range local {
			command.path = edit.Path
			command.script = scriptIndex + 1
			command.index = len(commands) + 1
			commands = append(commands, command)
		}
	}
	return commands
}

func recoveryBatchHandlesBinding(edits []mekugi.FileEdit, handles []string) string {
	var source strings.Builder
	fmt.Fprintf(&source, "%d:", len(edits))
	for _, edit := range edits {
		fmt.Fprintf(&source, "%d:%s%d:%s", len(edit.Path), edit.Path, len(edit.Script), edit.Script)
	}
	source.WriteString(strconv.Itoa(len(handles)))
	for _, handle := range handles {
		fmt.Fprintf(&source, "%d:%s", len(handle), handle)
	}
	return recoveryHash(source.String())
}

type recoveredBatch struct {
	edits []mekugi.FileEdit
	delta string
}

func recoverBatchDetailed(
	ctx context.Context,
	baseline []mekugi.FileEdit,
	payload string,
	handles []string,
	scriptIndex int,
) (recoveredBatch, error) {
	if len(baseline) == 0 {
		return recoveredBatch{}, fmt.Errorf("retained edit has no file scripts")
	}
	commands := recoveryBatchCommands(baseline, handles)
	if len(commands) != len(handles) {
		return recoveredBatch{}, fmt.Errorf("invalid retained command handles")
	}

	selected := scriptIndex - 1
	if scriptIndex < 0 || scriptIndex > len(baseline) {
		return recoveredBatch{}, fmt.Errorf("recovery script %d is not retained", scriptIndex)
	}
	if scriptIndex == 0 {
		if len(baseline) != 1 {
			return recoveredBatch{}, fmt.Errorf("recovery requires --script N for a multi-file edit")
		}
		selected = 0
	}

	// The command-level protocol is intentionally pathless. Select the original
	// file first, then rebuild only its complete script while all other files
	// remain byte-for-byte unchanged.
	offset := 0
	for index := range baseline {
		if index == selected {
			break
		}
		offset += len(recoveryCommands(baseline[index].Script, nil))
	}
	count := len(recoveryCommands(baseline[selected].Script, nil))
	if offset+count > len(handles) {
		return recoveredBatch{}, fmt.Errorf("invalid retained command handles")
	}
	selectedHandles := handles[offset : offset+count]
	recovered, err := recoverScriptDetailed(ctx, baseline[selected].Script, payload, selectedHandles)
	if err != nil {
		return recoveredBatch{}, err
	}
	edits := make([]mekugi.FileEdit, len(baseline))
	copy(edits, baseline)
	edits[selected].Script = recovered.script
	total := 0
	for _, edit := range edits {
		total += len(edit.Script)
		if total > maxMekugiScriptBytes {
			return recoveredBatch{}, fmt.Errorf("script exceeds %d bytes", maxMekugiScriptBytes)
		}
	}
	return recoveredBatch{edits: edits, delta: recovered.delta}, nil
}

func mekugiRecoveryGuidanceBatch(
	edits []mekugi.FileEdit,
	rejections []mekugi.HostRejection,
	refreshed bool,
	handles []string,
) string {
	commands := recoveryBatchCommands(edits, handles)
	byCommand := make(map[int]recoveryCommandReference, len(commands))
	for _, command := range commands {
		byCommand[command.index] = command
	}
	var output strings.Builder
	if refreshed {
		output.WriteString("\nThis re-rejection changed no workspace file. Earlier command handles are stale; use only the current handles below.\n")
	}
	output.WriteString("\nRepair the retained pathless script selected by --script N; the router reevaluates the complete multi-file edit atomically. Command corrections preserve all other files and fields.\n\nRejected commands:\n")
	seen := make(map[int]bool)
	for _, rejection := range rejections {
		command, ok := byCommand[rejection.Command]
		if !ok || seen[rejection.Command] || !command.parts.parsed {
			continue
		}
		seen[rejection.Command] = true
		fmt.Fprintf(&output, "    %s %s\n", command.handle, mekugiRecoveryCommandSummary(command))
	}
	if len(seen) != 0 {
		output.WriteString("\nUse each handle once. Send one pathless correction through hpatch --recover HANDLE --script N. Generated-source line numbers are diagnostic only, never recovery targets.\n\n")
	}
	offset := 0
	for index, edit := range edits {
		localCommands := recoveryCommands(edit.Script, nil)
		count := len(localCommands)
		localRejections := make([]mekugi.HostRejection, 0)
		unparsed := false
		for _, rejection := range rejections {
			if rejection.Command <= offset || rejection.Command > offset+count {
				continue
			}
			local := rejection
			local.Command -= offset
			localRejections = append(localRejections, local)
			if local.Command >= 1 && local.Command <= len(localCommands) && !localCommands[local.Command-1].parts.parsed {
				unparsed = true
			}
		}
		if unparsed {
			var localHandles []string
			if offset < len(handles) {
				localHandles = handles[offset:min(offset+count, len(handles))]
			}
			fmt.Fprintf(&output, "\nScript %d, file %q retained-script context:\n", index+1, edit.Path)
			output.WriteString(genericRecoveryGuidance(edit.Script, localRejections, refreshed, localHandles, index+1))
		}
		offset += count
	}
	return output.String()
}
