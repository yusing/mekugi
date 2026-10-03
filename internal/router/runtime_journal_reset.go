package router

import (
	"context"
	"errors"
	"fmt"
)

// startTurn runs before native input. On a new session, bind installs it before
// the first native tool receipt; no synthetic turn is created by journal reads.
func (o *runtimeJournalOwner) startTurn(ctx context.Context, turn string) error {
	o.mu.Lock()
	if o.turn != turn {
		o.pendingStop = ""
	}
	o.turn = turn
	sink := o.root
	o.mu.Unlock()
	if sink == nil {
		return nil
	}
	ctx, err := o.scope(ctx, o.rootBinding())
	if err != nil {
		return err
	}
	return o.journals.beginJournalTurn(ctx, o.capture.store, sink.workspace, sink.thread, turn)
}

func (o *runtimeJournalOwner) stopTurn(ctx context.Context, turn string) error {
	o.mu.Lock()
	o.pendingStop = turn
	sink := o.root
	o.mu.Unlock()
	if sink == nil {
		return nil
	} // Native binding installs the retained stop.
	b := o.rootBinding()
	ctx, err := o.scope(ctx, b)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.turn != turn || o.pendingStop != turn {
		return nil
	}
	if err := o.journals.stopJournalTurn(ctx, o.capture.store, b.Workspace, observationThread(b), turn); err != nil {
		return err
	}
	o.pendingStop = ""
	return nil
}

// Serialize the user's stop with scope rotation. Before first new input, the
// prepared scope has no native turn ID; retain the stop in both reset branches.
func (o *runtimeJournalOwner) cancelReset(ctx context.Context, marker string, source ObservationBinding) error {
	o.capture.mu.Lock()
	defer o.capture.mu.Unlock()
	b := ObservationBinding{Runtime: o.capture.runtime, Workspace: o.capture.workspace, Session: o.capture.session}
	thread := observationThread(b)
	if source.Runtime != b.Runtime || source.Workspace != b.Workspace || source.Agent != "" || source.Branch != "" || source.Session == "" {
		return errors.New("reset cancellation source mismatch")
	}
	threads := []string{thread}
	if source != b {
		if err := o.capture.store.locked(ctx, func() error {
			scope, _, err := o.capture.store.readHandleScope(thread)
			if err != nil {
				return err
			}
			if scope.Fork != observationThread(source) {
				return errors.New("reset cancellation cannot cross unrelated scopes")
			}
			threads = append(threads, scope.Fork)
			return nil
		}); err != nil {
			return err
		}
	}
	for _, thread := range threads {
		scope := context.WithValue(ctx, storageSessionKey{}, storageSessionIdentity{Thread: thread, Namespace: thread})
		if err := o.journals.transaction(scope, o.capture.store, b.Workspace, thread, func(j *threadJournal, exists bool) error {
			if !exists {
				return errJournalUnchanged
			}
			j.ResetHandledTurn, j.ResetIntent = j.TurnID, nil
			j.pauseTasks(marker)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// The SDK creates a new native session, not an edited transcript. Shared fork
// owners freeze journals, change streams and retained output into that scope.
// The source stays independently resumable; no live child authority is copied.
func (o *runtimeJournalOwner) resetSession(ctx context.Context, operation string, target ObservationBinding) (any, error) {
	if operation == "journal_reset_installed" {
		ctx, err := o.scope(ctx, target)
		if err != nil || target.Agent != "" || target != o.rootBinding() {
			return nil, errors.Join(err, errors.New("reset installation requires the bound native root"))
		}
		thread := observationThread(target)
		record, found, err := o.capture.store.scoped(ctx).lookup(ctx, target.Workspace, thread+"/reset-context")
		if err != nil || !found || record.ToolName != "native_reset_context" {
			return nil, errors.Join(err, errors.New("prepared reset context unavailable"))
		}
		_, err = o.journals.apply(ctx, o.capture.store, target.Workspace, thread, "native-reset:"+target.Session,
			[]journalMutation{{Op: "log", Text: new("Context reset from journal · native session " + target.Session)}})
		return map[string]bool{"installed": err == nil}, err
	}
	root := o.rootBinding()
	text, err := o.recover(ctx, root)
	if err != nil {
		return nil, err
	}
	o.capture.mu.Lock()
	defer o.capture.mu.Unlock()
	if o.capture.pendingCount.Load() != 0 {
		return nil, errors.New("journal reset waits for native observations to settle")
	}
	if operation == "journal_reset_check" {
		return map[string]bool{"ready": true}, nil
	}
	if target.Runtime != root.Runtime || target.Workspace != root.Workspace || target.Session == "" || len(target.Session) > 1024 || target.Session == root.Session || target.Agent != "" || target.Branch != "" {
		return nil, errors.New("reset requires a distinct native root session in this workspace")
	}
	thread, source := observationThread(target), observationThread(root)
	identity := storageSessionIdentity{Thread: thread, Namespace: thread, Fork: source, Initialize: true}
	ctx = context.WithValue(ctx, storageSessionKey{}, identity)
	store := o.capture.store.scoped(ctx)
	if err := store.locked(ctx, func() error {
		_, exists, err := store.readHandleScope(thread)
		if err != nil {
			return err
		}
		if exists {
			return errors.New("reset target already has retained scope; request is not replayed")
		}
		return store.initializeHandleScope(nil, nil)
	}); err != nil {
		return nil, err
	}
	if err := o.journals.initialize(ctx, store, target.Workspace, thread, "/root", source); err != nil {
		return nil, err
	}
	if err := store.put(ctx, target.Workspace, map[string]mekugiHistory{thread + "/reset-context": {
		ToolName: "native_reset_context", Root: target.Workspace, ExecutingThread: thread,
		Report: text, NativeObservation: &nativeObservationRecord{Binding: target},
	}}); err != nil {
		return nil, fmt.Errorf("retain prepared reset context: %w", err)
	}
	// Only the bridge calls this after closing and draining the old query.
	// Keep its leases until launch shutdown, but do not authorize its old hooks.
	o.capture.session = target.Session
	clear(o.capture.bindings)
	clear(o.capture.tasks)
	clear(o.capture.taskEvents)
	clear(o.capture.live)
	clear(o.capture.pending)
	o.capture.windows = &execWindowRegistry{}
	o.mu.Lock()
	o.journals.detachNative(o.root)
	o.root, o.turn, o.pendingStop = nil, "", ""
	o.mu.Unlock()
	return map[string]string{"text": text}, nil
}
