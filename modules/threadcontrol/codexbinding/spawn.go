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
	Observe(thread, parent, name string, child bool) bool
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
	if err = d.DispatchPending(ctx, caller.Thread); err != nil {
		return SpawnResult{}, err
	}
	tree, err := d.control.Snapshot(caller.Thread)
	if err != nil {
		return SpawnResult{}, err
	}
	created := tree.Effect(caller.Thread, spawn.ID)
	result := SpawnResult{Thread: created.Target, Task: created.Child.Task, State: created.State}
	if result.State != "confirmed" {
		return result, fmt.Errorf("spawn is %s; no repeated dispatch was issued", result.State)
	}
	return result, nil
}

func sameJSON(a, b jsontext.Value) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}
