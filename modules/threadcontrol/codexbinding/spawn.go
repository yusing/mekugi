package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// ChildAudience is the existing application audience owner. Admission precedes
// any assignment, so provider/observation traffic cannot race logical ownership.
type ChildAudience interface {
	ObserveAdmitted(thread, parent, name string, child bool) bool
}

type SpawnResult struct {
	Thread string `json:"thread_id"`
	Task   string `json:"task_name"`
	State  string `json:"state"`
}

// SpawnPrepared creates an independent thread using a frozen preparation. It
// borrows the one host ledger; uncertain effects remain retained and inspectable.
func (d *Driver) SpawnPrepared(ctx context.Context, caller control.Identity, prepared PreparedChild) (SpawnResult, error) {
	if prepared.FullHistory && prepared.Import == "" {
		return SpawnResult{}, errors.New("full-history spawn requires an admitted sanitized fork import")
	}
	spawn, err := d.control.ReserveSpawn(ctx, caller, control.SpawnRequest{Name: prepared.TaskName, Settings: prepared.Settings, Limits: prepared.Limits, Message: prepared.Message, Arguments: prepared.Arguments, Import: prepared.Import, Preload: prepared.Preload})
	if err != nil {
		return SpawnResult{}, err
	}
	if spawn.Target != "" {
		if err := d.Reconcile(ctx, spawn.Target); err != nil {
			return SpawnResult{}, err
		}
	}
	for {
		changed, err := d.control.Changes(caller.Thread)
		if err != nil {
			return SpawnResult{}, err
		}
		created, err := d.control.Effect(caller.Thread, spawn.ID)
		if err != nil {
			return SpawnResult{}, err
		}
		result := SpawnResult{Thread: created.Target, Task: created.Child.Task, State: created.State}
		switch created.State {
		case "confirmed":
			return result, nil
		case "pending", "queued":
			// Advance only this spawn. Draining the caller's entire queue can
			// claim a sibling and leave that sibling's own call unconfirmed.
			action, err := d.control.ClaimEffect(ctx, caller.Thread, spawn.ID)
			if err != nil {
				return result, err
			}
			if action != nil {
				if err := d.dispatchClaimedAction(ctx, action); err != nil {
					return result, err
				}
				continue
			}
		case "dispatching":
			// A lifecycle worker may already own this phase. Its acknowledgement,
			// not a second dispatch or an optimistic receipt, releases this call.
		default:
			err := fmt.Errorf("spawn is %s; no repeated dispatch was issued", result.State)
			if created.Error != "" {
				err = errors.Join(err, errors.New(created.Error))
			}
			return result, err
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

func sameJSON(a, b jsontext.Value) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}
