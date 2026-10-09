package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"

	"github.com/yusing/mekugi/internal/appserver"
)

type orchestrateEvent struct {
	main, workspace string
	Task            string         `json:"task_name"`
	Thread          string         `json:"thread_id,omitempty"`
	Turn            string         `json:"turn_id,omitempty"`
	Kind            string         `json:"kind"`
	Status          string         `json:"status,omitempty"`
	Request         jsontext.Value `json:"request_id,omitempty"`
	Item            string         `json:"item_id,omitempty"`
	Message         string         `json:"message,omitempty"`
}

// Wait state belongs to this live coordinator, not replay. A canceled waiter
// cannot consume an event that arrives after its cancellation.
func (u *appServerUI) waitOrchestratedEvent(command *orchestrateCommand) {
	u.orchestrateWaiters = slices.DeleteFunc(u.orchestrateWaiters, func(c *orchestrateCommand) bool { return c.ctx.Err() != nil })
	u.orchestrateWaiters = append(u.orchestrateWaiters, command)
	u.flushOrchestratedEvents()
}

func (u *appServerUI) publishOrchestratedEvent(child *orchestrateChild, event orchestrateEvent) {
	event.main, event.workspace = child.command.main, child.command.workspace
	u.orchestrateEvents = append(u.orchestrateEvents, event)
	u.flushOrchestratedEvents()
}

func (u *appServerUI) flushOrchestratedEvents() {
	for waiting := 0; waiting < len(u.orchestrateWaiters); {
		waiter := u.orchestrateWaiters[waiting]
		if waiter.ctx.Err() != nil {
			u.orchestrateWaiters = slices.Delete(u.orchestrateWaiters, waiting, waiting+1)
			continue
		}
		index := slices.IndexFunc(u.orchestrateEvents, func(e orchestrateEvent) bool {
			return e.main == waiter.main && e.workspace == waiter.workspace
		})
		if index < 0 {
			waiting++
			continue
		}
		event := u.orchestrateEvents[index]
		waiter.reply <- orchestrateResult{event: &event}
		u.orchestrateEvents = slices.Delete(u.orchestrateEvents, index, index+1)
		u.orchestrateWaiters = slices.Delete(u.orchestrateWaiters, waiting, waiting+1)
	}
}

// Prompt ownership and answering stay with the existing host/UI paths. This
// observer exposes only request identity and leaves the message untouched.
func (u *appServerUI) observeOrchestratedPrompt(m appserver.Message) {
	if len(u.orchestrateThreads) == 0 {
		return
	}
	kind := ""
	switch m.Method {
	case "item/completed":
		kind = "question"
	case "item/tool/requestUserInput":
		kind = "question"
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
		kind = "approval"
	default:
		return
	}
	// Read only prompt identity, not unrelated completed-item output.
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			ID        string           `json:"id"`
			Type      string           `json:"type"`
			Delivery  string           `json:"delivery"`
			Questions []jsontext.Value `json:"questions"`
		} `json:"item"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	item := ""
	if m.Method == "item/completed" {
		if p.Item.Type != "agentMessage" || p.Item.Delivery != "async" || len(p.Item.Questions) == 0 {
			return
		}
		item = p.Item.ID
	} else if len(m.ID) == 0 {
		return
	}
	// Native descendants belong to the independent batch's tree as well.
	root := p.ThreadID
	if u.orchestrateThreads[root] == nil {
		root = u.session.threadRoot(p.ThreadID)
	}
	if child := u.orchestrateThreads[root]; child != nil {
		u.publishOrchestratedEvent(child, orchestrateEvent{Task: child.batch.TaskName, Thread: p.ThreadID, Turn: p.TurnID, Kind: kind, Request: slices.Clone(m.ID), Item: item})
	}
}
