package sessionapi

import (
	"strings"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

// WaitProgress freezes exact capture selections without Diff-pane filtering or
// history materialization. The completed combined yield owns output bounding.
func (h *Hub) WaitProgress(thread string, targets []string, seen map[string]int) (threadcontrol.WaitProgress, map[string]int) {
	family, review := h.family(thread, false)
	result := threadcontrol.WaitProgress{}
	cursor := make(map[string]int)
	if family == nil || review == nil {
		result.Error = "Captured progress is unavailable"
		return result, cursor
	}
	result.Error = review.Failure
	if review.Coverage != "complete" && result.Error == "" {
		result.Error = "Capture coverage: " + review.Coverage
	}
	if runes := []rune(result.Error); len(runes) > 256 {
		result.Error = string(runes[:256]) + "…"
	}
	callers := make(map[string]bool, len(targets))
	for _, target := range targets {
		callers[target] = true
	}
	indices := make(map[string]int)
	ids := make(map[string]bool)
	var selections, intervals [][]string
	for capture := range review.Publication.Progress(callers, seen) {
		index, exists := indices[capture.Caller]
		if !exists {
			index = len(result.Agents)
			indices[capture.Caller] = index
			result.Agents = append(result.Agents, threadcontrol.AgentProgress{Task: capture.Caller})
			selections = append(selections, nil)
			intervals = append(intervals, nil)
		}
		key := capture.Caller + "\x00" + capture.Change
		if !ids[key] {
			ids[key] = true
			selections[index] = append(selections[index], capture.Change)
		}
		intervals[index] = append(intervals[index], capture.Interval)
		cursor[capture.Key] = capture.Chunks
	}
	for i := range result.Agents {
		result.Agents[i].Changes = strings.Join(selections[i], " ") + "\nFrozen intervals (retained record order, inclusive chunks): " + strings.Join(intervals[i], "; ")
	}
	return result, cursor
}
