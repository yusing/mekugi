package sessionpresentation

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/contracts/presentation"
)

// New validates the selected view contributions before any module starts.
func New(status []presentation.Status) (*Service, error) {
	s := new(Service)
	seen := make(map[string]bool)
	for _, contribution := range status {
		if contribution.ID == "" || contribution.Text == nil || len(contribution.Placements) == 0 {
			return nil, fmt.Errorf("presentation %q requires an identity, projection and placement", contribution.ID)
		}
		if seen[contribution.ID] {
			return nil, fmt.Errorf("duplicate presentation %q", contribution.ID)
		}
		seen[contribution.ID] = true
		for i, placement := range contribution.Placements {
			if placement != presentation.ComposerCaption && placement != presentation.AgentMetric {
				return nil, fmt.Errorf("presentation %q: unsupported placement %q", contribution.ID, placement)
			}
			if slices.Contains(contribution.Placements[:i], placement) {
				return nil, fmt.Errorf("presentation %q: duplicate placement %q", contribution.ID, placement)
			}
		}
		contribution.Placements = slices.Clone(contribution.Placements)
		s.status = append(s.status, contribution)
	}
	return s, nil
}

// StatusItems reads the selected thread's admitted facts without retaining them.
func (m *Model) StatusItems(thread presentation.Thread, placement presentation.Placement) []presentation.Item {
	var items []presentation.Item
	for _, contribution := range m.status {
		if slices.Contains(contribution.Placements, placement) {
			if text := contribution.Text(thread); text != "" {
				items = append(items, presentation.Item{ID: contribution.ID, Thread: thread.ID, Text: text})
			}
		}
	}
	return items
}
