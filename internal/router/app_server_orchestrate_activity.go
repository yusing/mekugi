package router

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type orchestrationWaitView struct {
	wait     orchestrate.Wait
	block    *activityui.Block
	restored bool // Settled history updates its row, not Main's current operation.
}

// Retained wait ownership, not a completed host turn or an old transcript
// registration, determines whether Main has suspended its operation.
func (s *appServerSession) orchestrationWaitSuspends(turn string) bool {
	for _, result := range s.orchestrationWaitResults {
		if turn != "" && result.wait.Turn == turn && result.wait.State != "completed" && result.wait.State != "canceled" {
			return true
		}
	}
	return false
}

func (s *appServerSession) orchestrationWaitStatus() (string, bool) {
	if s.orchestrationWaitTurn == "" {
		return "", false
	}
	var selected *orchestrationWaitView
	active := false
	for _, result := range s.orchestrationWaitResults {
		if result.wait.Turn != s.orchestrationWaitTurn || result.wait.State == "completed" {
			continue
		}
		if result.restored && (result.wait.State == "delivered" || result.wait.State == "canceled") {
			continue
		}
		pending := slices.Contains([]string{"registered", "ready", "dispatching"}, result.wait.State)
		if selected == nil || pending && !active || pending == active && result.wait.ID < selected.wait.ID {
			selected, active = result, pending
		}
	}
	if selected == nil {
		return "", false
	}
	return selected.block.ProgressText(), active
}

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
			block.WaitTargets = nil
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
	if !isWait {
		_ = json.Unmarshal(data, &outcome)
	} else if live || item.Status != "inProgress" {
		var wait orchestrate.Wait
		_ = json.Unmarshal(data, &wait)
		// Only the registration row receives a later logical result. A repeated
		// call acknowledges delivery without returning the selected event again.
		if wait.State == "registered" && args.CancelID == "" && item.Error == nil && !result.IsError && item.Status != "failed" {
			entry.native.orchestrationWaitID = wait.ID
		}
		orchestrationWaitResult(block, wait)
		block.Live = live && wait.State == "registered"
		entry.Text = block.ProgressText()
	}

	if item.Error != nil || result.IsError || item.Status == "failed" {
		reason := cmp.Or(strings.Join(output, "\n"), item.Status, "failed")
		if isWait {
			block.Body = "Wait failed"
			block.WaitTargets = nil
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
	if entry.native.thread == u.thread && entry.native.orchestrationWaitID != "" {
		if retained := u.session.orchestrationWaitResults[entry.native.orchestrationWaitID]; retained != nil {
			if retained.wait.State == "registered" {
				block.Live = true
			} else {
				*block = *retained.block
				entry.Text = block.ProgressText()
				return true
			}
		}
	}
	if len(output) > 0 {
		block.Output = u.session.outputs.New()
		block.Output.Finish(new(strings.Join(output, "\n")), nil)
	}
	return true
}

// A logical result can arrive before or after its MCP registration item, or
// after the user switches views. Match the subscription inside its owning root;
// the existing host item still owns the transcript row and its position.
func (u *appServerUI) observeOrchestrationWait(workspace, main string, wait orchestrate.Wait) {
	if u.thread != main || u.session.cwd != workspace || wait.ID == "" {
		return
	}
	block := &activityui.Block{Kind: "progress"}
	orchestrationWaitResult(block, wait)
	block.Live = wait.State == "registered"
	if block.Live {
		targets := wait.Targets
		if len(targets) == 0 {
			targets = []string{"all"}
		}
		for _, target := range targets {
			block.WaitTargets = append(block.WaitTargets, activityui.WaitTarget{Name: target})
		}
	}
	data, _ := json.Marshal(&wait)
	block.Output = u.session.outputs.New()
	block.Output.Finish(new(string(data)), nil)
	if u.session.orchestrationWaitResults == nil {
		u.session.orchestrationWaitResults = make(map[string]*orchestrationWaitView)
	}
	u.session.orchestrationWaitResults[wait.ID] = &orchestrationWaitView{wait: wait, block: block}
	u.dirty = true
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if view == nil {
			continue
		}
		for i, record := range view.entries {
			n := record.native
			if n == nil || n.thread != main || n.orchestrationWaitID != wait.ID {
				continue
			}
			entry, native := record.activityPaneEntry, *n
			native.orchestration = block
			entry.native, entry.Text = &native, block.ProgressText()
			view.replaceEntry(i, entry, parseLiveActivity(entry))
			u.dirty = true
		}
	}
}

// Selected targets describe a subscription, never the source of its result.
// Use only event metadata in the row; message and review payloads stay in the
// existing output dialog. This projection does not settle the logical wait.
func orchestrationWaitResult(block *activityui.Block, wait orchestrate.Wait) {
	if wait.State == "registered" {
		block.Body = appServerWaitProgress(appServerItem{Type: "collabAgentToolCall", Tool: "wait", waitTimeoutMS: uint64(wait.TimeoutMS)}, "item/started").Body
		return
	}
	block.WaitTargets = nil
	block.Body = "Wait ended (no event reported)"
	switch wait.State {
	case "canceled":
		block.Body = "Wait canceled"
		return
	case "delivered":
		block.Body = "Wait result delivered"
	case "dispatching", "uncertain":
		block.Body = "Wait delivery unconfirmed"
	case "rejected":
		block.Body = "Wait delivery failed"
	case "ready":
		block.Body = "Wait result pending delivery"
	}
	if wait.TimedOut {
		if wait.State == "completed" || wait.State == "delivered" {
			block.Body = "Wait timed out"
		} else {
			block.Body += " (timeout)"
		}
		return
	}
	var event orchestrateEvent
	if json.Unmarshal(wait.Event, &event) != nil || event.Kind == "" {
		return
	}
	if wait.State == "completed" || wait.State == "delivered" {
		block.Body = "Wait yielded"
	} else if block.Body == "Wait ended (no event reported)" {
		block.Body = "Wait result"
	}
	// Lifecycle events can precede the current turn or workflow generation.
	// Describe the recorded observation, never the batch's current state.
	reason := map[string]string{
		"message": "Message", "done": "Recorded completion", "failure": "Recorded failure",
		"blocked": "Recorded blocker", "question": "Question", "approval": "Approval",
	}[event.Kind]
	if reason == "" {
		reason = event.Kind
	}
	if event.Status != "" && event.Status != "completed" && event.Status != event.Kind {
		reason += " (" + strings.ReplaceAll(event.Status, "_", " ") + ")"
	}
	if event.Task == "" {
		block.Body += " · " + livediff.Safe(reason, false)
		return
	}
	block.WaitTargets = []activityui.WaitTarget{{Name: livediff.Safe(strings.TrimPrefix(event.Task, "/root/"), false), Status: livediff.Safe(reason, false)}}
}
