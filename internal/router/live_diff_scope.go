package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

var errLiveDiffSessionEnded = errors.New("live diff session ended")

// Session scopes select durable thread streams, never timestamps or router cache IDs.
// Old workspace history and other concurrent sessions are excluded before projection.
func (s *mekugiReplayStore) liveDiffIndexes(workspace, sessionFile string) (map[string]changeIndex, error) {
	workspaces := map[string]map[string]bool{workspace: nil}
	if sessionFile != "" {
		file, err := os.Open(sessionFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errLiveDiffSessionEnded
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(file, maxLiveDiffScopeBytes+1))
		file.Close()
		if err != nil {
			return nil, err
		}
		var scope liveDiffScope
		if len(data) > maxLiveDiffScopeBytes || json.Unmarshal(data, &scope) != nil || scope.Workspaces == nil {
			return nil, errors.New("invalid live diff session scope")
		}
		workspaces = scope.Workspaces
	}
	indexes := make(map[string]changeIndex, len(workspaces))
	for path, threads := range workspaces {
		if !filepath.IsAbs(path) {
			return nil, errors.New("live diff workspace must be absolute")
		}
		index, err := s.readChangeIndex(path)
		if err != nil {
			return nil, err
		}
		if sessionFile != "" {
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
