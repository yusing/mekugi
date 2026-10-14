package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/internal/appserver"
)

// Both the caller and the existing storage owner can end a journal worker.
func journalWorkContext(caller, storage context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(storage, cancel)
	return ctx, func() { stop(); cancel() }
}

const journalStorageLane = "/journal"

// Lifecycle facts and dependent reset work share the existing ordered UI lane.
// Workspace and ancestry inputs are captured at observation, before navigation
// or metadata can change the selected session.
func (u *appServerUI) queueJournalHostMessage(m appserver.Message) {
	if len(m.ID) != 0 || u.session.paths == nil || u.proxy == nil || u.proxy.journals == nil {
		return
	}
	switch m.Method {
	case "turn/started", "turn/completed", "thread/status/changed", "thread/tokenUsage/updated", "item/completed":
	default:
		return
	}
	var event appServerEvent
	if json.Unmarshal(m.Params, &event) != nil || event.ThreadID == "" {
		return // The ordinary event consumer reports malformed notifications.
	}
	u.proxy.observeJournalContextUsage(m.Method, event)
	if m.Method == "thread/tokenUsage/updated" || m.Method == "item/completed" {
		return
	}
	workspace := u.session.threadWorkspace(event.ThreadID)
	scope := &appServerUI{ctx: u.ctx, proxy: u.proxy, thread: u.thread, session: appServerSession{cwd: u.session.cwd}}
	owner, generation := u.orchestrationOwner(), u.orchestrateGeneration
	owner.orchestrateBatchWork(u.session.cwd, event.ThreadID, journalStorageLane, func() func() {
		ctx, done := journalWorkContext(scope.ctx, owner.orchestrateStorageContext)
		defer done()
		scope.ctx = ctx
		var err error
		if workspace == "" && scope.proxy.replayStore != nil {
			workspace, err = scope.proxy.replayStore.retainedJournalWorkspace(event.ThreadID, true)
			if errors.Is(err, errNoRetainedJournalWorkspace) {
				workspace, err = scope.journalDescendantWorkspace(event.ThreadID)
			}
		}
		if err == nil {
			err = scope.proxy.observeJournalHostTurn(scope.ctx, workspace, m.Method, event)
		}
		return func() {
			if generation == u.orchestrateGeneration {
				if u.journalLifecycleErrors == nil {
					u.journalLifecycleErrors = make(map[string]error)
				}
				u.journalLifecycleErrors[event.ThreadID] = err
			}
			if err != nil {
				owner.orchestrateStorageErr = errors.Join(owner.orchestrateStorageErr, fmt.Errorf("journal lifecycle: %w", err))
				if generation == u.orchestrateGeneration {
					u.setNotice("Journal lifecycle: "+err.Error(), true)
				}
			}
		}
	})
}

// Each worker owns a detached driver. Only its UI result can adopt state or
// send a host request. Preparation follows the preceding durable lane result,
// while context usage and service tier retain their observation-time values.
func (u *appServerUI) queueJournalResetWork(label string, work func(*journalResetDriver) error) {
	d := u.reset
	if d == nil {
		return
	}
	owner, epoch := u.orchestrationOwner(), u.journalResetEpoch
	d.proxy.mu.RLock()
	usage, known := d.proxy.contextUsage[d.thread]
	d.proxy.mu.RUnlock()
	tier := ""
	if d.serviceTier != nil {
		tier = d.serviceTier()
	}
	d.pending++
	owner.orchestratePreparedBatchWork(u.session.cwd, d.thread, journalStorageLane, func() func() func() {
		next := *d
		next.pending = 0
		if d.intent != nil {
			intent := *d.intent
			next.intent = &intent
		}
		next.serviceTier = func() string { return tier }
		next.contextSnapshot = func() (hostContextUsage, bool) { return usage, known }
		var phase, method string
		var params any
		next.deferRequest = func(p, m string, value any) { phase, method, params = p, m, value }
		return func() func() {
			ctx, done := journalWorkContext(next.ctx, owner.orchestrateStorageContext)
			defer done()
			next.ctx = ctx
			err := work(&next)
			return func() {
				d.pending--
				next.pending = d.pending
				next.deferRequest, next.contextSnapshot, next.serviceTier, next.ctx = nil, nil, d.serviceTier, d.ctx
				*d = next
				if err != nil {
					owner.orchestrateStorageErr = errors.Join(owner.orchestrateStorageErr, fmt.Errorf("%s: %w", label, err))
				}
				if u.reset != d {
					return // A replaced session retains persistence, not dispatch.
				}
				if err != nil {
					u.setNotice(label+": "+err.Error(), true)
				}
				u.showResetNotice()
				if method != "" {
					if epoch != u.journalResetEpoch {
						d.phase, d.startPending = "", false // The queued stop owns cancellation.
					} else if owner.orchestrateClosing || u.turn != "" || u.questionCount() != 0 || len(u.unsent)+len(u.queued) != 0 || u.journalLifecycleErrors[d.thread] != nil {
						u.stopJournalReset("", false)
					} else {
						id, sendErr := d.client.Send(method, params, true)
						if sendErr != nil {
							u.queueJournalResetWork("Journal slice", func(next *journalResetDriver) error { return next.sendFailed(sendErr) })
						} else {
							d.phase, d.requestID = phase, id
						}
					}
				}
				if !d.active() {
					u.interruption.cancelDeferred()
					if u.journalNotifyTurn != "" && u.turn == "" && u.questionCount() == 0 {
						u.notify("agent-turn-complete", "Agent turn complete")
						u.journalNotifyTurn = ""
					}
				}
				if !owner.orchestrateClosing {
					if flushErr := u.flushInput(); flushErr != nil {
						u.setNotice("Journal input: "+flushErr.Error(), true)
					}
				}
				u.dirty = true
			}
		}
	})
}

