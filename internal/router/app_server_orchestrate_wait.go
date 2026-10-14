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

// Registration belongs to the durable run, not the MCP request lifetime.
func (u *appServerUI) waitOrchestratedEvent(command *orchestrateCommand) {
	u.flushOrchestratedEvents()
	store := u.proxy.orchestration.store
	now := u.now()
	u.orchestrateWork(func() func() {
		var w orchestrate.Wait
		var err error
		if command.waitCancel != "" {
			w, err = store.UpdateWait(command.ctx, command.workspace, command.main, command.waitCancel, "canceled", "")
		} else {
			w, err = store.RegisterWait(command.ctx, command.workspace, command.main, orchestrate.Wait{ID: command.callID, Turn: command.waitTurn, Item: command.waitItem, Targets: command.waitTargets, TimeoutMS: command.waitTimeout}, now)
		}
		return func() {
			command.reply <- orchestrateResult{wait: &w, err: err}
			if err == nil {
				if w.State == "registered" || w.State == "canceled" {
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
	u.orchestrateEvents = slices.DeleteFunc(u.orchestrateEvents, func(event orchestrateEvent) bool {
		return event.main != u.thread || event.workspace != u.session.cwd
	})
	events := u.orchestrateEvents
	dueAt := u.orchestrateWaitDeadline
	if !u.orchestrateWaitRetry.IsZero() {
		if u.now().Before(u.orchestrateWaitRetry) {
			return
		}
		dueAt = u.orchestrateWaitRetry
	}
	due := !dueAt.IsZero() && !u.now().Before(dueAt)

	u.orchestrateEvents = nil
	// Acceptance is a live lifecycle trigger, not a replayable wait event.
	events = slices.DeleteFunc(events, func(event orchestrateEvent) bool {
		if event.Kind != "accepted" {
			return false
		}
		u.cleanAcceptedOrchestratedChild(event)
		return true
	})
	if len(events) != 0 || due {
		u.orchestrateWaitDeadline, u.orchestrateWaitRetry = time.Time{}, time.Time{}
		u.advanceOrchestratedWaits(u.session.cwd, u.thread, events)
	}
}

func (u *appServerUI) advanceOrchestratedWaits(workspace, main string, events []orchestrateEvent) {
	if workspace != u.session.cwd || main != u.thread {
		return
	}
	store := u.proxy.orchestration.store
	now := u.now()
	generation, stop := u.orchestrateGeneration, u.orchestrateWaitStop
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
		if err != nil {
			ready = nil
		}
		return func() {
			current := u.orchestrateGeneration == generation && !u.orchestrateClosing
			if current {
				u.orchestrateWaitDeadline = deadline
				if err != nil {
					u.orchestrateEvents = append(events, u.orchestrateEvents...)
					u.orchestrateWaitRetry = u.now().Add(time.Second)
					if u.orchestrateStorageErr == nil {
						u.orchestrateStorageErr = err
					}
					u.setNotice("Orchestration wait: "+err.Error(), true)
				} else {
					u.orchestrateWaitRetry = time.Time{}
				}
			}
			for _, result := range ready {
				request := orchestrateRPC{method: "turn/start", wait: &result, waitMain: main, waitWorkspace: workspace}
				if !current || stop != u.orchestrateWaitStop {
					// No host call occurred; retain or cancel the selected result.
					request.waitCanceled = stop != u.orchestrateWaitStop
					u.recordOrchestratedWait(request, "ready", "")
					continue
				}
				notification := result
				notification.State = "completed"
				payload, _ := json.Marshal(&notification)
				text := "Message Type: WAIT_RESULT\nTask name: /root\nSender: orchestrate\nPayload:\n" + string(payload)
				if err := u.orchestrateRPCRequest(request, map[string]any{"threadId": main, "input": []any{}, "toolOutput": map[string]any{"namespace": "orchestrate", "name": "wait_agent", "output": text, "retain": true}}); err != nil {
					u.recordOrchestratedWait(request, "uncertain", err.Error())
				}
			}
		}
	})
}

func (u *appServerUI) recordOrchestratedWait(r orchestrateRPC, state, message string) {
	store := u.proxy.orchestration.store
	generation := u.orchestrateGeneration
	u.orchestrateWork(func() func() {
		wait, err := store.UpdateWait(context.WithoutCancel(u.ctx), r.waitWorkspace, r.waitMain, r.wait.ID, state, message)
		if err == nil && r.waitCanceled {
			wait, err = store.UpdateWait(context.WithoutCancel(u.ctx), r.waitWorkspace, r.waitMain, r.wait.ID, "canceled", "")
		}
		return func() {
			if u.orchestrateGeneration != generation || u.thread != r.waitMain || u.session.cwd != r.waitWorkspace {
				return
			}
			if err != nil {
				u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
				u.setNotice("Orchestration wait acknowledgement: "+err.Error(), true)
			} else if message != "" {
				u.setNotice("Orchestration wait delivery: "+message, true)
			}
			if err == nil {
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

// Human input supersedes unsent waits without interrupting any child. Storage
// stays on the existing worker; confirmed cancellation updates the owning row.
func (u *appServerUI) stopOrchestratedWaits() {
	if u.proxy == nil || u.proxy.orchestration == nil {
		return
	}
	u.orchestrateWaitStop++
	store, workspace, main := u.proxy.orchestration.store, u.session.cwd, u.thread
	u.orchestrateWork(func() func() {
		waits, err := store.StopWaits(context.WithoutCancel(u.ctx), workspace, main)
		return func() {
			if err != nil {
				u.setNotice("Orchestration wait cancellation: "+err.Error(), true)
				return
			}
			for _, wait := range waits {
				u.observeOrchestrationWait(workspace, main, wait)
			}
		}
	})
}
