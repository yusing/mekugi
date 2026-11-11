package codexbinding

import (
	"context"
	"errors"
	"time"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (d *Driver) peer(caller control.Identity, target string) (control.Peer, error) {
	if _, err := d.control.Caller(caller); err != nil {
		return control.Peer{}, err
	}
	resolver, ok := d.native().(control.PeerResolver)
	if !ok {
		return control.Peer{}, errors.New("target is outside the admitted tree and no active session resolver is available")
	}
	return resolver.Peer(caller, target)
}

func (d *Driver) NotifyCoordination(thread string) { d.enqueue(hostObservation{thread: thread}) }

// DeliverMessages uses the receiver's ordinary effect claims and wake lifecycle.
func (d *Driver) DeliverMessages(ctx context.Context, thread string) error {
	d.mu.RLock()
	lifecycle := d.lifecycle
	d.mu.RUnlock()
	if lifecycle == nil {
		return errors.New("message delivery requires an active lifecycle")
	}
	if err := lifecycle.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()
	if err := d.Reconcile(ctx, thread); err != nil {
		return err
	}
	if err := d.dispatchPending(ctx, thread, thread, nil); err != nil {
		return err
	}
	if err := lifecycle.Err(); err != nil {
		return err
	}
	if err := d.Wake(ctx, thread, time.Now()); err != nil {
		return err
	}
	d.NotifyCoordination(thread)
	return nil
}

func (d *Driver) messagePeer(ctx context.Context, caller control.Identity, target, message string, btw bool, dispatched *bool) (any, error) {
	peer, err := d.peer(caller, target)
	if err != nil {
		return nil, err
	}
	if peer.Source.Session == peer.Target.Session {
		return nil, errors.New("target is not an admitted foreign session")
	}
	if btw {
		member, ok := peer.Control.Member(peer.Target.Thread)
		if !ok {
			return nil, control.ErrNotAdmitted
		}
		sources, err := d.freezeMessage(ctx, caller, "peer-btw", message, member, peer.Runtime)
		if err != nil {
			return nil, err
		}
		if sources != "" {
			message, err = d.control.MessageText(caller.Thread, sources, message)
			if err != nil {
				return nil, err
			}
		}
		d.control.RecordFeature(ctx, caller.Thread, caller.Call, control.FeatureEvent{Kind: control.FeatureMessageAccepted, BTW: true, Sources: sources != ""})
		*dispatched = true
		answer, err := peer.Runtime.SideQuestion(ctx, member, peer.Source.Sender()+"\n"+message)
		if err != nil {
			return nil, err
		}
		current, err := d.peer(caller, target)
		if err != nil || current.Target.Session != peer.Target.Session || current.Target.Generation != peer.Target.Generation {
			return nil, errors.Join(err, errors.New("side-question target generation changed"))
		}
		return map[string]any{"thread_id": target, "task_name": peer.Target.Task, "session_id": peer.Target.Session, "answer": answer}, nil
	}
	sources := ""
	if selection := messageSelection(message); selection != "" {
		member, ok := peer.Control.Member(peer.Target.Thread)
		if !ok {
			return nil, control.ErrNotAdmitted
		}
		sources, err = peer.Control.FreezePeerMessage(ctx, peer.Source, target, caller.Call, message, func() (string, error) {
			return acquireMessageSources(ctx, peer.Runtime, member, selection)
		})
		if err != nil {
			return nil, err
		}
	}
	action, err := peer.Control.ReceiveMessageWithSources(ctx, peer.Source, target, caller.Call, message, sources)
	if err != nil {
		return nil, err
	}
	d.control.RecordFeature(ctx, caller.Thread, caller.Call, control.FeatureEvent{Kind: control.FeatureMessageAccepted, Sources: sources != ""})
	if err := peer.Deliver(ctx, target); err != nil {
		return nil, err
	}
	retained, err := peer.Control.Effect(target, action.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": action.ID, "state": retained.State, "thread_id": target, "task_name": peer.Target.Task, "session_id": peer.Target.Session}, nil
}
