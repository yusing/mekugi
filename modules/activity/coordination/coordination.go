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
	Action, Kind, Target, Thread, Message, State, ID, Failure, EventKind string
	TimeoutMS                                                            *int64
	Confirmed                                                            bool
	Targets                                                              []Target
	Count                                                                int
	Answer                                                               string
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
	case "compact":
		c.Action, c.Kind = "Compact", "progress"
	case "wait_agent":
		c.Action, c.Kind = "Wait", "progress"
	case "interrupt_agent":
		c.Action, c.Kind = "Interrupt", "progress"
	case "list_agents":
		c.Action, c.Kind = "List agents", "progress"
	case "list_models":
		c.Action, c.Kind = "List models", "progress"
	case "inspect":
		c.Action, c.Kind = "Inspect", "progress"
	default:
		return Coordination{}, false
	}
	var args struct {
		Task    string         `json:"task_name"`
		Message string         `json:"message"`
		Target  jsontext.Value `json:"target"`
		Timeout *int64         `json:"timeout_ms"`
		BTW     bool           `json:"btw"`
	}
	argumentsOK := json.Unmarshal(item.Arguments, &args) == nil
	c.Target, c.Message = args.Task, args.Message
	if item.Tool == "send_message" && args.BTW {
		c.Action, c.Kind = "Side question", "side-question"
	}
	if item.Tool == "wait_agent" {
		c.TimeoutMS = args.Timeout
		var targets []string
		if args.Target.Kind() == '[' {
			_ = json.Unmarshal(args.Target, &targets)
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
		IsError           bool           `json:"isError"`
		StructuredContent jsontext.Value `json:"structuredContent"`
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
	if !argumentsOK || !envelopeOK {
		return c, true
	}
	// Successful waits deliberately return an empty content array while the
	// logical subscription remains active. Missing/null content is not an ack.
	if item.Tool == "wait_agent" && envelope.Content != nil && len(envelope.Content) == 0 && (len(envelope.StructuredContent) == 0 || string(envelope.StructuredContent) == "null") {
		c.State, c.Confirmed = "waiting", true
		return c, true
	}
	if len(envelope.Content) != 1 || envelope.Content[0].Type != "text" {
		return c, true
	}
	return projectResult(c, item.Tool, args.BTW, []byte(envelope.Content[0].Text)), true
}

// ProjectWaitOutcome decodes a separately delivered, host-admitted wake output.
// Callers retain responsibility for namespace and delivery provenance.
func ProjectWaitOutcome(output []byte) Coordination {
	return projectResult(Coordination{Action: "Wait", Kind: "progress", State: "unconfirmed"}, "wait_agent", false, output)
}

func projectResult(c Coordination, tool string, btw bool, output []byte) Coordination {
	type outcome struct {
		Kind   string `json:"kind"`
		Task   string `json:"task"`
		Status string `json:"status"`
	}
	var result struct {
		Targets     []string          `json:"targets"`
		EffectiveMS *int64            `json:"effective_ms"`
		ID          string            `json:"id"`
		State       string            `json:"state"`
		Thread      string            `json:"thread_id"`
		Task        string            `json:"task_name"`
		Answer      *string           `json:"answer"`
		Agents      *[]jsontext.Value `json:"agents"`
		Models      *[]jsontext.Value `json:"models"`
		Event       *outcome          `json:"event"`
		Events      []outcome         `json:"events"`
	}
	if json.Unmarshal(output, &result) != nil {
		return c
	}
	if tool == "wait_agent" && result.State == "registered" {
		result.State = "waiting"
	}
	// Older checkpoints used a scheduling acknowledgement for the same work.
	if (tool == "compact" || tool == "wait_agent") && result.State == "scheduled" {
		result.State = "compacting"
	}
	c.State = cmp.Or(result.State, c.State)
	c.ID = result.ID
	switch tool {
	case "spawn_agent":
		c.Confirmed = result.State == "confirmed" && result.Thread != "" && result.Task != ""
	case "send_message":
		if btw {
			c.Confirmed = result.Thread != "" && result.Task != "" && result.Answer != nil
			if c.Confirmed {
				c.State, c.Answer = "answered", *result.Answer
			}
		} else {
			c.Confirmed = result.State == "confirmed" && result.ID != ""
		}
	case "followup_task", "interrupt_agent":
		c.Confirmed = result.State == "confirmed" && result.ID != ""
	case "list_agents":
		c.Confirmed = result.Agents != nil
		if c.Confirmed {
			c.Count = len(*result.Agents)
			c.State = "completed"
		}
	case "list_models":
		c.Confirmed = result.Models != nil
		if c.Confirmed {
			c.Count = len(*result.Models)
			c.State = "completed"
		}
	case "inspect":
		c.Confirmed = result.Thread != "" && result.Task != ""
		if c.Confirmed {
			c.State = "completed"
		}
	case "compact":
		c.Confirmed = result.State == "compacting"
	case "wait_agent":
		c.Confirmed = slices.Contains([]string{"compacting", "waiting", "completed", "timeout", "canceled", "paused", "yielded", "failed"}, result.State)
		if c.Confirmed && len(result.Targets) > 0 {
			c.Targets = nil
			for _, target := range result.Targets {
				c.Targets = append(c.Targets, Target{Name: target})
			}
		}
		if result.EffectiveMS != nil {
			c.TimeoutMS = result.EffectiveMS
		}
		if c.Confirmed && len(result.Events) != 0 {
			c.Targets = nil
			for _, event := range result.Events {
				status := event.Status
				if event.Kind == "delivery_failed" {
					status = "delivery failed"
				}
				c.Targets = append(c.Targets, Target{Name: event.Task, Status: status})
			}
		}
		if c.Confirmed && result.Event != nil {
			c.EventKind = result.Event.Kind
			status := result.Event.Status
			if result.Event.Kind == "delivery_failed" {
				status = "delivery failed"
			}
			c.Targets = []Target{{Name: result.Event.Task, Status: status}}
		}
	}
	if c.Confirmed {
		c.Thread = result.Thread
		c.Target = cmp.Or(result.Task, c.Target)
	} else if c.State == "confirmed" {
		c.State = "unconfirmed"
	}
	return c
}
