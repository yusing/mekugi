package capture

import (
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/contracts/inspection"
)

func (s *Service) InspectUsage(ctx context.Context, path string, filter inspection.UsageFilter) (map[string]inspection.Usage, error) {
	usage, err := s.InspectProviderUsage(ctx, path, UsageInspectionFilter(filter))
	if err != nil {
		return nil, err
	}
	result := make(map[string]inspection.Usage, len(usage))
	for id, observed := range usage {
		value := inspection.Usage{State: observed.State, UnknownCompletenessRecords: observed.UnknownCompletenessRecords, ObservedRecords: observed.ObservedRecords, MissingRecords: observed.MissingRecords}
		if observed.Tokens != nil {
			value.Tokens, err = json.Marshal(observed.Tokens, jsonv1.DefaultOptionsV1())
			if err != nil {
				return nil, err
			}
		}
		result[id] = value
	}
	return result, nil
}
