package sessionapi

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"reflect"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// Sizes belong to one immutable top-level feed, never roster summaries, whose
// sequences can overlap. They measure reconnect state without encoding retained
// entries again on every update.
type nativeFeedState struct {
	owner      *activity.Publication
	entryBytes map[uint64]int
}

type nativeEncodedFeed struct {
	data      []byte
	fullBytes int
	delta     bool
	state     nativeFeedState
}

type nativeFeedEncoding [2]nativeEncodedFeed

func encodeNativeFeed(owner *activity.Publication, previous nativeFeedState, allowDelta bool) (nativeEncodedFeed, error) {
	result := nativeEncodedFeed{state: nativeFeedState{owner: owner}}
	old := make(map[uint64]activity.Entry)
	if allowDelta && previous.owner != nil && previous.entryBytes != nil {
		for _, entry := range previous.owner.Entries {
			old[entry.Seq] = entry
		}
	}
	if owner != nil {
		result.state.entryBytes = make(map[uint64]int, len(owner.Entries))
		for _, entry := range owner.Entries {
			if entry.Seq == 0 || result.state.entryBytes[entry.Seq] != 0 {
				// Unsuitable membership uses the ordinary complete encoder.
				result.state.entryBytes = nil
				break
			}
			result.state.entryBytes[entry.Seq] = 1
		}
	}
	if owner == nil || result.state.entryBytes == nil {
		data, err := json.Marshal(owner, nativeEncoding)
		result.data, result.fullBytes = data, len(data)
		return result, err
	}
	entries := make([]jsontext.Value, len(owner.Entries))
	saved := 0
	for i, entry := range owner.Entries {
		var data []byte
		var err error
		size := 0
		if prior, ok := old[entry.Seq]; ok && reflect.DeepEqual(prior, entry) {
			data, err = json.Marshal(entry.Seq)
			size = previous.entryBytes[entry.Seq]
			result.delta = true
		} else {
			data, err = json.Marshal(entry, nativeEncoding)
			size = len(data)
		}
		if err != nil {
			return nativeEncodedFeed{}, err
		}
		entries[i] = data
		result.state.entryBytes[entry.Seq] = size
		saved += size - len(data)
	}
	value := struct {
		*activity.Publication
		Entries []jsontext.Value
	}{owner, entries}
	data, err := json.Marshal(value, nativeEncoding)
	if err != nil {
		return nativeEncodedFeed{}, err
	}
	result.fullBytes = len(data) + saved
	// Charge the complete mask overhead to each candidate. Even one changed
	// owner must save bytes once the transport flag is included.
	if result.delta && saved <= len(`,"entryDeltas":3`) {
		result.delta = false
		data, err = json.Marshal(owner, nativeEncoding)
	}
	result.data = data
	return result, err
}

// Only flagged owners pass through raw membership. Unflagged publications keep
// their ordinary typed decoder, and references borrow immutable entry values
// into a newly allocated membership slice.
func decodeNativeFeed(data jsontext.Value, previous *activity.Publication) (*activity.Publication, error) {
	if previous == nil || data.Kind() != '{' {
		return nil, errors.New("native entry delta has no owner snapshot")
	}
	owner := new(activity.Publication)
	var raw struct {
		*activity.Publication
		Entries []jsontext.Value
	}
	raw.Publication = owner
	if err := json.Unmarshal(data, &raw, nativeEncoding); err != nil {
		return nil, err
	}
	if raw.Entries == nil {
		return nil, errors.New("native entry delta is missing its membership array")
	}
	old := make(map[uint64]activity.Entry, len(previous.Entries))
	for _, entry := range previous.Entries {
		if entry.Seq == 0 {
			return nil, errors.New("native entry delta has unsuitable prior membership")
		}
		if _, duplicate := old[entry.Seq]; duplicate {
			return nil, errors.New("native entry delta has duplicate prior membership")
		}
		old[entry.Seq] = entry
	}
	owner.Entries = make([]activity.Entry, len(raw.Entries))
	seen := make(map[uint64]bool, len(raw.Entries))
	for i, value := range raw.Entries {
		var entry activity.Entry
		if value.Kind() == '{' {
			if err := json.Unmarshal(value, &entry, nativeEncoding); err != nil {
				return nil, err
			}
		} else {
			var seq uint64
			if err := json.Unmarshal(value, &seq); err != nil || value.Kind() != '0' {
				return nil, errors.New("native entry delta has an invalid reference")
			}
			var ok bool
			entry, ok = old[seq]
			if !ok {
				return nil, errors.New("native entry delta references missing membership")
			}
		}
		if entry.Seq == 0 || seen[entry.Seq] {
			return nil, errors.New("native entry delta has missing or duplicate membership")
		}
		seen[entry.Seq] = true
		owner.Entries[i] = entry
	}
	return owner, nil
}

// Substitute references against a queued owner, which can itself contain
// baseline references. Raw objects preserve extension fields and exact numbers.
func coalesceNativeFeed(previous, next jsontext.Value) (jsontext.Value, bool, bool) {
	var before, after map[string]jsontext.Value
	var old, current []jsontext.Value
	if previous.Kind() != '{' || next.Kind() != '{' || json.Unmarshal(previous, &before) != nil || json.Unmarshal(next, &after) != nil || json.Unmarshal(before["Entries"], &old) != nil || json.Unmarshal(after["Entries"], &current) != nil {
		return nil, false, false
	}
	sequence := func(value jsontext.Value) (uint64, bool) {
		if value.Kind() == '{' {
			var identity struct{ Seq uint64 }
			err := json.Unmarshal(value, &identity)
			return identity.Seq, err == nil && identity.Seq != 0
		}
		var seq uint64
		err := json.Unmarshal(value, &seq)
		return seq, err == nil && value.Kind() == '0' && seq != 0
	}
	entries := make(map[uint64]jsontext.Value, len(old))
	for _, value := range old {
		seq, ok := sequence(value)
		if !ok || entries[seq] != nil {
			return nil, false, false
		}
		entries[seq] = value
	}
	delta := false
	seen := make(map[uint64]bool, len(current))
	for i, value := range current {
		seq, ok := sequence(value)
		if !ok || seen[seq] {
			return nil, false, false
		}
		seen[seq] = true
		if value.Kind() != '{' {
			value = entries[seq]
			if value == nil {
				return nil, false, false
			}
			current[i] = value
		}
		delta = delta || value.Kind() != '{'
	}
	var err error
	after["Entries"], err = json.Marshal(current, nativeEncoding)
	if err != nil {
		return nil, false, false
	}
	data, err := json.Marshal(after, nativeEncoding)
	return data, delta, err == nil
}
