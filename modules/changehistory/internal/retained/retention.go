package retained

// Source: internal/router/change_retention.go@[1a1981ba] retained change behavior.

import (
	"context"
	"maps"
	"slices"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func changeRetirementPlanner(original changeIndex) recordkind.RetirementPlanner {
	return func(ctx context.Context, retirement recordkind.Retirement) (*recordkind.RetirementUpdate, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		index := original
		if !retireStoredChanges(&index, retirement.Deleted, retirement.Thread) {
			return nil, nil
		}
		data, err := marshalIndex(&index)
		if err != nil {
			return nil, err
		}
		return &recordkind.RetirementUpdate{Data: data, Next: changeRetirementPlanner(index)}, nil
	}
}

// Source: internal/router/storage_retention.go:616:654@efcbf71f retireStoredChanges.
// Only deletion facts from this locked cleanup may prune an index. An unrelated
// missing replay file remains corruption, not an apparently successful cleanup.
func retireStoredChanges(index *changeIndex, deleted map[string]bool, thread string) bool {
	changed := false
	position := slices.IndexFunc(index.Streams, func(candidate changeStream) bool {
		return candidate.Thread == thread && thread != ""
	})
	for id, change := range index.Changes {
		stream, _, _ := changes.ParseID(id)
		pending := len(change.Calls) == 0 && position >= 0 && index.streamName(position) == stream
		if !pending && !slices.ContainsFunc(change.Calls, func(call trackedCall) bool {
			return deleted[legacycall.Name(index.Workspace, call.ID, false)]
		}) {
			continue
		}
		if !changed {
			index.Streams = slices.Clone(index.Streams)
			index.Changes = maps.Clone(index.Changes)
			changed = true
		}
		before := len(change.Calls)
		change.Calls = slices.DeleteFunc(slices.Clone(change.Calls), func(call trackedCall) bool {
			return deleted[legacycall.Name(index.Workspace, call.ID, false)]
		})
		if len(change.Calls) == 0 {
			delete(index.Changes, id)
			for position := range index.Streams {
				if index.streamName(position) == stream {
					index.Streams[position].Retired++
					break
				}
			}
		} else {
			change.RetiredCalls += before - len(change.Calls)
			index.Changes[id] = change
		}
	}
	return changed
}

// Source: internal/router/storage_retention.go:656:698@efcbf71f reconcileRetiredChanges.
func (s *store) reconcileRetiredChanges(index *changeIndex) (bool, error) {
	copy := *s
	copy.session.Namespace = index.Namespace
	s = &copy
	current, err := s.readChangeIndex(index.Workspace)
	if err != nil {
		return false, err
	}
	changed := false
	for position, stream := range current.Streams {
		if position >= len(index.Streams) || stream.Retired <= index.Streams[position].Retired {
			continue
		}
		for id := range index.Changes {
			owner, number, _ := changes.ParseID(id)
			if owner == index.streamName(position) && number <= stream.Next {
				if _, exists := current.Changes[id]; !exists {
					delete(index.Changes, id)
				}
			}
		}
		index.Streams[position].Retired = stream.Retired
		changed = true
	}
	for id, change := range index.Changes {
		if prior, exists := current.Changes[id]; exists && prior.RetiredCalls > change.RetiredCalls {
			var retained []trackedCall
			for _, call := range change.Calls {
				_, found, err := s.read(index.Workspace, call.ID, false)
				if err != nil {
					return false, err
				}
				if found {
					retained = append(retained, call)
				}
			}
			change.Calls, change.RetiredCalls = retained, prior.RetiredCalls
			index.Changes[id] = change
			changed = true
		}
	}
	return changed, nil
}
