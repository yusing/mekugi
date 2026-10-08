package router

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Native receipts authorize one exact MCP invocation, not model-supplied scope.
// Journal transactions and publication retain their shared owner and store.
type runtimeJournalOwner struct {
	capture     *nativeObservationOwner
	journals    *journalStore
	mu          sync.Mutex
	root        *nativeJournalSink
	turn        string
	pendingStop string
}

func (s *ObservationService) EnableJournal() {
	s.journal = &runtimeJournalOwner{capture: s.owner, journals: newJournalStore()}
}

func (o *runtimeJournalOwner) scope(ctx context.Context, b ObservationBinding) (context.Context, error) {
	o.capture.mu.Lock()
	defer o.capture.mu.Unlock()
	return o.capture.callContext(ctx, ObservationCall{Binding: b, ID: "journal", Tool: "journal"})
}

func (o *runtimeJournalOwner) rootBinding() ObservationBinding {
	o.capture.mu.Lock()
	defer o.capture.mu.Unlock()
	return ObservationBinding{Runtime: o.capture.runtime, Workspace: o.capture.workspace, Session: o.capture.session}
}

func (o *runtimeJournalOwner) bind(ctx context.Context, b ObservationBinding) error {
	ctx, err := o.scope(ctx, b)
	if err != nil {
		return err
	}
	thread := observationThread(b)
	author := "/root"
	if b.Agent != "" {
		author = "/native/" + b.Agent
	}
	if err := o.journals.initialize(ctx, o.capture.store, b.Workspace, thread, author, ""); err != nil {
		return err
	}
	// A hook establishes caller identity, not a child's durable parent. Children
	// remain unmounted until the runtime supplies ancestry, never by agent_type.
	if b.Agent != "" {
		return o.resolveParents(ctx)
	}
	if err := o.journals.bindIdentity(ctx, o.capture.store, b.Workspace, thread, "", author, true); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.root == nil {
		o.root = o.journals.attachNative(b.Workspace, thread)
		if err := o.journals.restoreNative(ctx, o.capture.store, o.root); err != nil {
			o.journals.detachNative(o.root)
			o.root = nil
			return err
		}
	}
	if err := o.journals.beginJournalTurn(ctx, o.capture.store, b.Workspace, thread, o.turn); err != nil {
		return err
	}
	if o.pendingStop != "" && o.pendingStop == o.turn {
		if err := o.journals.stopJournalTurn(ctx, o.capture.store, b.Workspace, thread, o.turn); err != nil {
			return err
		}
		o.pendingStop = ""
	}
	return nil
}

func journalNativeReceiptID(root ObservationBinding, id string) string {
	root.Agent = ""
	return fmt.Sprintf("%s/journal-call-%x", observationThread(root), sha256.Sum256([]byte(id)))
}

func (o *runtimeJournalOwner) before(ctx context.Context, call ObservationCall) error {
	if call.Tool != "mcp__mekugi__journal_batch" && call.Tool != "mcp__mekugi__journal_read" && call.Tool != "mcp__mekugi__mchanges" && call.Tool != "Agent" && call.Tool != "Task" {
		return errors.New("unsupported native journal tool")
	}
	// Authenticated native hook identity is evidence even if SubagentStart has
	// not reached this adapter yet. bind still validates the launch/session.
	if err := o.capture.bind(ctx, call.Binding); err != nil {
		return err
	}
	ctx, err := o.scope(ctx, call.Binding)
	if err != nil {
		return err
	}
	if call.ID == "" || len(call.ID) > 1024 || len(call.Input) > 8<<20 {
		return errors.New("invalid native journal receipt")
	}
	if err := o.bind(ctx, call.Binding); err != nil {
		return err
	}
	if err := o.journals.observeLifecycleReceipt(ctx, o.capture.store, call.Binding.Workspace, observationThread(call.Binding), "working", "", "native-start:"+call.ID); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	key := journalNativeReceiptID(call.Binding, call.ID)
	record, found, err := o.capture.store.lookup(ctx, call.Binding.Workspace, key)
	if err != nil {
		return err
	}
	if found {
		if record.NativeObservation == nil || record.NativeObservation.Call == nil || !reflect.DeepEqual(*record.NativeObservation.Call, call) {
			return errors.New("native journal receipt changed")
		}
		return nil
	}
	h := mekugiHistory{ToolName: call.Tool, ExecutingThread: observationThread(call.Binding), Root: call.Binding.Workspace, NativeObservation: &nativeObservationRecord{Binding: call.Binding, Call: &call}}
	return o.capture.store.put(ctx, call.Binding.Workspace, map[string]mekugiHistory{key: h})
}

