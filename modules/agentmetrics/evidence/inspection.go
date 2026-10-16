// Source: internal/router/session_inspect.go@[d9f28a74] whole-rollout AX report.
package evidence

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/contracts/inspection"
)

type InspectionReport struct {
	Commands       AXCommandMetrics    `json:"commands"`
	Scope          string              `json:"scope"`
	ThreadID       string              `json:"thread_id"`
	Edits          AXEditMetrics       `json:"edits"`
	Reads          AXReadMetrics       `json:"reads"`
	Completion     AXCompletionMetrics `json:"completion"`
	Defects        AXDefectMetrics     `json:"defect_assessments"`
	UnmatchedCalls uint64              `json:"unmatched_calls"`
}

type inspectionCollector struct {
	Completion AXCompletionAccumulator
	Commands   AXCommandAccumulator
	edits      AXEditAccumulator
	calls      map[string]bool
	reads      *AXReadMetrics
}

// NewInspection optionally reuses an already validated read-log selection.
func (*Service) NewInspection(reads *AXReadMetrics) inspection.AXCollector {
	return &inspectionCollector{calls: make(map[string]bool), reads: reads}
}
func (observations *inspectionCollector) Observe(event inspection.Event) error {
	switch event.Kind {
	case "item_started", "item_completed":
		if event.Kind == "item_completed" && event.PersistedTimes {
			return observations.Commands.ObserveCompleted(event.ItemID, event.CallID, event.Started, event.Completed, event.ExitCode)
		}
		return observations.Commands.Observe(event.Kind, event.ItemID, event.CallID, event.At, event.ExitCode)
	case "task_started", "task_complete", "turn_started", "turn_complete":
		observations.Completion.Observe(event.Kind, event.TurnID, event.At)
	}
	return nil
}

func (c *inspectionCollector) ObserveEdit(edit inspection.Edit) {
	c.calls[edit.CallID] = true
	c.edits.Observe(edit.Script, edit.Failed, edit.Unconfirmed)
}
func (c *inspectionCollector) Report(ctx context.Context, options inspection.AXOptions) (jsontext.Value, error) {
	result := InspectionReport{Scope: "entire supplied rollout; edit metrics require matched observed patches",
		ThreadID: options.ThreadID, Completion: c.Completion.Result(), Commands: c.Commands.Result(), Edits: c.edits.Metrics, UnmatchedCalls: options.UnmatchedCalls}
	var err error
	if c.reads != nil {
		result.Reads = *c.reads
	} else {
		result.Reads, err = new(Service).ReadReads(ctx, options.ReadLog, options.ThreadID)
		if err != nil {
			return nil, err
		}
	}
	result.Defects, err = new(Service).ReadDefects(options.Defects, c.calls)
	if err != nil {
		return nil, err
	}
	// The retained session report keeps its established zero/nil and escaping semantics.
	return json.Marshal(&result, jsonv1.DefaultOptionsV1(), jsontext.EscapeForHTML(false))
}
