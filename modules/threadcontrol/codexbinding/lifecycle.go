package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type hostObservation struct {
	thread, method, wait string
	value                binding.Notification
	session              history.Session
	restored             chan error
	reduced              chan error
	settled              func(error)
	inputID              string
	receipt              control.Receipt
	status               control.Status
	failure              control.HostFailure
	err                  error
	compound             *control.CompoundSettlement
	compoundReply        func(control.CompoundDisposition, error)
	compoundReset        *control.CompoundReset
	feature              *control.FeatureFact
	features             chan control.FeatureSnapshot
}

// CompactionFailure is auxiliary: preserve logical work while displaying the failure.
type CompactionFailure struct{ Err error }

func (e *CompactionFailure) Error() string { return e.Err.Error() }
func (e *CompactionFailure) Unwrap() error { return e.Err }

// Completed queues a typed backend terminal fact on the existing lifecycle
// owner, leaving the runtime event reader free to acknowledge later inputs.
// settled releases native retirement before any newly eligible effects dispatch.
func (d *Driver) Completed(thread, turn string, status control.Status, failure control.HostFailure, settled func(error)) {
	d.enqueue(hostObservation{thread: thread, method: "runtime/completed", value: binding.Notification{TurnID: turn}, status: status, failure: failure, settled: settled})
}

func (d *Driver) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lifecycle != nil {
		return errors.New("thread lifecycle already started")
	}
	d.lifecycle, d.cancel = context.WithCancel(ctx)
	go d.reduceHost()
	return nil
}
func (d *Driver) Stop(ctx context.Context) error {
	d.mu.Lock()
	if d.cancel == nil {
		d.mu.Unlock()
		return nil
	}
	d.cancel()
	thread := d.thread
	pending := d.pending
	d.pending = nil
	d.mu.Unlock()
	for _, event := range pending {
		if source := d.featureSource(); event.feature != nil && source != nil {
			source.InvalidateFeatureUse(context.Canceled)
		}
		if event.features != nil {
			event.features <- control.FeatureSnapshot{Err: context.Canceled}
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.stopped:
	}
	d.auxiliary.Wait()
	if _, admitted := d.control.Member(thread); !admitted {
		return nil
	}
	return d.control.Pause(ctx, thread)
}
func (d *Driver) Changed() <-chan struct{} { return d.changed }
func (d *Driver) Failures() []error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var result []error
	for thread, err := range d.failures {
		result = append(result, fmt.Errorf("thread %s: %w", thread, err))
	}
	clear(d.failures)
	return result
}

func (d *Driver) SettleCompound(input control.CompoundSettlement, reply func(control.CompoundDisposition, error)) {
	d.enqueue(hostObservation{thread: input.Thread, method: "thread/compoundWait/settle", compound: &input, compoundReply: reply})
}

func (d *Driver) NativeCompoundReplyFailed(input control.CompoundSettlement, failure error) {
	d.enqueue(hostObservation{thread: input.Thread, method: "thread/compoundWait/reset", compoundReset: &control.CompoundReset{
		Thread: input.Thread, Turn: input.Turn, Item: input.Item, Window: input.Window, Cell: input.Cell,
		Ticket: input.Ticket, Reset: "uncertain", Diagnostic: failure.Error(),
	}})
}

