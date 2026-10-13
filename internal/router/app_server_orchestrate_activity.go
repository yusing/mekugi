package router

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"github.com/yusing/mekugi/internal/orchestrate"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Host MCP items include nested code-mode calls. Live and history adapters use
// the same call identity and bounded output owner, without publishing model text.
func (u *appServerUI) orchestrationActivity(entry *activityPaneEntry, item appServerItem, phase string, live bool) bool {
	if item.Type != "mcpToolCall" || item.Server != "orchestrate" {
		return false
	}
	var args struct {
		TaskName  string         `json:"task_name"`
		Target    string         `json:"target"`
		CancelID  string         `json:"cancel_id"`
		Message   string         `json:"message"`
		Targets   jsontext.Value `json:"targets"`
		TimeoutMS uint64         `json:"timeout_ms"`
	}
	_ = json.Unmarshal(item.Arguments, &args)
	target := strings.TrimSpace(cmp.Or(args.TaskName, args.Target))
	verb := map[string]string{
		"prepare": "Prepare", "spawn_agent": "Start", "followup_task": "Follow up",
		"send_message": "Message", "interrupt_agent": "Interrupt", "list_agents": "List",
		"integrate": "Integrate", "cleanup": "Clean up", "wait_agent": "Wait",
	}[item.Tool]
	if verb == "" {
		verb = item.Tool
	}
	if item.Tool == "list_agents" {
		target = "agents"
	}
	status := item.Status
	if status == "inProgress" {
		status = "in progress"
		if !live {
			status = "unfinished (history only)"
		}
	}
	label := target
	if status != "" {
		label = strings.TrimSpace(label + " · " + status)
	}
	block := &activityui.Block{Kind: "op", Verb: verb, Label: livediff.Safe(label, false)}
	entry.Kind, entry.Text = "tool", verb+" "+label
	entry.native.duration = appServerDuration(item)
	entry.native.orchestration = block
	entry.native.orchestrated = true
	isWait := item.Tool == "wait_agent"
	if isWait {
		*block = *appServerWaitProgress(appServerItem{Type: "collabAgentToolCall", Tool: "wait", Status: item.Status, waitTimeoutMS: args.TimeoutMS}, phase)
		names, err := orchestrateWaitTargets(args.Targets)
		if err == nil {
			if len(names) == 0 {
				names = []string{"all"}
			}
			for _, name := range names {
				block.WaitTargets = append(block.WaitTargets, activityui.WaitTarget{Name: name})
			}
		}
		if !live && item.Status == "inProgress" {
			block.Body = "Wait unfinished (history only)"
		}
		entry.Kind, entry.Text = "progress", block.ProgressText()
	}
	if phase != "item/completed" {
		return true
	}
	var result struct {
		IsError    bool           `json:"isError"`
		Structured jsontext.Value `json:"structuredContent"`
		Content    []struct {
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
	var outcome orchestrate.Batch
	data := result.Structured
	if len(data) == 0 && len(output) == 1 {
		data = jsontext.Value(output[0])
	}
	_ = json.Unmarshal(data, &outcome)
	if isWait && item.Error == nil && !result.IsError {
		switch outcome.State {
		case "registered":
			block.Body = "Wait registered"
		case "canceled":
			block.Body = "Wait canceled"
		case "dispatching", "uncertain":
			block.Body = "Wait delivery unconfirmed"
		case "rejected":
			block.Body = "Wait delivery failed"
		}
		if args.CancelID != "" {
			block.WaitTargets = nil
		}
		entry.Text = block.ProgressText()
	}

	if item.Error != nil || result.IsError || item.Status == "failed" {
		reason := cmp.Or(strings.Join(output, "\n"), item.Status, "failed")
		if isWait {
			block.Body = "Wait failed"
			entry.Text = block.ProgressText()
		} else {
			*block = activityui.Block{Kind: "error", Body: livediff.Safe(strings.TrimSpace(verb+" "+target+": "+reason), false)}
			entry.Kind = "error"
		}
	} else if item.Status == "completed" {
		// Reuse the native collaboration block builder, but retain this MCP
		// item's identity and caller. Only confirmed outcomes claim delivery.
		semantic := activityPaneEntry{Agent: entry.Agent}
		to := target
		if to == "main" || to == "/root" {
			to = "/root"
		} else if !strings.HasPrefix(to, "/root/") {
			to = "/root/" + to
		}
		from := entry.Agent
		if from == "Main" {
			from = "/root"
		}
		if n := u.navigation; n != nil {
			if child := n.owner.orchestrateThreads[entry.native.thread]; child != nil {
				from = "/root/" + child.batch.TaskName
			}
		}
		switch {
		case item.Tool == "spawn_agent" && outcome.Launch != nil && outcome.Launch.TurnID != "":
			semantic.Kind, semantic.start = "start", &activityStart{}
			semantic.assignment = &activityAssignment{from: from, to: to, text: args.Message}
		case (item.Tool == "send_message" || item.Tool == "followup_task") && (outcome.State == "delivered" || outcome.State == "reported"):
			semantic.Kind = "reply"
			semantic.message = &activityMessage{from: from, to: to, text: args.Message}
		}
		if semantic.Kind != "" {
			*block = parseLiveActivity(semantic)[0]
			entry.Kind = semantic.Kind
			entry.start, entry.assignment, entry.message = semantic.start, semantic.assignment, semantic.message
		} else if outcome.State != "" && !isWait {
			block.Label = livediff.Safe(strings.TrimSpace(target+" · "+outcome.State), false)
		}
	}
	if len(output) > 0 {
		block.Output = u.session.outputs.New()
		block.Output.Finish(new(strings.Join(output, "\n")), nil)
	}
	return true
}
