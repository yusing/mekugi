package retained

import (
	"cmp"
	json "encoding/json/v2"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func projectChange(value trackedChange) changes.Change {
	result := changes.Change{Correlation: value.Correlation, RetiredCalls: value.RetiredCalls}
	for _, call := range value.Calls {
		result.Calls = append(result.Calls, changes.Call{ID: call.ID, Thread: call.Thread, Sequence: call.Sequence, Managed: call.Managed})
	}
	return result
}
func snapshot(index changeIndex) changes.Snapshot {
	result := changes.Snapshot{Sequence: index.Sequence, Workspace: index.Workspace, Namespace: index.Namespace, Changes: make(map[string]changes.Change, len(index.Changes))}
	for position, stream := range index.Streams {
		result.Streams = append(result.Streams, changes.Stream{Name: index.streamName(position), Thread: stream.Thread, Next: stream.Next, Retired: stream.Retired})
	}
	for id, value := range index.Changes {
		result.Changes[id] = projectChange(value)
	}
	return result
}
func publicationUpdates(index changeIndex, updates map[string][]trackedCall) []changes.Update {
	var result []changes.Update
	for id, calls := range updates {
		name, _, _ := changes.ParseID(id)
		for stream, info := range index.Streams {
			if index.streamName(stream) != name {
				continue
			}
			byThread := make(map[string][]trackedCall)
			for _, call := range calls {
				thread := cmp.Or(call.Thread, info.Thread)
				byThread[thread] = append(byThread[thread], call)
			}
			for _, thread := range slices.Sorted(maps.Keys(byThread)) {
				result = append(result, changes.Update{Workspace: index.Workspace, Namespace: index.Namespace, Thread: thread, Stream: stream, ID: id, Change: projectChange(trackedChange{Correlation: index.Changes[id].Correlation, Calls: byThread[thread]})})
			}
			break
		}
	}
	return result
}

// Source: internal/router/live_diff_data.go:157:203@[1a1981ba] retained membership.
func (s *store) visibleIndexes(workspace string, threads map[string]bool) ([]changeIndex, error) {
	names := map[string]bool{changeIndexName(workspace, ""): true}
	if threads == nil {
		entries, err := os.ReadDir(s.directory)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if hashedRecordName(entry.Name(), changeIndexPrefix) {
				names[entry.Name()] = true
			}
		}
	} else {
		for thread := range threads {
			scope, _, err := s.publication.ReadScope(thread)
			if err != nil {
				return nil, err
			}
			names[changeIndexName(workspace, scope.Namespace)] = true
		}
	}
	var indexes []changeIndex
	for _, name := range slices.Sorted(maps.Keys(names)) {
		data, err := history.ReadRecord(filepath.Join(s.directory, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var index changeIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return nil, err
		}
		if index.Version != 1 || index.Changes == nil || changeIndexName(index.Workspace, index.Namespace) != name {
			return nil, errors.New("invalid live diff change index identity")
		}
		if index.Workspace != workspace {
			continue
		}
		if err := validateChangeIndex(index); err != nil {
			return nil, err
		}
		indexes = append(indexes, index)
	}
	return indexes, nil
}