// Observe returns a completion receipt only for admitted terminal observations.
// The host event loop can defer terminal presentation without waiting for I/O.
func (d *Driver) Observe(method string, raw jsontext.Value) (<-chan error, error) {
	if method == "featureUse/unavailable" {
		var notice struct {
			Thread  string `json:"threadId"`
			Message string `json:"message"`
		}
		err := json.Unmarshal(raw, &notice, json.RejectUnknownMembers(true))
		if err == nil {
			err = errors.New(notice.Message)
		}
		if source := d.featureSource(); source != nil {
			source.InvalidateFeatureUse(err)
		}
		return nil, nil
	}
	if method == "featureUse/observed" {
		var fact control.FeatureFact
		if err := json.Unmarshal(raw, &fact, json.RejectUnknownMembers(true)); err != nil {
			if source := d.featureSource(); source != nil {
				source.InvalidateFeatureUse(err)
			}
			return nil, nil
		}
		d.enqueue(hostObservation{thread: fact.Thread, method: method, feature: &fact})
		return nil, nil
	}
	if method == "thread/compoundWait/reset" {
		var receipt control.CompoundReset
		if err := json.Unmarshal(raw, &receipt, json.RejectUnknownMembers(true)); err != nil {
			return nil, err
		}
		d.enqueue(hostObservation{thread: receipt.Thread, method: method, compoundReset: &receipt})
		return nil, nil
	}
	if method != "turn/started" && method != "turn/completed" && method != "item/completed" {
		return nil, nil
	}
	value, err := binding.DecodeNotification(method, raw)
	if err != nil {
		return nil, err
	}
	// Async question items carry final_answer too, but their text belongs to
	// the shared question channel, not the delegate's authored completion.
	if method == "item/completed" && value.Item.Type == "agentMessage" && value.Item.Delivery == "async" && len(value.Item.Questions) > 0 {
		return nil, nil
	}
	if method == "item/completed" && !(value.Item.Type == "agentMessage" && value.Item.Phase != "commentary" || value.Item.Type == "userMessage" && value.Item.ClientID != "") {
		return nil, nil
	}
	if _, ok := d.control.Member(value.ThreadID); !ok {
		return nil, nil
	}
	var reduced chan error
	if method == "turn/completed" {
		reduced = make(chan error, 1)
	}
	d.enqueue(hostObservation{thread: value.ThreadID, method: method, value: value, reduced: reduced})
	return reduced, nil
}

func (d *Driver) Tick(now time.Time, threads []string) {
	for _, thread := range threads {
		for _, thread := range d.control.Due(thread, now) {
			d.enqueue(hostObservation{thread: thread})
		}
	}
}

// Interrupt queues exact logical-wait cancellation off the host event loop.
func (d *Driver) Interrupt(thread, wait string) {
	d.enqueue(hostObservation{thread: thread, method: "human/interrupt", wait: wait})
}

// CheckpointWait keeps auxiliary compaction interruption on the serialized host
// lifecycle, without treating it as task interruption.
func (d *Driver) CheckpointWait(thread, turn string) {
	d.enqueue(hostObservation{thread: thread, method: "compaction/stopped", value: binding.Notification{TurnID: turn}})
}

// HumanInputAcknowledged queues the sole request owner's correlated outcome.
func (d *Driver) HumanInputAcknowledged(thread, id, turn string, err error) {
	receipt := control.Receipt{Turn: turn}
	if err != nil {
		receipt.Error = err.Error()
		failure, known := errors.AsType[*binding.DispatchError](err)
		receipt.Uncertain = !known || failure.Outcome == binding.Uncertain
	}
	d.enqueue(hostObservation{thread: thread, method: "human/input", inputID: id, receipt: receipt})
}

func (d *Driver) enqueue(event hostObservation) {
	d.mu.Lock()
	if d.lifecycle == nil || d.lifecycle.Err() != nil {
		d.mu.Unlock()
		err := errors.New("thread lifecycle is unavailable")
		if source := d.featureSource(); event.feature != nil && source != nil {
			source.InvalidateFeatureUse(err)
		}
		if event.features != nil {
			event.features <- control.FeatureSnapshot{Err: err}
		}
		if event.compoundReply != nil {
			event.compoundReply(control.CompoundDisposition{Ticket: event.compound.Ticket}, err)
		}
		if event.restored != nil {
			event.restored <- err
		}
		if event.settled != nil {
			event.settled(err)
		}
		if event.reduced != nil {
			event.reduced <- err
			close(event.reduced)
		}
		return
	}
	if event.method == "" && slices.ContainsFunc(d.pending, func(old hostObservation) bool { return old.thread == event.thread && old.method == "" }) {
		d.mu.Unlock()
		return
	}
	d.pending = append(d.pending, event)
	select {
	case d.wake <- struct{}{}:
	default:
	}
	d.mu.Unlock()
}

