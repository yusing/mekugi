// Package codexbinding adapts independent thread coordination to the selected
// Codex host. Session Control remains the only request/connection owner.
package codexbinding

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type Driver struct {
	control      *control.Service
	mu           sync.RWMutex
	host         control.Native
	audience     ChildAudience
	models       []Model
	catalogError error
	lifecycle    context.Context
	cancel       context.CancelFunc
	thread       string
	pending      []hostObservation
	wake         chan struct{}
	failures     map[string]error
	changed      chan struct{}
	stopped      chan struct{}
}

func NewDriver(service *control.Service) (*Driver, error) {
	if service == nil {
		return nil, errors.New("thread control requires its admitted service")
	}
	return &Driver{control: service, wake: make(chan struct{}, 1), failures: make(map[string]error), changed: make(chan struct{}, 1), stopped: make(chan struct{})}, nil
}

// Connect borrows the sole Session Control ledger after the invocation transport
// has been constructed. Tool schemas exist before the host starts; effects do not.
func (d *Driver) Connect(host control.Native, audience ChildAudience) error {
	if host == nil || audience == nil {
		return errors.New("thread control requires host and audience owners")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.host != nil {
		return errors.New("thread control is already connected")
	}
	d.host, d.audience = host, audience
	return nil
}

// Coordination exposes the actual feature owner to observation and lifecycle
// consumers, without reconstructing its store or host connection.
func (d *Driver) Coordination() *control.Service { return d.control }

// DispatchPending delivers already-admitted controls released by host settlement.
// Claims serialize each target; an unresolved effect leaves its successors queued.
func (d *Driver) DispatchPending(ctx context.Context, thread string) error {
	for {
		tree, err := d.control.Snapshot(thread)
		if err != nil {
			return err
		}
		progressed := false
		for _, pending := range tree.Actions() {
			if pending.Target != thread && pending.Caller != thread || pending.State != "pending" && pending.State != "queued" {
				continue
			}
			action, err := d.control.ClaimEffect(ctx, pending.Caller, pending.ID)
			if err != nil {
				return err
			}
			if action == nil {
				continue
			}
			if err := d.dispatchClaimedAction(ctx, action); err != nil {
				return err
			}
			progressed = true
			break // Re-read to include controls queued during this dispatch.
		}
		if !progressed {
			return nil
		}
	}
}

func (d *Driver) dispatchClaimedAction(ctx context.Context, effect *control.Effect) error {
	tree, err := d.control.Snapshot(effect.Caller)
	if err != nil {
		return err
	}
	kind := effect.Kind
	if kind == "create" {
		kind = effect.Phase
	}
	var receipt control.Receipt
	var callErr error
	switch kind {
	case "human":
		return errors.New("human input must be dispatched by its ordinary request owner")
	case "create":
		path := ""
		if effect.Child.Import != "" {
			path, callErr = d.control.ImportPath(effect.Caller, effect.Child.Import)
		}
		if callErr == nil {
			receipt, callErr = d.native().Create(ctx, effect.Caller, path, effect.Child.Settings)
		}
	case "admit":
		callErr = d.control.AdmitNamespace(ctx, effect.Target)
		if callErr == nil {
			d.mu.RLock()
			audience := d.audience
			d.mu.RUnlock()
			if audience == nil || !audience.Observe(effect.Target, effect.Caller, tree.Members[effect.Target].Task, true) {
				callErr = &binding.DispatchError{Outcome: binding.NotDispatched, Cause: errors.New("confirmed child could not enter the application audience")}
			}
		}
	default:
		var owner control.Runtime
		owner, callErr = d.native().Resume(ctx, effect.Target)
		if callErr != nil {
			callErr = &binding.DispatchError{Outcome: binding.NotDispatched, Cause: callErr}
		}
		if callErr == nil {
			if kind == "interrupt" {
				callErr = owner.Interrupt(ctx, effect.Target, effect.Turn)
			} else {
				receipt.Turn, callErr = owner.Input(ctx, nativeInput(tree, *effect))
			}
		}
	}
	if callErr != nil {
		receipt.Error = callErr.Error()
		failure, known := errors.AsType[*binding.DispatchError](callErr)
		receipt.Uncertain = !known || failure.Outcome == binding.Uncertain
	}
	receipt.Phase = effect.Phase
	return errors.Join(callErr, d.control.EffectAcknowledged(context.WithoutCancel(ctx), effect.Caller, effect.ID, receipt))
}

func (d *Driver) Wake(ctx context.Context, thread string, now time.Time) error {
	wake, err := d.control.PrepareWake(ctx, thread, now)
	if err != nil || wake == nil {
		return err
	}
	return d.DispatchPending(ctx, thread)
}

func (d *Driver) native() control.Native {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.host
}

func (d *Driver) runtime(thread string) (control.Runtime, error) {
	owner, err := d.native().Runtime(thread)
	if err != nil {
		return nil, &binding.DispatchError{Outcome: binding.NotDispatched, Cause: err}
	}
	return owner, nil
}

func nativeInput(tree control.Tree, effect control.Effect) control.NativeInput {
	kind := effect.Kind
	if kind == "create" {
		kind = effect.Phase
	}
	input := control.NativeInput{Thread: effect.Target, ParentTurn: effect.CallerTurn, ID: effect.Caller + ":" + effect.ID, Sender: tree.Members[effect.Caller].Task, Recipient: tree.Members[effect.Target].Task, Kind: "MESSAGE", Message: effect.Message, Wake: effect.Wake}
	switch kind {
	case "human":
		input.Kind, input.ID, input.TargetTurn = "HUMAN_INPUT", effect.ID, effect.Turn
	case "interrupt":
		input.Kind, input.TargetTurn = "INTERRUPT", effect.Turn
	case "initial":
		settings := tree.Members[effect.Target].Settings
		input.Run, input.Settings, input.Message = true, &settings, ""
	case "wake":
		input.Run = true
	case "followup", "assignment_input":
		input.Kind, input.Run = "NEW_TASK", kind == "followup"
		if kind == "assignment_input" {
			input.ID += ":assignment"
		}
	case "final":
		input.Kind, input.ID = "FINAL_ANSWER", effect.ID
		if effect.Outcome != control.Completed {
			input.Message = fmt.Sprintf("Status: %s\n%s", effect.Outcome, effect.Message)
		}
	}
	return input
}
