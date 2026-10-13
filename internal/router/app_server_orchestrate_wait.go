package router

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestrateEvent struct {
	main, workspace, directory string
	ID                         string         `json:"event_id,omitempty"`
	Task                       string         `json:"task_name"`
	Thread                     string         `json:"thread_id,omitempty"`
	Turn                       string         `json:"turn_id,omitempty"`
	Kind                       string         `json:"kind"`
	Status                     string         `json:"status,omitempty"`
	Request                    jsontext.Value `json:"request_id,omitempty"`
	Item                       string         `json:"item_id,omitempty"`
	Message                    string         `json:"message,omitempty"`
	Path                       string         `json:"path,omitempty"`
}

type orchestrateWaitRun struct{ workspace, main string }
type orchestrateWaitSchedule struct {
	deadline, retry time.Time
	pending         int
}

// Registration belongs to the durable run, not the MCP request lifetime.
func (u *appServerUI) waitOrchestratedEvent(command *orchestrateCommand) {
	u.flushOrchestratedEvents()
	store := u.proxy.orchestration.store
	now := u.now()
	u.orchestrateWork(func() func() {
		var w orchestrate.Wait
		var err error
		if command.waitCancel != "" {
			w, err = store.CancelWait(command.ctx, command.workspace, command.main, command.callID, command.waitCancel)
		} else {
			w, err = store.RegisterWait(command.ctx, command.workspace, command.main, orchestrate.Wait{ID: command.callID, Targets: command.waitTargets, TimeoutMS: command.waitTimeout}, now)
		}
		return func() {
			command.reply <- orchestrateResult{wait: &w, err: err}
			if err == nil {
				if command.waitCancel != "" && w.State == "canceled" {
					u.observeOrchestrationWait(command.workspace, command.main, w)
				}
				u.advanceOrchestratedWaits(command.workspace, command.main, nil)
			}
		}
	})
}

func (u *appServerUI) publishOrchestratedEvent(child *orchestrateChild, event orchestrateEvent) {
	u.flushOrchestratedEvents()
	event.main, event.workspace = child.command.main, child.command.workspace
	u.orchestrateEvents = append(u.orchestrateEvents, event)
	u.flushOrchestratedEvents()
}

func (u *appServerUI) flushOrchestratedEvents() {
	if u.proxy == nil || u.proxy.orchestration == nil || u.orchestrateClosing {
		return
	}
	runtime := u.proxy.orchestration
	runtime.eventsMu.Lock()
	u.orchestrateEvents = append(u.orchestrateEvents, runtime.journalEvents...)
	runtime.journalEvents = nil
	runtime.eventsMu.Unlock()
	events := u.orchestrateEvents
	for run, schedule := range u.orchestrateWaitRuns {
		if schedule.pending != 0 {
			continue
		}
		due := schedule.deadline
		if !schedule.retry.IsZero() {
			due = schedule.retry
		}
		if !due.IsZero() && !u.now().Before(due) {
			u.advanceOrchestratedWaits(run.workspace, run.main, nil)
		}
	}

	u.orchestrateEvents = nil
	// Acceptance is a live lifecycle trigger, not a replayable wait event.
	events = slices.DeleteFunc(events, func(event orchestrateEvent) bool {
		if event.Kind != "accepted" {
			return false
		}
		u.cleanAcceptedOrchestratedChild(event)
		return true
	})
	// Group by retained run identity. A journal may belong to a hidden view.
	for len(events) != 0 {
		first := events[0]
		var selected, rest []orchestrateEvent
		for _, e := range events {
			if e.main == first.main && e.workspace == first.workspace {
				selected = append(selected, e)
			} else {
				rest = append(rest, e)
			}
		}
		schedule := u.orchestrateWaitRuns[orchestrateWaitRun{first.workspace, first.main}]
		if schedule != nil && !schedule.retry.IsZero() && u.now().Before(schedule.retry) {
			u.orchestrateEvents = append(u.orchestrateEvents, selected...)
		} else {
			u.advanceOrchestratedWaits(first.workspace, first.main, selected)
		}
		events = rest
	}
}

