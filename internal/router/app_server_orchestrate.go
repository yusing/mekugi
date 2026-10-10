package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

// The MCP workers enqueue work; only the UI loop writes to the host client.
type orchestrateRuntime struct {
	store         *orchestrate.Store
	commands      chan *orchestrateCommand
	active        atomic.Bool
	eventsMu      sync.Mutex
	journalEvents []orchestrateEvent
}

type orchestrateCommand struct {
	ctx             context.Context
	workspace, main string
	input           orchestrateSpawnInput
	target          string
	wait            bool
	followup        bool
	deferred        bool
	removeCheckout  bool
	caller, callID  string
	reply           chan orchestrateResult
}

type orchestrateResult struct {
	batch    orchestrate.Batch
	err      error
	event    *orchestrateEvent
	delivery *orchestrate.Delivery
}
type orchestrateRPC struct {
	method   string
	child    *orchestrateChild
	cleanup  bool
	followup *orchestrateCommand
	delivery orchestrate.Delivery
}
type orchestrateChild struct {
	command       *orchestrateCommand
	batch         orchestrate.Batch
	turn          string
	completedTurn string
	followups     []*orchestrateCommand
	removing      bool
}

func (u *appServerUI) orchestrateCommands() <-chan *orchestrateCommand {
	if u.proxy == nil || u.proxy.orchestration == nil {
		return nil
	}
	return u.proxy.orchestration.commands
}

func (u *appServerUI) startOrchestratedChild(command *orchestrateCommand) {
	fail := func(err error) { command.reply <- orchestrateResult{err: err} }
	if err := command.ctx.Err(); err != nil {
		fail(err)
		return
	}
	if command.followup {
		u.queueOrchestratedFollowup(command)
		return
	}
	if command.main != u.thread || command.workspace != u.session.cwd {
		fail(errors.New("orchestration caller is not the active coordinator"))
		return
	}
	if command.wait {
		u.waitOrchestratedEvent(command)
		return
	}
	if command.removeCheckout {
		u.cleanOrchestratedChild(command)
		return
	}
	if command.target != "" {
		for _, child := range u.orchestrateThreads {
			if (command.target == child.batch.TaskName || command.target == "/root/"+child.batch.TaskName) && child.command.main == command.main && child.command.workspace == command.workspace {
				if child.turn == "" {
					command.reply <- orchestrateResult{batch: child.batch}
					return
				}
				request := *child
				request.command = command
				if err := u.orchestrateRequest(&request, "turn/interrupt", map[string]any{"threadId": child.batch.Launch.ThreadID, "turnId": child.turn}); err != nil {
					fail(err)
				}
				return
			}
		}
		fail(errors.New("target is not a live orchestration child"))
		return
	}
	store := u.proxy.orchestration.store
	u.orchestrateWork(func() func() {
		batches, err := store.List(command.ctx, command.workspace, command.main)
		return func() {
			if u.orchestrateClosing {
				fail(context.Canceled)
				return
			}
			if err != nil {
				fail(err)
				return
			}
			var batch orchestrate.Batch
			for _, b := range batches {
				if b.TaskName == command.input.TaskName {
					batch = b
					break
				}
			}
			if batch.TaskName == "" {
				fail(errors.New("prepare this batch before spawning"))
				return
			}
			params, err := u.orchestrateThreadRequest(batch, command.input)
			if err != nil {
				fail(err)
				return
			}
			encoded, err := json.Marshal(params)
			if err != nil {
				fail(err)
				return
			}
			u.orchestrateWork(func() func() {
				batch, dispatch, err := store.BeginLaunch(command.ctx, command.workspace, command.main, batch.TaskName, command.input.Message, encoded)
				return func() {
					if err != nil || !dispatch {
						command.reply <- orchestrateResult{batch: batch, err: err}
						return
					}
					child := &orchestrateChild{command: command, batch: batch}
					if u.orchestrateClosing {
						u.finishOrchestrate(child, context.Canceled)
						return
					}
					if err := command.ctx.Err(); err != nil {
						u.failOrchestrate(child, err)
						return
					}
					if err := u.orchestrateRequest(child, "thread/start", params); err != nil {
						u.failOrchestrate(child, err)
					}
				}
			})
		}
	})
}

