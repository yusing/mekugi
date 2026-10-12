package router

import (
	json "encoding/json/v2"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Host MCP items include nested code-mode calls. Live and history adapters use
// the same call identity and bounded output owner, without publishing model text.
func (s *appServerSession) orchestrationActivity(entry *activityPaneEntry, item appServerItem, phase string, live bool) bool {
	if item.Type != "mcpToolCall" || item.Server != "orchestrate" {
		return false
	}
	var args struct {
		TaskName string `json:"task_name"`
		Target   string `json:"target"`
	}
	_ = json.Unmarshal(item.Arguments, &args)
	label := item.Tool
	if target := strings.TrimSpace(args.TaskName + args.Target); target != "" {
		label += " " + target
	}
	status := item.Status
	if status == "inProgress" {
		status = "in progress"
		if !live {
			status = "unfinished (history only)"
		}
	}
	if status != "" {
		label += " · call " + status
	}
	block := &activityui.Block{Kind: "op", Verb: "Orchestrate", Label: livediff.Safe(label, false)}
	entry.Kind, entry.Text = "tool", label
	entry.native.duration = appServerDuration(item)
	entry.native.orchestration = block
	if phase != "item/completed" {
		return true
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(item.Result, &result)
	var output []string
	for _, content := range result.Content {
		if content.Type == "text" && content.Text != "" {
			output = append(output, content.Text)
		}
	}
	if item.Error != nil {
		output = append(output, item.Error.Message)
	}
	if len(output) > 0 {
		block.Output = s.outputs.New()
		block.Output.Finish(new(strings.Join(output, "\n")), nil)
		view := block.Output.View()
		block.TailRows = 5
		start := max(0, len(view.Lines)-block.TailRows)
		block.Tail, block.TailOmitted = slices.Clone(view.Lines[start:]), view.Dropped+start
	}
	return true
}
