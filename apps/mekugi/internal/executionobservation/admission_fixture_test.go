package executionobservation

import (
	"errors"
	"os"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func (s *mekugiReplayStore) testFileSizes() (map[string]int64, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, err
	}
	files := make(map[string]int64)
	for _, entry := range entries {
		_, managed, err := testHistory().Kind(entry.Name())
		if err != nil {
			return nil, err
		}
		if !managed && !history.CatalogName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("non-regular managed record")
		}
		files[entry.Name()] = info.Size()
	}
	return files, nil
}