// Storage effects run in event order. Only their completions touch UI state or
// dispatch host requests; no UI callback waits for a run lock or Git process.
func (u *appServerUI) orchestrateWork(work func() func()) {
	if u.orchestrateCompletions == nil {
		u.orchestrateStorageContext, u.orchestrateStorageCancel = context.WithCancel(context.WithoutCancel(u.ctx))
		u.orchestrateCompletions = make(chan func(), 1)
	}
	u.orchestrateJobs = append(u.orchestrateJobs, work)
	if len(u.orchestrateJobs) == 1 {
		u.runOrchestrateWork()
	}
}
func (u *appServerUI) runOrchestrateWork() {
	work, ctx, completed := u.orchestrateJobs[0], u.orchestrateStorageContext, u.orchestrateCompletions
	go func() {
		result := work()
		select {
		case completed <- result:
		case <-ctx.Done():
		}
	}()
}
func (u *appServerUI) completeOrchestrateWork(complete func()) {
	// Keep this job reserved during its callback, which can append dependent work.
	complete()
	u.orchestrateJobs[0] = nil
	u.orchestrateJobs = u.orchestrateJobs[1:]
	if len(u.orchestrateJobs) != 0 {
		u.runOrchestrateWork()
	}
	u.dirty = true
}

func (u *appServerUI) orchestrateRequest(child *orchestrateChild, method string, params any) error {
	return u.orchestrateRPCRequest(orchestrateRPC{method: method, child: child}, params)
}

func (u *appServerUI) orchestrateRPCRequest(request orchestrateRPC, params any) error {
	if u.orchestrateClosing {
		return context.Canceled
	}
	id, err := u.client.Send(request.method, params, true)
	if err != nil {
		return err
	}
	if u.orchestrateRequests == nil {
		u.orchestrateRequests = make(map[string]orchestrateRPC)
	}
	u.orchestrateRequests[id] = request
	return nil
}

func (u *appServerUI) finishOrchestrate(child *orchestrateChild, err error) {
	if err != nil && u.navigation != nil {
		if v := u.navigation.views[child.batch.Launch.ThreadID]; v != nil && v.awaitingTurn && child.turn == "" {
			v.awaitingTurn = false
			v.setNotice("Orchestration startup: "+err.Error(), true)
		}
	}
	select {
	case child.command.reply <- orchestrateResult{batch: child.batch, err: err}:
	default:
	}
}

func (u *appServerUI) failOrchestrate(child *orchestrateChild, err error) {
	c, store, ctx, name := child.command, u.proxy.orchestration.store, u.orchestrateStorageContext, child.batch.TaskName
	u.orchestrateWork(func() func() {
		persistErr := store.ObserveLaunch(ctx, c.workspace, c.main, name, "failed", err.Error())
		return func() {
			u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, persistErr)
			err := errors.Join(err, persistErr)
			u.finishOrchestrate(child, err)
			status := "failed"
			if persistErr != nil {
				status = "storage_failed"
			}
			u.publishOrchestratedEvent(child, orchestrateEvent{Task: name, Thread: child.batch.Launch.ThreadID, Kind: "failure", Status: status, Message: err.Error()})
			u.setNotice("Orchestration "+name+": "+err.Error(), true)
		}
	})
}

