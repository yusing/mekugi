package coordination

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"strings"

	"github.com/yusing/mekugi/contracts/observation"
)

// Coordination is an observed operation, never authority to execute or deliver it.
// Confirmed distinguishes admitted traffic from queued and uncertain requests.
type Coordination struct {
	Action, Kind, Target, Thread, Message, State, Failure string
	Confirmed                                             bool
	Targets                                               []Target
	Count                                                 int
}

type Target struct{ Name, Status string }

func ProjectCoordination(item observation.Item) (Coordination, bool) {
	if item.Type != "mcpToolCall" || item.Server != "threadcontrol" {
		return Coordination{}, false
	}
	var c Coordination
	switch item.Tool {
	case "spawn_agent":
		c.Action, c.Kind = "Spawn", "start"
	case "followup_task":
		c.Action, c.Kind = "Follow-up", "assignment"
	case "send_message":
		c.Action, c.Kind = "Message", "reply"
	case "wait_agent":
		c.Action, c.Kind = "Wait", "progress"
	case "interrupt_agent":
		c.Action, c.Kind = "Interrupt", "progress"
	case "list_agents":
		c.Action, c.Kind = "List agents", "progress"
	default:
		return Coordination{}, false
	}
	var args struct {
		Task    string         `json:"task_name"`
		Message string         `json:"message"`
		Target  jsontext.Value `json:"target"`
		Cancel  *string        `json:"cancel_id"`
	}
	argumentsOK := json.Unmarshal(item.Arguments, &args) == nil
	c.Target, c.Message = args.Task, args.Message
	if item.Tool == "wait_agent" {
		var targets []string
		if args.Target.Kind() == '[' {
			_ = json.Unmarshal(args.Target, &targets)
		}
		if args.Cancel == nil && (len(args.Target) == 0 || args.Target.Kind() == '"') {
			targets = []string{"All descendants"}
		}
		for _, target := range targets {
			c.Targets = append(c.Targets, Target{Name: target})
		}
	} else if len(args.Target) != 0 {
		_ = json.Unmarshal(args.Target, &c.Target)
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	envelopeOK := json.Unmarshal(item.Result, &envelope) == nil
	if item.Status == "failed" || envelope.IsError || len(item.Error) > 0 && string(item.Error) != "null" {
		var failure struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(item.Error, &failure) == nil {
			c.Failure = failure.Message
		}
		if c.Failure == "" {
			_ = json.Unmarshal(item.Error, &c.Failure)
		}
		if c.Failure == "" {
			var text []string
			for _, content := range envelope.Content {
				if content.Type == "text" {
					text = append(text, content.Text)
				}
			}
			c.Failure = strings.TrimSpace(strings.Join(text, "\n"))
		}
		c.Failure = cmp.Or(c.Failure, "Operation failed")
		return c, true
	}
	c.State = "pending"
	if item.Status != "completed" {
		return c, true
	}
	c.State = "unconfirmed"
	if !argumentsOK || !envelopeOK || len(envelope.Content) != 1 || envelope.Content[0].Type != "text" {
		return c, true
	}
	var result struct {
		ID     string            `json:"id"`
		State  string            `json:"state"`
		Thread string            `json:"thread_id"`
		Task   string            `json:"task_name"`
		Agents *[]jsontext.Value `json:"agents"`
		Event  *struct {
			Task   string `json:"task"`
			Status string `json:"status"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(envelope.Content[0].Text), &result) != nil {
		return c, true
	}
	c.State = cmp.Or(result.State, c.State)
	switch item.Tool {
	case "spawn_agent":
		c.Confirmed = result.State == "confirmed" && result.Thread != "" && result.Task != ""
	case "followup_task", "send_message", "interrupt_agent":
		c.Confirmed = result.State == "confirmed" && result.ID != ""
	case "list_agents":
		c.Confirmed = result.Agents != nil
		if c.Confirmed {
			c.Count = len(*result.Agents)
			c.State = "completed"
		}
	case "wait_agent":
		c.Confirmed = result.ID != "" && slices.Contains([]string{"registered", "completed", "timeout", "canceled", "paused"}, result.State)
		if c.Confirmed && result.Event != nil {
			c.Targets = []Target{{Name: result.Event.Task, Status: result.Event.Status}}
		}
	}
	if c.Confirmed {
		c.Thread = result.Thread
		c.Target = cmp.Or(result.Task, c.Target)
	} else if c.State == "confirmed" {
		c.State = "unconfirmed"
	}
	return c, true
}
