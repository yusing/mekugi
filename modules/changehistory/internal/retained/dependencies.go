package retained

import "github.com/yusing/mekugi/libraries/legacycall"

func (s *store) dependencies(workspace string, ids []string, existingOnly bool) ([]string, error) {
	index, err := s.readChangeIndex(workspace)
	if err != nil {
		return nil, err
	}
	return changeDependencies(index, ids, existingOnly)
}

func changeDependencies(index changeIndex, ids []string, existingOnly bool) ([]string, error) {
	var names []string
	if !existingOnly {
		names = append(names, changeIndexName(index.Workspace, index.Namespace))
	}
	for _, id := range ids {
		change, exists := index.Changes[id]
		if !exists {
			if existingOnly {
				continue
			}
			return nil, missingChangeError(index, id)
		}
		if existingOnly && len(names) == 0 {
			names = append(names, changeIndexName(index.Workspace, index.Namespace))
		}
		for _, id := range change.Dependencies {
			names = append(names, legacycall.Name(index.Workspace, id, false))
		}
		for _, call := range change.Calls {
			names = append(names, legacycall.Name(index.Workspace, call.ID, false))
		}
	}
	return names, nil
}
