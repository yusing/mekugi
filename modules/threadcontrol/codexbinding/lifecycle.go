package codexbinding

import (
	"context"
	"encoding/json/jsontext"
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
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.stopped:
	}
	if thread == "" {
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
func (d *Driver) Observe(method string, raw jsontext.Value) error {
	if method != "turn/started" && method != "turn/completed" && method != "item/completed" {
		return nil
	}
	value, err := binding.DecodeNotification(method, raw)
	if err != nil {
		return err
	}
	if method == "item/completed" && (value.Item.Type != "agentMessage" || value.Item.Phase == "commentary") {
		return nil
	}
	if _, ok := d.control.Member(value.ThreadID); ok {
		d.enqueue(hostObservation{thread: value.ThreadID, method: method, value: value})
	}
	return nil
}
func (d *Driver) Tick(now time.Time) {
	for _, thread := range d.control.Due(now) {
		d.enqueue(hostObservation{thread: thread})
	}
}

// Interrupt queues exact logical-wait cancellation off the host event loop.
func (d *Driver) Interrupt(thread, wait string) {
	d.enqueue(hostObservation{thread: thread, method: "human/interrupt", wait: wait})
}
func (d *Driver) enqueue(event hostObservation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lifecycle == nil || d.lifecycle.Err() != nil {
		if event.restored != nil {
			event.restored <- errors.New("thread lifecycle is unavailable")
		}
		return
	}
	if event.method == "" && slices.ContainsFunc(d.pending, func(old hostObservation) bool { return old.thread == event.thread && old.method == "" }) {
		return
	}
	d.pending = append(d.pending, event)
	select {
	case d.wake <- struct{}{}:
	default:
	}
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
			return
		}
		thread, p := event.thread, event.value
		var err error
		switch event.method {
		case "thread/restore":
			err = d.control.Restore(d.lifecycle, event.session)
			if err == nil {
				for _, child := range d.control.Descendants(thread) {
					if !d.audience.Observe(child.Thread, child.Parent, child.Task, true) {
						err = errors.New("retained child could not enter the application audience")
						break
					}
				}
			}
			event.restored <- err
		case "human/interrupt":
			if member, ok := d.control.Member(thread); !ok || member.Wait == nil || member.Wait.ID != event.wait {
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
			err = d.control.AnswerCompleted(d.lifecycle, thread, p.TurnID, p.Item.Text)
		case "turn/completed":
			failure := ""
			if p.Turn.Error != nil {
				failure = p.Turn.Error.Message
			}
			err = d.control.HostCompleted(d.lifecycle, thread, p.Turn.ID, control.Status(p.Turn.Status), failure)
		}
		if err == nil && (event.method == "turn/completed" || event.method == "human/interrupt" || event.method == "") {
			err = d.DispatchPending(d.lifecycle, thread)
			err = errors.Join(err, d.Wake(d.lifecycle, thread, time.Now()))
			if member, ok := d.control.Member(thread); ok && member.Parent != "" {
				err = errors.Join(err, d.Wake(d.lifecycle, member.Parent, time.Now()))
			}
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
