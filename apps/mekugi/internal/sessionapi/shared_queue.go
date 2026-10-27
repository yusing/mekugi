package sessionapi

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// Shared fields replace whole values, except the independently keyed feeds.
// Keep entry encodings raw: sequence numbers and payload integers can exceed
// float64 precision, and unknown entry fields must survive catch-up unchanged.
func coalesceShared(previous, next publication) (publication, bool) {
	if next.Kind != "patch" || previous.Kind != "snapshot" && previous.Kind != "patch" {
		return publication{}, false
	}
	var before, after map[string]jsontext.Value
	if json.Unmarshal(previous.Data, &before) != nil || before == nil || json.Unmarshal(next.Data, &after) != nil || after == nil {
		return publication{}, false
	}
	for key, value := range after {
		if (key == "main" || key == "agents") && before[key] != nil {
			var oldFeed, newFeed feedPatch
			if json.Unmarshal(before[key], &oldFeed) != nil || json.Unmarshal(value, &newFeed) != nil {
				return publication{}, false
			}
			merged, ok := coalesceFeed(oldFeed, newFeed)
			if !ok {
				return publication{}, false
			}
			var err error
			value, err = json.Marshal(&merged, encoding)
			if err != nil {
				return publication{}, false
			}
		}
		before[key] = value
	}
	data, err := json.Marshal(before, encoding)
	return publication{Kind: previous.Kind, Data: data}, err == nil
}

func coalesceFeed(previous, next feedPatch) (feedPatch, bool) {
	if previous.Attachment != next.Attachment || previous.Revision != next.Revision {
		return next, true
	}
	entries := make(map[uint64]jsontext.Value, len(previous.Entries)+len(next.Entries))
	var sequence []uint64
	for _, batch := range [][]jsontext.Value{previous.Entries, next.Entries} {
		for _, entry := range batch {
			var identity struct {
				Seq uint64 `json:"seq"`
			}
			if json.Unmarshal(entry, &identity) != nil {
				return feedPatch{}, false
			}
			if _, exists := entries[identity.Seq]; !exists {
				sequence = append(sequence, identity.Seq)
			}
			entries[identity.Seq] = entry
		}
	}
	order := previous.Order
	if next.Order != nil {
		order = next.Order
	}
	// A supplied order removes entries, including entries introduced by an
	// earlier queued delta. Empty and omitted order have different meanings.
	if order != nil {
		sequence = order
	}
	merged := next
	merged.Order, merged.Entries = order, nil
	for _, seq := range sequence {
		if entry := entries[seq]; entry != nil {
			merged.Entries = append(merged.Entries, entry)
		}
	}
	return merged, true
}
