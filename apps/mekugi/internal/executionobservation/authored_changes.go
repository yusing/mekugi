package executionobservation

import (
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

// Adapt the captured host outcome to the change owner's provenance projection.
func authoredChangeHistory(value mekugiHistory) mekugiHistory {
	coverage := ""
	if value.ExecOutcome != nil {
		coverage = value.ExecOutcome.Coverage
	}
	value.reviewFiles, coverage = changes.AuthoredReviews(value.reviews(), coverage)
	value.ReviewFiles = nil
	if value.ExecOutcome != nil && value.ExecOutcome.Coverage != coverage {
		outcome := *value.ExecOutcome
		outcome.Coverage = coverage
		value.ExecOutcome = &outcome
	}
	return value
}
