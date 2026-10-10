package router

import (
	"context"
	"errors"
	"slices"
	"time"
)

func (p *mekugiProxy) observeJournalHostTurn(ctx context.Context, workspace, method string, event appServerEvent) error {
	if event.ThreadID != "" {
		if method == "thread/tokenUsage/updated" {
			usage := event.TokenUsage
			used := uint64(0)
			if usage.Last != nil {
				used = usage.Last.TotalTokens
			}
			p.observeContextSliceUsage(event.ThreadID, usage.Last != nil, used, usage.ModelContextWindow)
		} else if method == "item/completed" && event.Item.Type == "contextCompaction" {
			p.mu.Lock()
			delete(p.contextUsage, event.ThreadID)
			p.mu.Unlock()
		}
	}
	state, reason := "", ""
	if method == "turn/started" {
		state = "working"
	}
	if method == "turn/completed" {
		if event.Turn.Status == "completed" {
			state = "done"
		} else {
			state, reason = "blocked", "Host turn "+event.Turn.Status
		}
	}
	if method == "thread/status/changed" && event.ThreadID != "" {
		switch event.Status.Type {
		case "active", "idle", "notLoaded", "systemError":
			running := event.Status.Type == "active"
			err := p.journals.observeWorkStatus(ctx, p.replayStore, workspace, event.ThreadID, running)
			if workspace != "" {
				err = errors.Join(err, p.journals.observeWorkStatus(ctx, p.replayStore, "", event.ThreadID, running))
			}
			return err
		}
	}
	if state == "" || event.ThreadID == "" {
		return nil
	}
	// Requests without workspace metadata keep their journals in the unscoped
	// namespace, which the native frontend also presents. Only a namespace that
	// holds the child's record changes; the other is a no-op.
	err := p.journals.observeLifecycle(ctx, p.replayStore, workspace, event.ThreadID, state, reason)
	if workspace != "" {
		err = errors.Join(err, p.journals.observeLifecycle(ctx, p.replayStore, "", event.ThreadID, state, reason))
	}
	return err
}

func (p *mekugiProxy) refreshJournalChildLifecycles(ctx context.Context, workspace, caller string) error {
	if p.nativeTrace == nil {
		return nil
	}
	store := p.replayStore.scoped(ctx)
	return p.journals.transaction(ctx, store, workspace, caller, func(_ *threadJournal, exists bool) error {
		if !exists {
			return errJournalUnchanged
		}
		// Read host evidence under the same lock as durable turn state.
		// A newer request cannot commit between this snapshot and reconciliation.
		results := p.nativeTrace.readAgentResults()
		if len(results) == 0 {
			return errJournalUnchanged
		}
		// Load only the observed threads and their parent chains. Every request
		// refreshes, so decoding the whole workspace would scale with history.
		journals := make(map[string]threadJournal)
		recordErrors := make(map[string]error)
		loaded := make(map[string]bool)
		for _, result := range results {
			if result.thread == caller {
				continue
			}
			for thread := result.thread; thread != "" && !loaded[thread]; {
				loaded[thread] = true
				if store == nil {
					j, ok := p.journals.memory[journalKey(workspace, thread)]
					if !ok {
						break
					}
					journals[thread] = j.clone()
				} else {
					j, exists, err := readThreadJournal(store, workspace, thread)
					if !exists {
						break // Unidentifiable records cannot prove ancestry.
					}
					journals[thread], recordErrors[thread] = j, err
				}
				thread = journals[thread].Parent
			}
		}
		for _, result := range results {
			child := journals[result.thread]
			chain := journalAncestry(journals, child.Thread)
			if len(chain) < 2 || !slices.Contains(chain[1:], caller) || slices.ContainsFunc(chain, func(thread string) bool { return recordErrors[thread] != nil }) {
				continue
			}
			state, reason := result.state, result.reason
			if result.state != "working" && (child.Parent != result.parent || child.Author != result.agent) {
				state, reason = "working", "" // Retain only the proven host start.
			}
			if state == "working" && result.turn == child.TurnID && (child.LifecycleState == "done" || child.LifecycleState == "blocked") {
				continue // A start cannot replace this turn's observed host outcome.
			}
			if child.TurnID != result.turn {
				previous := slices.Index(result.previous, child.TurnID)
				if child.TurnID != "" && previous < 0 && (child.LifecycleState == "working" || state == "done") {
					// Unmatched turn evidence keeps integration open, never
					// reuses a previous turn's success or releases a live child.
					state, reason = "working", ""
				} else {
					// An idle child's unmatched start or failure is newer than its
					// durable turn, as when a restarted router has a fresh trace.
					// Adopting it lets the same turn's later failure settle.
					child.TurnID, child.TurnStartSeq = result.turn, child.Sequence
					child.Turns += len(result.previous) - previous
				}
			}
			if child.LifecycleState == state && child.LifecycleReason == reason && child.TurnID == journals[result.thread].TurnID {
				continue
			}
			now := time.Now().UTC()
			child.discardForeignTimerAnchors()
			child.TimerOwner = journalTimerOwner
			child.setWorkTimers(state == "working", now)
			child.LifecycleState, child.LifecycleReason, child.LifecycleAt = state, reason, now.Format(time.RFC3339Nano)
			if store != nil {
				// Host lifecycle evidence does not make the caller's session
				// retain the child's record.
				if err := writeThreadJournal(p.replayStore, child); err != nil {
					return err
				}
			} else {
				p.journals.memory[journalKey(workspace, child.Thread)] = child
			}
			if sink := p.journals.nativeSink(workspace, child.Thread); sink != nil {
				sink.publish(child, false)
			}
			p.journals.publishMountedViews(store, workspace, child.Thread)
		}
		return errJournalUnchanged
	})
}

func (p *mekugiProxy) readJournalTree(ctx context.Context, workspace, thread, agent, path string, depth *int, view string) ([]journalNode, error) {
	if view == "" || view == "combined" {
		if err := p.refreshJournalChildLifecycles(ctx, workspace, thread); err != nil {
			return nil, err
		}
	}
	return p.journals.readTree(ctx, p.replayStore, workspace, thread, agent, path, depth, view)
}

func (p *mekugiProxy) applyJournal(ctx context.Context, workspace, thread, receipt string, mutations []journalMutation) ([]string, error) {
	if err := p.refreshJournalChildLifecycles(ctx, workspace, thread); err != nil {
		return nil, err
	}
	mutations, err := p.prepareJournalAcceptance(ctx, workspace, thread, receipt, mutations)
	if err != nil {
		return nil, err
	}
	paths, blocks, err := p.journals.applyWithBlocks(ctx, p.replayStore, workspace, thread, receipt, mutations)
	if runtime := p.orchestration; err == nil && runtime != nil && runtime.store != nil {
		blocks = slices.DeleteFunc(blocks, func(e orchestrateEvent) bool { return e.directory != runtime.store.Directory })
		if len(blocks) != 0 {
			runtime.eventsMu.Lock()
			runtime.journalEvents = append(runtime.journalEvents, blocks...)
			runtime.eventsMu.Unlock()
		}
	}
	return paths, err
}