func (u *appServerUI) orchestratedWaitSchedule(workspace, main string) *orchestrateWaitSchedule {
	if u.orchestrateWaitRuns == nil {
		u.orchestrateWaitRuns = make(map[orchestrateWaitRun]*orchestrateWaitSchedule)
	}
	run := orchestrateWaitRun{workspace, main}
	schedule := u.orchestrateWaitRuns[run]
	if schedule == nil {
		schedule = &orchestrateWaitSchedule{}
		u.orchestrateWaitRuns[run] = schedule
	}
	return schedule
}

func (u *appServerUI) advanceOrchestratedWaits(workspace, main string, events []orchestrateEvent) {
	store := u.proxy.orchestration.store
	now := u.now()
	schedule := u.orchestratedWaitSchedule(workspace, main)
	schedule.pending++
	u.orchestrateWork(func() func() {
		retained := make([]orchestrate.WaitEvent, 0, len(events))
		for _, e := range events {
			data, _ := json.Marshal(&e)
			id := e.ID
			if id == "" {
				id = fmt.Sprintf("%x", sha256.Sum256(data))
			}
			retained = append(retained, orchestrate.WaitEvent{ID: id, Task: e.Task, Data: data, Prompt: e.Kind == "question" || e.Kind == "approval"})
		}
		ready, deadline, err := store.AdvanceWaits(context.WithoutCancel(u.ctx), workspace, main, retained, now)
		return func() {
			schedule.pending--
			if err != nil {
				u.orchestrateEvents = append(events, u.orchestrateEvents...)
				schedule.retry = u.now().Add(time.Second)
				if u.orchestrateStorageErr == nil {
					u.orchestrateStorageErr = err
				}
				u.setNotice("Orchestration wait: "+err.Error(), true)
				return
			}
			schedule.deadline, schedule.retry = deadline, time.Time{}
			for _, w := range ready {
				u.deliverOrchestratedWait(workspace, main, w)
			}
		}
	})
}

func (u *appServerUI) deliverOrchestratedWait(workspace, main string, w orchestrate.Wait) {
	if u.orchestrateClosing {
		return
	}
	store := u.proxy.orchestration.store
	u.orchestrateWork(func() func() {
		result, dispatch, err := store.ClaimWaitDelivery(context.WithoutCancel(u.ctx), workspace, main, w.ID)
		return func() {
			if err != nil {
				u.orchestratedWaitSchedule(workspace, main).retry = u.now().Add(time.Second)
				if u.orchestrateStorageErr == nil {
					u.orchestrateStorageErr = err
				}
				u.setNotice("Orchestration wait: "+err.Error(), true)
				return
			}
			if !dispatch {
				return
			}
			if u.orchestrateClosing {
				// No host call was attempted. Release only this known-unsent claim.
				u.recordOrchestratedWait(orchestrateRPC{wait: &result, waitMain: main, waitWorkspace: workspace}, "ready", "")
				return
			}
			notification := result
			notification.State = "completed"
			payload, _ := json.Marshal(&notification)
			text := "Message Type: WAIT_RESULT\nTask name: /root\nSender: orchestrate\nPayload:\n" + string(payload)
			request := orchestrateRPC{method: "thread/inject_items", wait: &result, waitMain: main, waitWorkspace: workspace}
			err := u.orchestrateRPCRequest(request, map[string]any{"threadId": main, "items": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}})
			if err != nil {
				u.recordOrchestratedWait(request, "uncertain", err.Error())
			}
		}
	})
}

func (u *appServerUI) recordOrchestratedWait(r orchestrateRPC, state, message string) {
	store := u.proxy.orchestration.store
	u.orchestrateWork(func() func() {
		err := store.RecordWaitDelivery(context.WithoutCancel(u.ctx), r.waitWorkspace, r.waitMain, r.wait.ID, state, message)
		return func() {
			if err != nil {
				u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
				u.setNotice("Orchestration wait acknowledgement: "+err.Error(), true)
			} else if message != "" {
				u.setNotice("Orchestration wait delivery: "+message, true)
			}
			if err == nil {
				wait := *r.wait
				wait.State, wait.Error = state, message
				u.observeOrchestrationWait(r.waitWorkspace, r.waitMain, wait)
			}
		}
	})
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
