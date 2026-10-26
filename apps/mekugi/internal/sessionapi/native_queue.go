package sessionapi

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// Inspect only delivery identity. Rendering payloads remain raw, including
// fields unknown to this queue and exact integers inside retained output.
type nativeQueueEvent struct {
	Retained   nativeOwners `json:"retained"`
	Attachment string       `json:"attachment"`
	Startup    string       `json:"startup"`
	Frame      *struct {
		View struct {
			Version sessionpresentation.Version
			Main    struct{ ID string }
		} `json:"view"`
		Switch          *NativeSwitch         `json:"switch"`
		MainOutput      []jsontext.Value      `json:"mainOutput"`
		AgentOutput     []jsontext.Value      `json:"agentOutput"`
		ReacquireOutput bool                  `json:"reacquireOutput"`
		Effects         []struct{ ID uint64 } `json:"effects"`
	} `json:"frame"`
}

// Complete state can replace earlier state, including a queued snapshot. Keep
// its snapshot marker for a reader that has not started. Switches remain ordered
// boundaries; acquired output can travel with newer state only in the same
// presentation attachment. The owner's latest frame repeats an ordered prefix
// of unacknowledged effects. A shorter prefix remains pending at that owner;
// only client receipts release effects, never replacement of a queued frame.
func coalesceNative(previous, next publication) (publication, bool) {
	if (previous.Kind != "snapshot" && previous.Kind != "patch") || (next.Kind != "snapshot" && next.Kind != "patch") {
		return publication{}, false
	}
	var before, after nativeQueueEvent
	if json.Unmarshal(previous.Data, &before) != nil || json.Unmarshal(next.Data, &after) != nil || before.Frame == nil || after.Frame == nil || before.Attachment == "" || before.Attachment != after.Attachment {
		return publication{}, false
	}
	old, current := before.Frame, after.Frame
	if after.Retained != 0 && (next.Kind == "snapshot" || old.View.Version.Attachment != current.View.Version.Attachment || old.View.Main.ID != current.View.Main.ID || current.Switch != nil) {
		return publication{}, false
	}
	hasOutput := len(old.MainOutput) != 0 || len(old.AgentOutput) != 0
	if old.Switch != nil || hasOutput {
		if current.Switch != nil || old.View.Main.ID != current.View.Main.ID || before.Startup != after.Startup || old.View.Version.Attachment != current.View.Version.Attachment {
			return publication{}, false
		}
	} else if old.View.Main.ID != current.View.Main.ID && current.Switch == nil {
		return publication{}, false
	}
	merged := next
	if previous.Kind == "snapshot" {
		merged.Kind = "snapshot"
	}
	if after.Retained & ^before.Retained == 0 && old.Switch == nil && !hasOutput && (!old.ReacquireOutput || current.ReacquireOutput) {
		return merged, true
	}
	var envelope, frame map[string]jsontext.Value
	if json.Unmarshal(next.Data, &envelope) != nil || json.Unmarshal(envelope["frame"], &frame) != nil {
		return publication{}, false
	}
	if after.Retained & ^before.Retained != 0 {
		var oldEnvelope, oldFrame map[string]jsontext.Value
		if json.Unmarshal(previous.Data, &oldEnvelope) != nil || json.Unmarshal(oldEnvelope["frame"], &oldFrame) != nil {
			return publication{}, false
		}
		for i, field := range []string{"main", "agents", "review", "family"} {
			bit := nativeOwners(1 << i)
			if after.Retained&bit != 0 && before.Retained&bit == 0 {
				frame[field] = oldFrame[field]
			}
		}
		envelope["retained"], _ = json.Marshal(after.Retained&before.Retained, nativeEncoding)
	}
	if old.Switch != nil {
		frame["switch"], _ = json.Marshal(old.Switch, nativeEncoding)
	}
	if old.ReacquireOutput {
		frame["reacquireOutput"] = jsontext.Value(`true`)
	}
	for _, output := range []struct {
		field             string
		previous, current []jsontext.Value
	}{{"mainOutput", old.MainOutput, current.MainOutput}, {"agentOutput", old.AgentOutput, current.AgentOutput}} {
		if len(output.previous) == 0 {
			continue
		}
		merged, ok := coalesceNativeOutput(output.previous, output.current)
		if !ok {
			return publication{}, false
		}
		frame[output.field], _ = json.Marshal(merged, nativeEncoding)
	}
	var err error
	envelope["frame"], err = json.Marshal(frame, nativeEncoding)
	if err != nil {
		return publication{}, false
	}
	merged.Data, err = json.Marshal(envelope, nativeEncoding)
	return merged, err == nil
}