func (o *runtimeJournalOwner) invoke(ctx context.Context, operation, id, input string) (any, error) {
	if id == "" || len(id) > 1024 || len(input) > 8<<20 {
		return nil, errors.New("native MCP tool-use identity unavailable")
	}
	root := o.rootBinding()
	ctx, err := o.scope(ctx, root)
	if err != nil {
		return nil, err
	}
	receipt, found, err := o.capture.store.lookup(ctx, root.Workspace, journalNativeReceiptID(root, id))
	if err != nil {
		return nil, err
	}
	if !found || receipt.NativeObservation == nil || receipt.NativeObservation.Call == nil {
		return nil, errors.New("native MCP caller receipt unavailable")
	}
	call := *receipt.NativeObservation.Call
	if call.ID != id || call.Tool != "mcp__mekugi__"+operation {
		return nil, errors.New("native MCP tool receipt mismatch")
	}
	var expected, actual any
	if json.Unmarshal([]byte(call.Input), &expected) != nil || json.Unmarshal([]byte(input), &actual) != nil || !reflect.DeepEqual(expected, actual) {
		return nil, errors.New("native MCP arguments differ from hook receipt")
	}
	ctx, err = o.scope(ctx, call.Binding)
	if err != nil {
		return nil, err
	}
	thread := observationThread(call.Binding)
	if operation == "mchanges" {
		var args struct {
			Args []string `json:"args"`
		}
		if err := json.Unmarshal([]byte(input), &args, json.RejectUnknownMembers(true)); err != nil {
			return nil, err
		}
		options, err := parseChangeReadAt(args.Args, root.Workspace, root.Workspace)
		if err != nil {
			return nil, err
		}
		if options.view == "apply" || options.view == "revert" {
			return nil, errors.New("MCP mchanges is read-only; use native Bash with explicit IDs for apply or revert")
		}
		if options.workspace != root.Workspace {
			return nil, errors.New("workspace does not match the native caller workspace")
		}
		// Exact native receipt scope, never process environment or last hook.
		result := executeParsedChanges(ctx, toolWorkerManifest{ReplayDirectory: o.capture.store.directory}, options)
		return map[string]any{"stdout": result.Stdout, "stderr": result.Stderr, "exitCode": result.ExitCode}, nil
	}
	if operation == "journal_batch" {
		var args struct {
			Journal jsontext.Value `json:"journal"`
		}
		if err := json.Unmarshal([]byte(input), &args, json.RejectUnknownMembers(true)); err != nil {
			return nil, err
		}
		mutations, err := decodeJournalMutations(args.Journal)
		if err != nil {
			return nil, err
		}
		if len(mutations) == 0 {
			return nil, errors.New("journal batch requires operations")
		}
		for _, m := range mutations {
			if m.Op == "add" && m.Title == nil {
				return nil, errors.New("native journal add requires title")
			}
			if m.Op != "plan" && m.Op != "add" && m.Op != "set" && m.Op != "log" && m.Op != "remove" {
				return nil, errors.New("unsupported journal mutation; native turn completion does not mutate tasks")
			}
			if m.Agent != "" {
				if _, err := o.journals.listAgent(ctx, o.capture.store, root.Workspace, thread, m.Agent); err != nil {
					return nil, err
				}
			}
		}
		paths, err := o.journals.apply(ctx, o.capture.store, root.Workspace, thread, "native:"+id, mutations)
		return map[string]any{"paths": paths}, err
	}
	var args struct {
		P     string `json:"p,omitempty"`
		Agent string `json:"agent,omitempty"`
		Depth *int   `json:"depth,omitempty"`
		View  string `json:"view,omitempty"`
	}
	if err := json.Unmarshal([]byte(input), &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if call.Binding.Agent != "" && args.View == "" {
		args.View = "own"
	}
	nodes, err := o.journals.readTree(ctx, o.capture.store, root.Workspace, thread, args.Agent, args.P, args.Depth, args.View)
	if err != nil {
		return nil, err
	}
	return o.readResult(ctx, nodes)
}

// Large trees stay in the shared output store, not an oversized native MCP
// result or UI frame. References carry an immutable snapshot, not a new read.
func (o *runtimeJournalOwner) readResult(ctx context.Context, nodes []journalNode) (any, error) {
	result := map[string]any{"nodes": nodes}
	data, err := json.Marshal(result)
	if err != nil || len(data) <= 128<<10 {
		return result, err
	}
	next, err := o.capture.store.putOutputChunks(ctx, string(data))
	if err != nil {
		return nil, err
	}
	return map[string]any{"incomplete": true, "bytes": len(data), "format": "JSON object with nodes; concatenate stdout in continuation order", "next_call": "mread " + next}, nil
}

func (o *runtimeJournalOwner) sink() *nativeJournalSink {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.root
}

func (o *runtimeJournalOwner) complete(ctx context.Context, turn string) error {
	sink := o.sink()
	if sink == nil {
		return nil
	}
	b := o.rootBinding()
	ctx, err := o.scope(ctx, b)
	if err != nil {
		return err
	}
	return o.journals.transaction(ctx, o.capture.store, sink.workspace, sink.thread, func(j *threadJournal, exists bool) error {
		if exists {
			sink.publish(j.clone(), true, turn)
		}
		return errJournalUnchanged
	})
}

func (o *runtimeJournalOwner) recover(ctx context.Context, b ObservationBinding) (string, error) {
	return o.recoverBounded(ctx, b, 10000)
}

func (o *runtimeJournalOwner) recoverBounded(ctx context.Context, b ObservationBinding, capacity int) (string, error) {
	if capacity < 1 || capacity > 10000 {
		return "", errors.New("native journal recovery capacity must be 1 to 10000 characters")
	}
	ctx, err := o.scope(ctx, b)
	if err != nil {
		return "", err
	}
	release, err := o.journals.lockState(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	store := o.capture.store.scoped(ctx)
	var result journalSummary
	err = store.locked(ctx, func() error {
		j, exists, err := readThreadJournal(store, b.Workspace, observationThread(b))
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("native journal recovery evidence unavailable")
		}
		result, err = store.journalSummaryBoundedLocked(ctx, j, capacity)
		return err
	})
	return result.Text, err
}

// SDK Agent results name the child's native ID. Join them to the exact
// pre-tool receipt and persist that proof before resolving an ancestry chain.
func (o *runtimeJournalOwner) parent(ctx context.Context, id, child, status string) error {
	root := o.rootBinding()
	ctx, err := o.scope(ctx, root)
	if err != nil {
		return err
	}
	if id == "" || child == "" || len(child) > 1024 {
		return errors.New("invalid native child identity")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	receipt, found, err := o.capture.store.lookup(ctx, root.Workspace, journalNativeReceiptID(root, id))
	if err != nil {
		return err
	}
	if !found || receipt.NativeObservation == nil || receipt.NativeObservation.Call == nil {
		return errors.New("native parent receipt unavailable")
	}
	call := *receipt.NativeObservation.Call
	if call.ID != id || call.Tool != "Agent" && call.Tool != "Task" {
		return errors.New("native parent tool identity mismatch")
	}
	childBinding := root
	childBinding.Agent = child
	// The exact native spawning-call receipt and SDK result establish this
	// child's identity independently of delivery order of SubagentStart.
	if err := o.capture.bind(ctx, childBinding); err != nil {
		return err
	}
	childCtx, err := o.scope(ctx, childBinding)
	if err != nil {
		return err
	}
	key := observationThread(childBinding) + "/journal-parent"
	prior, found, err := o.capture.store.lookup(childCtx, root.Workspace, key)
	if err != nil {
		return err
	}
	if found {
		if prior.NativeObservation == nil || prior.NativeObservation.Call == nil || prior.NativeObservation.Call.Binding != call.Binding {
			return errors.New("native child parent changed")
		}
	}
	proof := mekugiHistory{ToolName: "native_journal_parent", Root: root.Workspace, ExecutingThread: observationThread(childBinding), NativeObservation: &nativeObservationRecord{Binding: childBinding, Call: &call, Terminal: &ObservationTerminal{Status: status}}}
	records := map[string]mekugiHistory{journalParentCallKey(childBinding, id): proof}
	if !found {
		records[key] = proof // The ancestry fact is immutable across continued calls.
	}
	if err := o.capture.store.put(childCtx, root.Workspace, records); err != nil {
		return err
	}
	if err := o.resolveParents(ctx); err != nil {
		return err
	}
	return o.settleChild(childCtx, childBinding, id, status)
}

func journalParentCallKey(b ObservationBinding, id string) string {
	return observationThread(b) + fmt.Sprintf("/journal-parent-call-%x", sha256.Sum256([]byte(id)))
}

func (o *runtimeJournalOwner) settleChild(ctx context.Context, b ObservationBinding, id, status string) error {
	// Agent results and task terminals are two carriers for one native call.
	// Their replay must not overwrite work started after its first settlement.
	receipt := "native-terminal:" + id
	task, found, err := o.capture.store.lookup(ctx, b.Workspace, journalNativeReceiptID(b, id)+"/task/terminal")
	if err != nil {
		return err
	}
	if found && task.NativeObservation != nil && task.NativeObservation.Terminal != nil {
		t := task.NativeObservation.Terminal
		status = t.Status
	}
	if status != "completed" && status != "failed" && status != "stopped" {
		return nil
	}
	state, reason := "done", ""
	if status != "completed" {
		state, reason = "blocked", "Native agent "+status
	}
	return o.journals.observeLifecycleReceipt(ctx, o.capture.store, b.Workspace, observationThread(b), state, reason, receipt)
}

// Native task starts correlate a task ID to its spawning call. Terminal updates
// need that retained mapping, not an assumed equality of task and agent IDs.
func (o *runtimeJournalOwner) task(ctx context.Context, event observationTask, start bool) error {
	root := o.rootBinding()
	if event.Session != root.Session || event.ID == "" || len(event.ID) > 1024 {
		return errors.New("native journal task binding mismatch")
	}
	ctx, err := o.scope(ctx, root)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	key := observationThread(root) + fmt.Sprintf("/journal-task-%x", sha256.Sum256([]byte(event.ID)))
	prior, found, err := o.capture.store.lookup(ctx, root.Workspace, key)
	if err != nil {
		return err
	}
	var call ObservationCall
	if found && prior.NativeObservation != nil && prior.NativeObservation.Call != nil {
		call = *prior.NativeObservation.Call
		if event.CallID != "" && event.CallID != call.ID {
			return errors.New("native journal task call changed")
		}
	} else {
		if event.CallID == "" {
			return nil // No native mapping: an unrelated task is not a journal agent.
		}
		receipt, ok, err := o.capture.store.lookup(ctx, root.Workspace, journalNativeReceiptID(root, event.CallID))
		if err != nil {
			return err
		}
		if !ok || receipt.NativeObservation == nil || receipt.NativeObservation.Call == nil {
			return nil
		}
		call = *receipt.NativeObservation.Call
		if call.Tool != "Agent" && call.Tool != "Task" {
			return nil
		}
	}
	if !start && event.Status != "completed" && event.Status != "failed" && event.Status != "stopped" {
		return nil
	}
	proof := mekugiHistory{ToolName: "native_journal_task", Root: root.Workspace, ExecutingThread: observationThread(call.Binding), NativeObservation: &nativeObservationRecord{Binding: call.Binding, Call: &call, Terminal: &ObservationTerminal{Task: event.ID, Status: "running"}}}
	callKey := journalNativeReceiptID(root, call.ID) + "/task"
	byCall, exists, err := o.capture.store.lookup(ctx, root.Workspace, callKey)
	if err != nil {
		return err
	}
	if exists && byCall.NativeObservation != nil && byCall.NativeObservation.Terminal != nil && byCall.NativeObservation.Terminal.Task != event.ID {
		return errors.New("native journal task identity changed")
	}
	records := map[string]mekugiHistory{key: proof, callKey: proof}
	if !start {
		terminalKey := callKey + "/terminal"
		prior, found, err := o.capture.store.lookup(ctx, root.Workspace, terminalKey)
		if err != nil {
			return err
		}
		if found {
			// Native shutdown can mark already-completed children stopped.
			// Keep the settled proof; it also deduplicates child lifecycle.
			r := prior.NativeObservation
			if r == nil || r.Terminal == nil || r.Terminal.Task != event.ID || !sameObservationCall(r.Call, &call) {
				return errors.New("native journal terminal identity changed")
			}
		} else {
			terminalProof := proof
			terminalProof.NativeObservation = &nativeObservationRecord{Binding: call.Binding, Call: &call, Terminal: &ObservationTerminal{Task: event.ID, Status: event.Status}}
			records[terminalKey] = terminalProof
		}
	}
	if err := o.capture.store.put(ctx, root.Workspace, records); err != nil {
		return err
	}
	if err := o.resolveParents(ctx); err != nil {
		return err
	}
	if start {
		return nil
	}
	o.capture.mu.Lock()
	bindings := make([]ObservationBinding, 0, len(o.capture.bindings))
	for b := range o.capture.bindings {
		if b.Agent != "" {
			bindings = append(bindings, b)
		}
	}
	o.capture.mu.Unlock()
	for _, b := range bindings {
		childCtx, err := o.scope(ctx, b)
		if err != nil {
			return err
		}
		proof, found, err := o.capture.store.lookup(childCtx, b.Workspace, journalParentCallKey(b, call.ID))
		if err != nil {
			return err
		}
		if found && proof.NativeObservation != nil && proof.NativeObservation.Call != nil && proof.NativeObservation.Call.Binding == call.Binding {
			if err := o.settleChild(childCtx, b, call.ID, event.Status); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *runtimeJournalOwner) resolveParents(ctx context.Context) error {
	o.capture.mu.Lock()
	bindings := make([]ObservationBinding, 0, len(o.capture.bindings))
	for b := range o.capture.bindings {
		if b.Agent != "" {
			bindings = append(bindings, b)
		}
	}
	o.capture.mu.Unlock()
	// A nested result may precede its parent's result. Retry only retained
	// identity proofs, never tools/processes, until no chain advances.
	for range len(bindings) {
		changed := false
		for _, b := range bindings {
			childCtx, err := o.scope(ctx, b)
			if err != nil {
				return err
			}
			proof, found, err := o.capture.store.lookup(childCtx, b.Workspace, observationThread(b)+"/journal-parent")
			if err != nil {
				return err
			}
			if !found || proof.NativeObservation == nil || proof.NativeObservation.Call == nil {
				continue
			}
			parentBinding := proof.NativeObservation.Call.Binding
			err = o.journals.transaction(childCtx, o.capture.store, b.Workspace, observationThread(b), func(j *threadJournal, exists bool) error {
				if !exists {
					return errJournalUnchanged
				}
				parent, found, err := readThreadJournal(o.capture.store.scoped(childCtx), b.Workspace, observationThread(parentBinding))
				if err != nil {
					return err
				}
				if !found || !parent.IdentityKnown || parent.IdentityConflicted {
					return errJournalUnchanged
				}
				if j.Thread == parent.Thread {
					return errors.New("native child identity cycles to itself")
				}
				if j.IdentityKnown {
					if j.Parent != parent.Thread {
						return errors.New("native child parent changed")
					}
					return errJournalUnchanged
				}
				old := j.Author
				j.Author = parent.Author + "/" + b.Agent
				for i := range j.Items {
					if j.Items[i].Author == old {
						j.Items[i].Author = j.Author
					}
				}
				for i := range j.Events {
					if j.Events[i].Author == old {
						j.Events[i].Author = j.Author
					}
					if j.Events[i].Fields.Author == old {
						j.Events[i].Fields.Author = j.Author
					}
				}
				j.Parent = parent.Thread
				j.IdentityKnown = true
				changed = true
				return nil
			})
			if err != nil {
				return err
			}
			status := ""
			if proof.NativeObservation.Terminal != nil {
				status = proof.NativeObservation.Terminal.Status
			}
			if err := o.settleChild(childCtx, b, proof.NativeObservation.Call.ID, status); err != nil {
				return err
			}
		}
		if !changed {
			return nil
		}
	}
	return nil
}