func (u *appServerUI) orchestrateMessage(m appserver.Message) bool {
	u.observeOrchestratedPrompt(m)
	if m.Method != "" {
		if len(u.orchestrateThreads) == 0 || m.Method != "turn/started" && m.Method != "turn/completed" {
			return false
		}
		var event appServerEvent
		if json.Unmarshal(m.Params, &event) != nil {
			return false
		}
		child := u.orchestrateThreads[event.ThreadID]
		if child == nil {
			return false
		}
		status := ""
		switch m.Method {
		case "turn/started":
			child.turn, status = event.Turn.ID, "running"
		case "turn/completed":
			child.turn, status = "", event.Turn.Status
			child.completedTurn = event.Turn.ID
		}
		if status != "" {
			child.batch.Launch.HostStatus = status
			c, store, ctx, name := child.command, u.proxy.orchestration.store, u.orchestrateStorageContext, child.batch.TaskName
			message := ""
			if event.Turn.Error != nil {
				message = event.Turn.Error.Message
			}
			u.orchestrateWork(func() func() {
				err := store.ObserveLaunch(ctx, c.workspace, c.main, name, status, message)
				return func() {
					if err != nil {
						u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
						u.setNotice("Orchestration lifecycle: "+err.Error(), true)
						u.publishOrchestratedEvent(child, orchestrateEvent{Task: name, Thread: event.ThreadID, Turn: event.Turn.ID, Kind: "failure", Status: "storage_failed", Message: err.Error()})
					} else if m.Method == "turn/completed" {
						kind := "done"
						if status == "failed" {
							kind = "failure"
						}
						u.publishOrchestratedEvent(child, orchestrateEvent{Task: name, Thread: event.ThreadID, Turn: event.Turn.ID, Kind: kind, Status: status, Message: message})
					}
				}
			})
		}
		return false // Existing question, approval and activity owners consume events.
	}
	r, ok := u.orchestrateRequests[string(m.ID)]
	if !ok {
		return false
	}
	delete(u.orchestrateRequests, string(m.ID))
	u.dirty = true
	if r.followup != nil {
		u.orchestratedFollowupResponse(r, m)
		return true
	}
	child, c := r.child, r.child.command
	if r.method == "turn/interrupt" {
		var err error
		if m.Error != nil {
			err = fmt.Errorf("turn/interrupt: %s", m.Error.Message)
			u.setNotice(err.Error(), true)
		}
		if !r.cleanup {
			u.finishOrchestrate(child, err)
		}
		return true
	}
	if m.Error != nil {
		u.failOrchestrate(child, fmt.Errorf("%s: %s", r.method, m.Error.Message))
		return true
	}
	store := u.proxy.orchestration.store
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		u.failOrchestrate(child, err)
		return true
	}
	if r.method == "thread/start" {
		if result.Thread.Cwd != child.batch.Cwd {
			u.failOrchestrate(child, errors.New("host returned a different child cwd"))
			return true
		}
		child.batch.Launch.ThreadID = result.Thread.ID
		child.batch.State, child.batch.Launch.ThreadResult = "started", m.Result
		if u.orchestrateThreads == nil {
			u.orchestrateThreads = make(map[string]*orchestrateChild)
		}
		u.orchestrateThreads[result.Thread.ID] = child
		result.Thread.AgentNickname = child.batch.TaskName
		u.session.roots[result.Thread.ID] = "/orchestrate/" + result.Thread.ID
		u.registerSessionThread(result.Thread)
		ctx, name, cwd, thread, raw := u.orchestrateStorageContext, child.batch.TaskName, child.batch.Cwd, result.Thread.ID, m.Result
		journals, replay := u.proxy.journals, u.proxy.replayStore
		u.orchestrateWork(func() func() {
			err := store.RecordThread(ctx, c.workspace, c.main, name, thread, raw)
			if err == nil {
				var release func()
				ctx, release, err = replay.beginSession(ctx, thread, "")
				if err == nil {
					defer release()
					err = journals.initialize(ctx, replay, cwd, thread, "/root", "")
					if err == nil {
						err = journals.bindIdentity(ctx, replay, cwd, thread, "", "/root", true)
					}
					if err == nil {
						run := journalRun{Directory: store.Directory, Workspace: c.workspace, Main: c.main}
						err = journals.bindRun(ctx, replay, c.workspace, c.main, run)
						if err == nil {
							err = journals.bindRun(ctx, replay, cwd, thread, run)
						}
					}
				}
			}
			return func() {
				if err != nil {
					u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
					u.failOrchestrate(child, err)
					return
				}
				if u.orchestrateClosing {
					u.finishOrchestrate(child, context.Canceled)
					return
				}
				if err := c.ctx.Err(); err != nil {
					u.finishOrchestrate(child, err)
					return
				}
				if err := u.addOrchestratedView(child, raw, func() {
					if u.orchestrateClosing || c.ctx.Err() != nil {
						u.finishOrchestrate(child, context.Canceled)
						return
					}
					input := orchestrateChildInput(cwd, c.input.Message)
					if len(child.batch.Evidence) != 0 {
						paths := make(map[string]string, len(child.batch.Evidence))
						for _, e := range child.batch.Evidence {
							paths[e.Name] = e.Path
						}
						encoded, _ := json.Marshal(paths, json.Deterministic(true))
						input = "Evidence files: " + string(encoded) + "\n" + input
					}
					if err := u.orchestrateRequest(child, "turn/start", map[string]any{"threadId": thread, "input": appserver.Input(input)}); err != nil {
						u.failOrchestrate(child, err)
					}
				}); err != nil {
					u.failOrchestrate(child, err)
				}
			}
		})
	} else {
		if result.Turn.ID == "" {
			u.failOrchestrate(child, errors.New("host returned no turn identity"))
			return true
		}
		child.batch.Launch.TurnID = result.Turn.ID
		if child.batch.Launch.HostStatus == "" || child.batch.Launch.HostStatus == "running" {
			child.turn = result.Turn.ID
		}
		if u.navigation != nil && child.turn == result.Turn.ID {
			if v := u.navigation.views[child.batch.Launch.ThreadID]; v != nil && v.awaitingTurn {
				v.started(result.Turn.ID)
				v.status, v.turnStarted = "Working", v.now()
			}
		}
		// Cancellation cleanup does not wait for acknowledgement persistence.
		interrupted := c.ctx.Err() != nil
		var interruptErr error
		if interrupted {
			interruptErr = u.orchestrateCleanupInterrupt(child, child.batch.Launch.ThreadID, result.Turn.ID)
		}
		ctx, name, thread, turn := u.orchestrateStorageContext, child.batch.TaskName, child.batch.Launch.ThreadID, result.Turn.ID
		u.orchestrateWork(func() func() {
			persistErr := errors.Join(store.RecordTurn(ctx, c.workspace, c.main, name, thread, turn), interruptErr)
			return func() {
				if c.ctx.Err() != nil && !interrupted && child.turn != "" && !u.orchestrateClosing {
					// Cancellation may arrive while the storage worker waits.
					if err := u.orchestrateCleanupInterrupt(child, thread, turn); err != nil {
						persistErr = errors.Join(persistErr, err)
					}
				}
				if persistErr != nil {
					u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, persistErr)
					u.finishOrchestrate(child, persistErr)
					u.setNotice("Orchestration outcome was not retained: "+persistErr.Error(), true)
					return
				}
				child.batch.State = "launched"
				u.finishOrchestrate(child, c.ctx.Err())
			}
		})
	}
	return true
}

