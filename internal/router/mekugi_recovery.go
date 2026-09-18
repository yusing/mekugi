package router

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi"
	codexinstructions "github.com/yusing/mekugi/contrib/codex"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
)

const mekugiRecoveryToolName = "hpatch_recover"

func mekugiRecoveryGuidance(
	script string,
	rejections []mekugi.HostRejection,
	refreshed bool,
	handles []string,
) string {
	notice := invalidFinalInputNotice(script, rejections)
	references, eligible := mekugiRecoveryReferences(script, rejections, refreshed, handles)
	if !eligible {
		return notice + genericRecoveryGuidance(script, rejections, refreshed, handles)
	}
	return notice + codexinstructions.RecoveryGuidance(references)
}

func invalidFinalInputNotice(script string, rejections []mekugi.HostRejection) string {
	if len(rejections) != 1 || rejections[0].Reason != "script-syntax" {
		return ""
	}
	line := rejections[0].SourceLine
	trimmed := strings.TrimRight(script, " \t\r\n")
	if line < 1 || line != mekugi.TextLineCount(trimmed) {
		return ""
	}
	return fmt.Sprintf("\nInvalid final input at script line %d; no effects were applied. Remove or correct it through hpatch --recover HANDLE.\n", line)
}

func genericRecoveryGuidance(script string, rejections []mekugi.HostRejection, refreshed bool, handles []string) string {
	var output strings.Builder
	if refreshed {
		output.WriteString("\nThis re-rejection changed no workspace file. Corrections are retained only in the new rejected-script baseline; earlier script rows and command handles may be stale.\n")
	}
	output.WriteString("\nRepair retained-script text with pathless type/add mutations through hpatch --recover HANDLE, without file paths, append, or file-management commands. Targets below address the rejected script, not workspace files. Use exact known literals for other retained text. The router rebuilds and reevaluates the complete script atomically; do not repeat unrelated prepared edits.\n\nRetained rejected-script rows:\n")
	lines := hpatchsyntax.SplitPhysicalLines(script)
	logicalRows := mekugiLogicalRowsByPhysicalLine(script, lines)
	commands := recoveryCommands(script, handles)
	offered := make(map[int]bool)
	for _, rejection := range rejections {
		if rejection.Command < 1 || rejection.Command > len(commands) || offered[rejection.Command] {
			continue
		}
		command := commands[rejection.Command-1]
		if !command.parts.parsed {
			continue
		}
		offered[rejection.Command] = true
		fmt.Fprintf(&output, "Command correction: %s value VALUE replaces only this command's value (quoted or heredoc).\n", command.handle)
		if command.parts.target != "" && command.parts.target != "EOF" {
			fmt.Fprintf(&output, "Command correction: %s target TARGET replaces only its workspace target.\n", command.handle)
		}
	}
	if len(offered) != 0 {
		output.WriteString("Use each handle once; command corrections may share a payload but cannot mix with script-text mutations. Generated-source line numbers are diagnostic only, never recovery targets.\n\n")
	}
	const rowLimit = 12
	rows := make([]int, 0, rowLimit)
	seen := make(map[int]bool)
	addPhysical := func(index int) {
		if index < 0 || index >= len(logicalRows) {
			return
		}
		for _, row := range logicalRows[index] {
			if !seen[row] && len(rows) < rowLimit {
				rows = append(rows, row)
				seen[row] = true
			}
		}
	}
	for _, rejection := range rejections {
		if rejection.Command < 1 || rejection.Command > len(commands) {
			continue
		}
		command := commands[rejection.Command-1]
		addPhysical(command.header)
		if rejection.ValueLine > 0 {
			for _, offset := range []int{-1, 0, 1} {
				addPhysical(command.header + rejection.ValueLine + offset)
			}
		} else {
			addPhysical(command.header + 1)
			addPhysical(command.end - 1)
		}
	}
	if len(rows) == 0 {
		for index := len(logicalRows) - 1; index >= 0; index-- {
			addPhysical(index)
			if len(rows) != 0 {
				break
			}
		}
	}
	slices.Sort(rows)
	output.WriteString(mekugi.TextReferences(script, rows...))
	if len(rows) == rowLimit {
		output.WriteString("Preview limited to 12 script rows.\n")
	}
	return output.String()
}

func mekugiRecoveryReferences(
	script string,
	rejections []mekugi.HostRejection,
	refreshed bool,
	handles []string,
) (string, bool) {
	commands := recoveryCommands(script, handles)
	relevant := make(map[int]struct{})
	for _, rejection := range rejections {
		if rejection.Reason != "row-stale" || rejection.Command < 1 || rejection.Command > len(commands) {
			return "", false
		}
		command := commands[rejection.Command-1]
		if !command.parts.parsed || command.parts.target == "" {
			return "", false
		}
		relevant[rejection.Command] = struct{}{}
	}
	if len(relevant) == 0 {
		return "", false
	}

	var output strings.Builder
	if refreshed {
		output.WriteString("This re-rejection changed no workspace file. Earlier command handles are stale; use only the current handles below.\n\n")
	}
	output.WriteString("Rejected target commands:\n")
	indices := make([]int, 0, len(relevant))
	for index := range relevant {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		command := commands[index-1]
		fmt.Fprintf(&output, "    %s %s\n", command.handle, mekugiRecoveryCommandSummary(command))
	}
	output.WriteString("\nSend one line per listed command as HANDLE CURRENT_TARGET. Put all corrections in one hpatch --recover HANDLE payload; the router preserves every operation and value and reevaluates the complete script.\n")
	return output.String(), true
}

// mekugiRecoveryCommandSummary creates a summary string for a recovery command reference.
func mekugiRecoveryCommandSummary(command recoveryCommandReference) string {
	summary := command.parts.operation
	if command.path != "" {
		if command.script > 0 {
			summary += fmt.Sprintf(" script %d file %q", command.script, command.path)
		} else {
			summary += " " + command.path
		}
	}
	if command.parts.target != "" {
		summary += " " + command.parts.target
	}
	if command.parts.multiline {
		return summary + " [heredoc value]"
	}
	return summary + " [inline value]"
}

func mekugiLogicalRowsByPhysicalLine(script string, lines []hpatchsyntax.PhysicalLine) [][]int {
	mapped := make([][]int, len(lines))
	offset := 0
	logicalRow := 1
	for index, line := range lines {
		next := offset + len(line.Text) + len(line.Terminator)
		count := mekugi.TextLineCount(script[offset:next])
		for range count {
			mapped[index] = append(mapped[index], logicalRow)
			logicalRow++
		}
		offset = next
	}
	return mapped
}

// recoveryBaseline is the complete rejected script a following recovery edits.
func (h mekugiHistory) recoveryBaseline() string {
	if h.Evaluated != "" {
		return h.Evaluated
	}
	return h.Script
}
