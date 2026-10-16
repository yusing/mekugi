package router

import (
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

// Adapt the captured host outcome to the change owner's provenance projection.
func authoredChangeHistory(value mekugiHistory) mekugiHistory {
	coverage := ""
	if value.ExecOutcome != nil {
		coverage = value.ExecOutcome.Coverage
	}
	value.ReviewFiles, coverage = changes.AuthoredEvidence(value.ReviewFiles, coverage)
	if value.ExecOutcome != nil && value.ExecOutcome.Coverage != coverage {
		outcome := *value.ExecOutcome
		outcome.Coverage = coverage
		value.ExecOutcome = &outcome
	}
	return value
}
