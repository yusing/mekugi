package launch

import (
	"context"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// Acquisition transfers detached retained data and its lease to the serialized
// endpoint. Workers never mutate presentation, issue RPCs or read the transport.
type nativeHydration struct {
	message  appserver.Message
	retained history.Session
	content  retainedActivity
	panes    *preferences.Attachment
	paneErr  error
	err      error
}

type retainedActivity struct {
	thread      binding.ThreadInfo
	rollout     restoredRollout
	hooks       []history.HookObservation
	compactions map[[2]string]*history.CompactionObservation
	segments    map[string]map[string]*reports.Report
	notices     []restoredHistoryNotice
}

func readRetainedActivity(ctx context.Context, retained history.Session, root binding.ThreadInfo) retainedActivity {
	result := retainedActivity{thread: root, rollout: readRestoredRollout(root, "/root"), compactions: make(map[[2]string]*history.CompactionObservation)}
	references := rollout.CompactionReferences(ctx, root.ID, root.Path)
	for ti := range result.thread.Turns {
		for ii := range result.thread.Turns[ti].Items {
			item := &result.thread.Turns[ti].Items[ii]
			if item.Type != "contextCompaction" {
				continue
			}
			summary, _, err := compact.ReadSummary(retained.Store, root.ID, references[item.ID])
			if err != nil {
				summary = "Compaction summary unavailable: " + err.Error()
			}
			item.Text = summary
			result.readCompaction(retained, root.ID, item.ID)
		}
	}
	for _, event := range result.rollout.events {
		if n := event.entry.Native; event.entry.Kind == "compaction" && n != nil {
			result.readCompaction(retained, n.Thread, n.Item)
		}
	}
	hooks, err := retained.Store.ReadHookObservations(root.Cwd, root.ID)
	if err != nil {
		result.notices = append(result.notices, restoredHistoryNotice{"Hook observations for " + root.ID + " unavailable: " + err.Error(), true})
	} else {
		result.hooks = hooks.Runs
		if hooks.Dropped > 0 {
			result.notices = append(result.notices, restoredHistoryNotice{fmt.Sprintf("%s: %d earlier hook observations were not retained", root.ID, hooks.Dropped), true})
		}
		if retained.Store != nil && len(hooks.Runs) > 0 {
			if err := retained.Store.Transaction(ctx, retained.Writes, func(tx *history.Transaction) error {
				return requestexecution.StoragePublicationError(tx.Retain(retained.Thread, retained.Limits, history.HookObservationsName(root.Cwd, root.ID)))
			}); err != nil {
				result.notices = append(result.notices, restoredHistoryNotice{"Hook observations for " + root.ID + " could not be retained: " + err.Error(), true})
			}
		}
	}
	return result
}

func (r *retainedActivity) readCompaction(retained history.Session, thread, item string) {
	key := [2]string{thread, item}
	if _, exists := r.compactions[key]; exists {
		return
	}
	value, _, err := retained.Store.ReadCompaction(thread, item)
	r.compactions[key] = value
	if err != nil {
		r.notices = append(r.notices, restoredHistoryNotice{"Compaction measurements unavailable: " + err.Error(), true})
	}
}

func (session *nativeSession) beginNativeHydration(message appserver.Message) (bool, error) {
	if session.connection == nil || session.nativeAgent == nil || session.nativeAgent.ParentThreadID == nil || message.Method != "" || message.Error != nil {
		return false, nil
	}
	request, ok := session.Client.Requests.Lookup(string(message.ID))
	if !ok || request.Method != "thread/resume" || request.Side != nil || request.Prediction != nil {
		return false, nil
	}
	if session.hydration != nil {
		return false, nil
	}
	if session.hydrating {
		return true, nil
	}
	accepted, err := session.Client.Startup.Accept(&session.Client.Live, request.Method, message.Result)
	if err != nil {
		return true, err
	}
	session.hydrating = true
	session.restoring = new(appServerActivityRestore)
	session.attached.presentation.Status.SetText("Loading agent history…")
	// Capture owners and immutable input before handing off. Only the event loop
	// installs the result, and cancellation disposes an undelivered lease.
	ctx, service, retained, segments := session.ctx, session.history, session.retained, session.segments
	var prefs *preferences.Service
	if session.panes != nil {
		prefs = session.panes.service
	}
	result := &nativeHydration{message: message}
	session.connection.workers.Go(func() {
		result.err = openRetainedSession(ctx, service, &result.retained, accepted.Thread.ID, retained.Store, retained.Writes, retained.Limits)
		if result.err == nil {
			result.content = readRetainedActivity(ctx, result.retained, accepted.Thread)
			result.content.segments = make(map[string]map[string]*reports.Report)
			for _, turn := range accepted.Thread.Turns {
				if ctx.Err() != nil {
					break
				}
				result.content.segments[turn.ID] = prepareCommandSegments(ctx, retained, segments, accepted.Thread.ID, accepted.Thread.Cwd, turn)
			}
			if prefs != nil && ctx.Err() == nil {
				result.panes, result.paneErr = prefs.Attach(preferences.Scope{Workspace: accepted.Thread.Cwd, Thread: accepted.Thread.ID}, nil)
				if result.paneErr == nil {
					var state preferences.Snapshot
					state, result.paneErr = prefs.Read(result.panes.Scope())
					if result.paneErr == nil {
						result.panes.Restore(state)
					}
				}
			}
		}
		result.err = errors.Join(result.err, ctx.Err())
		select {
		case session.hydrationResults <- result:
		case <-ctx.Done():
			result.retained.Close()
		}
	})
	return true, nil
}

func (session *nativeSession) acceptNativeHydration(result *nativeHydration) error {
	session.hydrating = false
	if result.err != nil {
		result.retained.Close()
		return fmt.Errorf("agent history acquisition failed: %w", result.err)
	}
	session.hydration = result
	defer func() { result.retained.Close(); session.hydration = nil }()
	return session.dispatch(result.message)
}