func (u *appServerUI) orchestrateBusy() bool {
	u.cancelOrchestratedMainFollowups()
	if len(u.orchestrateMainFollowups) != 0 || u.orchestrateMainInput != nil {
		return true
	}
	if n := u.navigation; n != nil {
		if n.owner != u {
			return n.owner.busy() || n.owner.reset.active() || n.owner.orchestrateBusy()
		}
		if len(n.pending) != 0 || len(n.reads) != 0 || len(n.resumes) != 0 {
			return true
		}
		for _, v := range n.views {
			if v != u && (v.busy() || len(v.activeChildren) != 0 || v.reset.active() || len(v.btwRequests) != 0 || v.btw != nil && (v.btw.busy || v.btw.starting || v.btw.interrupting)) {
				return true
			}
		}
	}
	if len(u.orchestrateRequests) != 0 || len(u.orchestrateJobs) != 0 {
		return true
	}
	for _, child := range u.orchestrateThreads {
		if child.turn != "" || len(child.followups) != 0 {
			return true
		}
	}
	for thread, path := range u.session.paths {
		root := u.session.threadRoot(thread)
		if u.orchestrateThreads[root] != nil || root == "" && len(u.orchestrateThreads) != 0 {
			if agent := u.session.agent(path); agent != nil && agent.Responding {
				return true
			}
		}
	}
	return false
}

// Cleanup interrupts never publish the spawn result; its storage completion owns it.
func (u *appServerUI) orchestrateCleanupInterrupt(child *orchestrateChild, thread, turn string) error {
	if err := u.orchestrateRequest(child, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}); err != nil {
		return err
	}
	for id, r := range u.orchestrateRequests {
		if r.child == child && r.method == "turn/interrupt" {
			r.cleanup = true
			u.orchestrateRequests[id] = r
		}
	}
	return nil
}

func (u *appServerUI) closeOrchestrateStorage() error {
	u.orchestrateClosing = true
	for _, c := range u.orchestrateMainFollowups {
		c.reply <- orchestrateResult{err: context.Canceled}
	}
	u.orchestrateMainFollowups = nil
	if pending := u.orchestrateMainInput; pending != nil && pending.dispatched {
		pending.dispatched = false
		u.retainOrchestratedMainInput(pending, "uncertain", "", errors.New("coordinator delivery was not acknowledged before shutdown"))
	}
	for _, waiter := range u.orchestrateWaiters {
		waiter.reply <- orchestrateResult{err: context.Canceled}
	}
	u.orchestrateWaiters = nil
	if u.orchestrateStorageCancel == nil {
		return nil
	}
	defer u.orchestrateStorageCancel()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for len(u.orchestrateJobs) != 0 {
		select {
		case complete := <-u.orchestrateCompletions:
			u.completeOrchestrateWork(complete)
		case <-timer.C:
			return errors.Join(u.orchestrateStorageErr, errors.New("orchestration storage did not finish before shutdown; inspect retained run"))
		}
	}
	return u.orchestrateStorageErr
}