// Each native runtime has one observation worker; the host event loop never waits
// for checkpoint storage. Independent runtimes retain their own workers.
func (d *Driver) reduceHost() {
	defer close(d.stopped)
	for {
		d.mu.Lock()
		if len(d.pending) == 0 {
			d.mu.Unlock()
			select {
			case <-d.lifecycle.Done():
				return
			case <-d.wake:
			}
			continue
		}
		event := d.pending[0]
		d.pending = d.pending[1:]
		d.mu.Unlock()
		if d.lifecycle.Err() != nil {
			if source := d.featureSource(); event.feature != nil && source != nil {
				source.InvalidateFeatureUse(context.Canceled)
			}
			if event.features != nil {
				event.features <- control.FeatureSnapshot{Err: d.lifecycle.Err()}
			}
			return
		}
		thread, p := event.thread, event.value
		var err error
		switch event.method {
		case "featureUse/read":
			event.features <- d.control.ReadFeatureUse(d.lifecycle, thread, d.featureSource())
			continue
		case "featureUse/observed":
			if source := d.featureSource(); source != nil {
				source.RecordFeature(d.lifecycle, *event.feature)
			}
			continue
		case "thread/compoundWait/settle":
			var response control.CompoundDisposition
			response, err = d.control.SettleCompound(d.lifecycle, *event.compound)
			event.compoundReply(response, err)
		case "thread/compoundWait/reset":
			err = d.control.CompoundResetCompleted(d.lifecycle, *event.compoundReset)
			if event.compoundReset.Diagnostic != "" {
				event.err = errors.New(event.compoundReset.Diagnostic)
			}
		case "compaction/stopped":
			err = d.control.CheckpointWait(d.lifecycle, thread, p.TurnID)
		case "thread/restore":
			err = d.control.Restore(d.lifecycle, event.session)
			if err == nil {
				d.bindFeatureUse(thread)
			}
			if err == nil {
				for _, child := range d.control.Descendants(thread) {
					if !d.control.ObserveAudience(child.Thread, d.audience.ObserveAdmitted) {
						err = errors.New("retained child could not enter the application audience")
						break
					}
				}
			}
			event.restored <- err
		case "human/input":
			err = d.control.EffectAcknowledged(d.lifecycle, thread, event.inputID, event.receipt)
		case "human/interrupt":
			if member, ok := d.control.Member(thread); !ok || (member.Wait == nil || member.Wait.ID != event.wait) && (member.Stop == nil || member.Stop.Wait != event.wait) {
				continue
			}
			err = d.Reconcile(d.lifecycle, thread)
			if err == nil {
				var turn string
				turn, err = d.control.HumanInterrupt(d.lifecycle, thread, event.wait)
				if err == nil && turn != "" {
					var owner control.Runtime
					owner, err = d.runtime(thread)
					if err == nil {
						err = owner.Interrupt(d.lifecycle, thread, turn)
					}
				}
			}
		case "turn/started":
			if member, ok := d.control.Member(thread); ok {
				err = d.control.RequestStarted(d.lifecycle, control.Identity{Thread: thread, Session: member.Session, Turn: p.Turn.ID}, member.Parent == "")
			}
		case "item/completed":
			switch p.Item.Type {
			case "userMessage":
				err = d.control.HumanInputCommitted(d.lifecycle, thread, p.TurnID, p.Item.ClientID)
			default:
				err = d.control.AnswerCompleted(d.lifecycle, thread, p.TurnID, p.Item.ID, p.Item.Text)
			}
		case "turn/completed":
			failure := control.HostFailure{}
			if p.Turn.Error != nil {
				failure.Message, failure.Recoverable = p.Turn.Error.Message, p.Turn.Error.Recoverable()
			}
			err = d.control.HostCompleted(d.lifecycle, thread, p.Turn.ID, control.Status(p.Turn.Status), failure)
		case "runtime/completed":
			err = d.control.HostCompleted(d.lifecycle, thread, p.TurnID, event.status, event.failure)
		}
		if event.settled != nil {
			event.settled(err)
		}
		if event.reduced != nil {
			event.reduced <- err
			close(event.reduced)
		}
		if err == nil && (event.method == "thread/compoundWait/reset" || event.method == "turn/completed" || event.method == "runtime/completed" || event.method == "compaction/stopped" || event.method == "compaction/settled" || event.method == "human/interrupt" || event.method == "human/input" || event.method == "") {
			err = d.DispatchPending(d.lifecycle, thread)
			err = errors.Join(err, d.Wake(d.lifecycle, thread, time.Now()))
			for member, ok := d.control.Member(thread); ok && member.Parent != ""; member, ok = d.control.Member(member.Parent) {
				// A sibling's delivery may have queued behind this target's
				// acknowledged injection. Drain it before selecting the wait.
				err = errors.Join(err, d.DispatchPending(d.lifecycle, member.Parent))
				err = errors.Join(err, d.Wake(d.lifecycle, member.Parent, time.Now()))
			}
		}
		if event.err != nil {
			err = errors.Join(err, &CompactionFailure{Err: event.err})
		}
		d.mu.Lock()
		if err != nil {
			d.failures[thread] = err
		}
		select {
		case d.changed <- struct{}{}:
		default:
		}
		d.mu.Unlock()
	}
}