// Hydration consumers correlate by the full demand, not a request ID. Repeated
// acquisitions of that immutable output replace each other. Byte budgets and
// versions remain distinct, as do sources and outputs within each feed.
func coalesceNativeOutput(previous, current []jsontext.Value) ([]jsontext.Value, bool) {
	seen := make(map[activity.OutputDemand]bool, len(previous)+len(current))
	merged := make([]jsontext.Value, 0, len(previous)+len(current))
	for _, batch := range [][]jsontext.Value{current, previous} {
		for i := len(batch) - 1; i >= 0; i-- {
			result := batch[i]
			var identity struct{ Demand activity.OutputDemand }
			if json.Unmarshal(result, &identity) != nil {
				return nil, false
			}
			if !seen[identity.Demand] {
				seen[identity.Demand] = true
				merged = append(merged, result)
			}
		}
	}
	slices.Reverse(merged)
	return merged, true
}

// This is a backlog target, not a maximum readable output. A newest lone result
// may exceed it, subject to the existing complete-publication transport limit.
const nativeOutputBacklogBytes = 8 << 20

// Bound transient hydration independently from semantic effects and state. A
// compacted result remains recoverable at its source; the marker tells readers
// to reacquire their unresolved demands, never to treat missing data as empty.
// Both queues normalize after folding and also on their first publication.
func normalizeNativeOutput(event publication) publication {
	return normalizeNativeOutputLimit(event, nativePublicationBytes)
}

func normalizeNativeOutputLimit(event publication, limit int) publication {
	if event.Kind != "snapshot" && event.Kind != "patch" {
		return event
	}
	// Each output array fits inside the complete publication. Below the
	// per-feed budget neither trimming loop can remove anything, so avoid
	// parsing the entire transcript on both the server and the client.
	if len(event.Data) <= nativeOutputBacklogBytes && len(event.Data) <= limit {
		return event
	}
	var decoded nativeQueueEvent
	if json.Unmarshal(event.Data, &decoded) != nil || decoded.Frame == nil {
		return event
	}
	outputs := [2][]jsontext.Value{decoded.Frame.MainOutput, decoded.Frame.AgentOutput}
	var sizes [2]int
	size := len(event.Data) + len(`,"reacquireOutput":true`)
	removed := false
	drop := func(feed int) {
		bytes := len(outputs[feed][0])
		if len(outputs[feed]) > 1 {
			bytes++ // the removed array separator
		}
		size -= bytes
		sizes[feed] -= bytes
		outputs[feed] = outputs[feed][1:]
		removed = true
	}
	for i, results := range outputs {
		sizes[i] = 2 // array brackets
		for j, result := range results {
			sizes[i] += len(result)
			if j > 0 {
				sizes[i]++
			}
		}
		for sizes[i] > nativeOutputBacklogBytes && len(outputs[i]) > 1 {
			drop(i)
		}
	}
	// Two individually deliverable large results, or a large complete frame,
	// can need a narrower window than the per-feed target. Keep one whole
	// result when there are no semantic consequences, so an indivisible oversized
	// response fails honestly. Consequences take priority over hydration: its last
	// result can be reacquired after the semantic prefix has been acknowledged.
	minimum := 1
	if len(decoded.Frame.Effects) > 0 {
		minimum = 0
	}
	for size > limit && len(outputs[0])+len(outputs[1]) > minimum {
		feed := 0
		if len(outputs[0]) == 0 || len(outputs[1]) > 0 && sizes[1] > sizes[0] {
			feed = 1
		}
		drop(feed)
	}
	if !removed {
		return event
	}
	var envelope, frame map[string]jsontext.Value
	if json.Unmarshal(event.Data, &envelope) != nil || json.Unmarshal(envelope["frame"], &frame) != nil {
		return event
	}
	frame["mainOutput"], _ = json.Marshal(outputs[0], nativeEncoding)
	frame["agentOutput"], _ = json.Marshal(outputs[1], nativeEncoding)
	frame["reacquireOutput"] = jsontext.Value(`true`)
	envelope["frame"], _ = json.Marshal(frame, nativeEncoding)
	data, err := json.Marshal(envelope, nativeEncoding)
	if err != nil {
		return event
	}
	event.Data = data
	return event
}
