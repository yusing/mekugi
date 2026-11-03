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
	if err := d.DispatchPending(ctx, thread); err != nil {
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

func (d *Driver) messagePeer(ctx context.Context, caller control.Identity, target, message string, btw bool) (any, error) {
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
		d.control.ObserveFeature(caller.Thread, control.FeatureSideQuestion)
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
	action, err := peer.Control.ReceiveMessage(ctx, peer.Source, target, caller.Call, message)
	if err != nil {
		return nil, err
	}
	if err := peer.Deliver(ctx, target); err != nil {
		return nil, err
	}
	retained, err := peer.Control.Effect(target, action.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": action.ID, "state": retained.State, "thread_id": target, "task_name": peer.Target.Task, "session_id": peer.Target.Session}, nil
}

func (d *Driver) foreignSelection(caller control.Identity, targets []string) (map[string]control.Peer, error) {
	peers := make(map[string]control.Peer)
	for _, target := range targets {
		if _, err := d.control.Target(caller, target); err == nil {
			continue
		}
		peer, err := d.peer(caller, target)
		if err != nil {
			return nil, err
		}
		if peer.Source.Session == peer.Target.Session {
			return nil, errors.New("wait target is not an admitted descendant or foreign session")
		}
		peers[target] = peer
	}
	return peers, nil
}

// Each observer borrows two publication notifications and the selected runtime
// lifetime. It owns no dispatch state: cancellation and outcomes stay in Wait.
func (d *Driver) watchForeign(caller control.Identity, id string, peers map[string]control.Peer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for target, peer := range peers {
		key := caller.Thread + ":" + id + ":" + target
		if d.foreignWatches[key] {
			continue
		}
		if d.foreignWatches == nil {
			d.foreignWatches = make(map[string]bool)
		}
		d.foreignWatches[key] = true
		d.auxiliary.Go(func() {
			defer func() { d.mu.Lock(); delete(d.foreignWatches, key); d.mu.Unlock() }()
			for {
				localChanged, err := d.control.Changes(caller.Thread)
				if err != nil {
					return
				}
				selected := d.control.ForeignWait(caller.Thread, id)
				if _, ok := selected[target]; !ok {
					return
				}
				remoteChanged, err := peer.Control.Changes(target)
				if err != nil {
					return
				}
				state, err := peer.Control.RemoteTarget(target)
				select {
				case <-peer.Done:
					err = errors.New("foreign runtime ended")
				default:
				}
				if err != nil {
					state = peer.Target
					state.Status, state.HostRunning, state.Unavailable = control.Failed, false, true
				}
				if selected[target] != state {
					if updateErr := d.control.RefreshForeignWait(d.lifecycle, caller.Thread, id, state); updateErr != nil {
						return
					}
					d.NotifyCoordination(caller.Thread)
				}
				if state.Unavailable {
					return
				}
				select {
				case <-d.lifecycle.Done():
					return
				case <-localChanged:
				case <-remoteChanged:
				case <-peer.Done:
				}
			}
		})
	}
}
