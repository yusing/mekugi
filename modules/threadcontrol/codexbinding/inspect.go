package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (d *Driver) inspectAgent(ctx context.Context, caller control.Identity, raw jsontext.Value) (any, error) {
	var args struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	target, err := d.control.Target(caller, args.Target)
	if err != nil {
		return nil, err
	}
	details, err := d.native().Inspect(ctx, target.Thread)
	if err != nil {
		return nil, err
	}
	return struct {
		Thread string `json:"thread_id"`
		Task   string `json:"task_name"`
		control.SessionDetails
	}{target.Thread, target.Task, details}, nil
}

// Auxiliary metrics never change a successful operation's acknowledgement.
func (d *Driver) sessionMetrics(ctx context.Context, thread string) control.SessionMetrics {
	if details, err := d.native().Inspect(ctx, thread); err == nil {
		return details.SessionMetrics
	}
	return control.SessionMetrics{}
}

func (d *Driver) waitMetrics(ctx context.Context, thread string, result *control.WaitResult) {
	result.Metrics = make(map[string]control.SessionMetrics, len(result.Targets))
	for _, target := range result.Targets {
		result.Metrics[target] = control.SessionMetrics{}
	}
	for _, member := range d.control.Descendants(thread) {
		if _, selected := result.Metrics[member.Task]; selected {
			result.Metrics[member.Task] = d.sessionMetrics(ctx, member.Thread)
		}
	}
}
