package codexbinding

import (
	"context"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Reconcile confirms retained target outcomes before explicit dependent work.
// It never starts runtimes, rearms waits or redispatches missing effects.
func (d *Driver) Reconcile(ctx context.Context, thread string) error {
	tree, err := d.control.Snapshot(thread)
	if err != nil {
		return err
	}
	var failures []error
	for _, action := range tree.Actions() {
		if action.Target != thread || action.State != "uncertain" {
			continue
		}
		kind := action.Kind
		if kind == "create" {
			kind = action.Phase
		}
		switch kind {
		case "message", "final", "assignment_input", "wake", "initial", "followup", "interrupt", "human", "completion":
			err = d.native().Confirm(ctx, nativeInput(tree, action))
			if err == nil {
				err = d.control.EffectAcknowledged(context.WithoutCancel(ctx), action.Caller, action.ID, control.Receipt{Reconciled: true, Phase: action.Phase})
			}
		default:
			err = fmt.Errorf("%s outcome has no confirmed retained acknowledgement", kind)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("operation %s requires reconciliation: %w", action.ID, err))
		}
	}
	return errors.Join(failures...)
}

// HumanInput is awaited on the control request worker before host dispatch.
func (d *Driver) HumanInput(ctx context.Context, thread, id string) (*control.Effect, error) {
	if _, ok := d.control.Member(thread); !ok {
		return nil, nil
	}
	if err := d.Reconcile(ctx, thread); err != nil {
		return nil, err
	}
	return d.control.HumanInput(ctx, thread, id)
}
