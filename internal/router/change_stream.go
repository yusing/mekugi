package router

import (
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// allocateChangeStream runs under store.lock. The namespace-wide high-water
// mark outlives payload retention, so another workspace cannot reuse a base ID.
func (s *mekugiReplayStore) allocateChangeStream() (string, error) {
	scope, _, err := s.readHandleScope(s.handleNamespace())
	if err != nil {
		return "", err
	}
	if scope.Namespace != s.handleNamespace() {
		return "", errors.New("change stream allocation requires the root namespace")
	}
	if scope.NextChangeStream == 0 {
		// Older indexes assigned names by position. Preserve their IDs and
		// reserve every existing name before allocating a new workspace/agent.
		entries, err := os.ReadDir(s.directory)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), changeIndexPrefix) {
				continue
			}
			data, err := readManagedOutputFile(filepath.Join(s.directory, entry.Name()))
			if err != nil {
				return "", err
			}
			// Foreign payload schemas cannot block this namespace's allocation.
			var identity struct{ Namespace string }
			if err := json.Unmarshal(data, &identity); err != nil {
				return "", err
			}
			if identity.Namespace != scope.Namespace {
				continue
			}
			var index changeIndex
			if err := json.Unmarshal(data, &index); err != nil {
				return "", err
			}
			if index.Version != 1 || changeIndexName(index.Workspace, index.Namespace) != entry.Name() {
				return "", errors.New("invalid change index during stream allocation")
			}
			if err := validateChangeIndex(index); err != nil {
				return "", err
			}
			for position := range index.Streams {
				number, err := changeStreamOrdinal(index.streamName(position))
				if err != nil {
					return "", err
				}
				scope.NextChangeStream = max(scope.NextChangeStream, number+1)
			}
		}
	}
	if scope.NextChangeStream == int(^uint(0)>>1) {
		return "", errors.New("change stream counter exhausted")
	}
	name := changeStreamName(scope.NextChangeStream)
	scope.NextChangeStream++
	if err := s.writeHandleScope(scope); err != nil {
		return "", err
	}
	return name, nil
}

func changeStreamOrdinal(name string) (int, error) {
	if name == "" {
		return 0, errors.New("empty change stream name")
	}
	number := 0
	for _, letter := range name {
		digit := int(letter-'a') + 1
		if digit < 1 || digit > 26 || number > (int(^uint(0)>>1)-digit)/26 {
			return 0, errors.New("invalid change stream name")
		}
		number = number*26 + digit
	}
	return number - 1, nil
}
