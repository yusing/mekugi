package sessionapi

import (
	"cmp"
	json "encoding/json/v2"
	"slices"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

// WaitProgress uses the ordinary family projection, without Diff-pane filtering.
// The returned cursor advances only captures included in the bounded result.
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
	agents := make(map[string]threadcontrol.AgentProgress)
	callers := make(map[string]bool, len(targets))
	for _, target := range targets {
		callers[target] = true
	}
	if family.Agents != nil {
		for _, row := range family.Agents.Roster {
			agent := row.Agent
			if !callers[agent.Name] {
				continue
			}
			activity := agent.Generating
			if activity == "" && len(agent.Summary) > 0 {
				entry := agent.Summary[len(agent.Summary)-1]
				activity = entry.Text
				if activity == "" && len(entry.Values) > 0 {
					activity = cmp.Or(entry.Values[0].Body, entry.Values[0].RowVerb())
				}
			}
			if runes := []rune(activity); len(runes) > 256 {
				activity = string(runes[:256]) + "…"
			}
			agents[agent.Name] = threadcontrol.AgentProgress{Task: agent.Name, Status: agent.Status, Activity: activity}
		}
	}
	for capture := range review.Publication.Progress(callers, seen) {
		index := slices.IndexFunc(result.Agents, func(agent threadcontrol.AgentProgress) bool { return agent.Task == capture.Caller })
		previous := ""
		if index < 0 {
			index = len(result.Agents)
			agent := agents[capture.Caller]
			agent.Task = capture.Caller
			agent.Status = cmp.Or(agent.Status, "unavailable")
			result.Agents = append(result.Agents, agent)
		} else {
			previous = result.Agents[index].Changes
		}
		result.Agents[index].Changes = previous + capture.Summary + "\n"
		encoded, _ := json.Marshal(result)
		if len(encoded) > 12<<10 {
			if previous == "" {
				result.Agents = result.Agents[:index]
			} else {
				result.Agents[index].Changes = previous
			}
			result.More = true
			break
		}
		cursor[capture.Key] = capture.Chunks
	}
	return result, cursor
}