func (u *appServerUI) queueJournalResetMessage(m appserver.Message) bool {
	d := u.reset
	ack := m.Method == "" && d.requestID != "" && string(m.ID) == d.requestID
	if !ack {
		if m.Method != "turn/started" && m.Method != "turn/completed" && m.Method != "item/completed" {
			return false
		}
		var event appServerEvent
		if json.Unmarshal(m.Params, &event) != nil || event.ThreadID != d.thread || m.Method == "item/completed" && event.Item.Type != "contextCompaction" {
			return false
		}
		if m.Method == "item/completed" {
			// Persistence can publish before this notification reaches painting.
			// Hold its reset disclosure before the worker can start.
			for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
				if sink != nil && sink.thread == event.ThreadID {
					sink.holdResetRecovery(event.TurnID)
				}
			}
		}
		if d.phase == "" && d.pending == 0 {
			// An idle driver's lifecycle observation is memory-only. Do not
			// hold ordinary host steering behind auxiliary journal storage.
			_, _ = d.message(m)
			return false
		}
	}
	u.queueJournalResetWork("Journal slice", func(next *journalResetDriver) error {
		_, err := next.message(m)
		return err
	})
	return ack
}

func (u *appServerUI) stopJournalReset(turn string, keepRPC bool) {
	if u.reset == nil {
		return
	}
	u.journalResetEpoch++ // Suppress unsent requests before storage can settle.
	u.journalNotifyTurn = ""
	u.queueJournalResetWork("Journal stop", func(d *journalResetDriver) error {
		if turn == "" && d.intent != nil {
			turn = d.intent.Turn
		}
		if keepRPC && d.requestID != "" {
			if err := d.proxy.journals.stopJournalTurn(d.ctx, d.proxy.replayStore, d.workspace, d.thread, turn); err != nil {
				return err
			}
			d.intent = nil // An already-dispatched RPC remains host-owned.
			return nil
		}
		return d.stop(turn)
	})
}

func (u *appServerUI) queueJournalCompactionProgress(item appServerItem, thread, turn string, entry activityPaneEntry) {
	owner, generation := u.orchestrationOwner(), u.orchestrateGeneration
	scope := &appServerUI{ctx: u.ctx, proxy: u.proxy, thread: u.thread, journal: u.journal, unscopedJournal: u.unscopedJournal, session: appServerSession{compactionRollout: u.session.compactionRollout}}
	d := u.reset
	owner.orchestratePreparedBatchWork(u.session.cwd, thread, journalStorageLane, func() func() func() {
		if d != nil {
			copy := *d
			scope.reset = &copy
		}
		return func() func() {
			ctx, done := journalWorkContext(scope.ctx, owner.orchestrateStorageContext)
			defer done()
			scope.ctx = ctx
			if scope.reset != nil {
				scope.reset.ctx = ctx
			}
			text, wait, _ := scope.progress(item, "item/completed", thread, turn)
			entry.Text, entry.native.wait = text, wait
			if text != "" {
				entry = scope.progressEntry(entry)
			}
			for _, sink := range []*nativeJournalSink{scope.journal, scope.unscopedJournal} {
				if sink != nil && sink.thread == thread {
					sink.finishResetRecovery(turn, scope.journalResetRecovery(sink.workspace, sink.thread, turn))
				}
			}
			return func() {
				if text != "" && generation == u.orchestrateGeneration {
					entry.Seq = u.session.next()
					u.applyActivity([]activityPaneEntry{entry}, nil)
					u.dirty = true
				}
			}
		}
	})
}
