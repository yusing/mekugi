package router

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
)

// Session scopes select durable thread streams, never timestamps or router cache IDs.
// Old workspace history and other concurrent sessions are excluded before projection.
func (s *mekugiReplayStore) liveDiffScopeIndexes(scope liveDiffScope) (map[string]changeIndex, error) {
	indexes := make(map[string]changeIndex, len(scope.Workspaces))
	for path, threads := range scope.Workspaces {
		if !filepath.IsAbs(path) {
			return nil, errors.New("live diff workspace must be absolute")
		}
		index, err := s.readChangeIndex(path)
		if err != nil {
			return nil, err
		}
		if threads != nil {
			for stream, info := range index.Streams {
				if threads[info.Thread] {
					continue
				}
				for n := 1; n <= info.Next; n++ {
					delete(index.Changes, "hp_"+changeStreamName(stream)+strconv.Itoa(n))
				}
				index.Streams[stream].Next = 0
			}
		}
		indexes[path] = index
	}
	return indexes, nil
}

func (s *mekugiReplayStore) liveDiffSnapshotFiles(ctx context.Context, indexes map[string]changeIndex) ([]liveDiffFile, error) {
	ordered := make([]changeIndex, 0, len(indexes))
	for _, workspace := range slices.Sorted(maps.Keys(indexes)) {
		ordered = append(ordered, indexes[workspace])
	}
	// File identity belongs to the edited path, not its routing workspace.
	return s.liveDiffFilesFromIndexes(ctx, ordered)
}
