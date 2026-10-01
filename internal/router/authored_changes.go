package router

import (
	"slices"

	"github.com/yusing/mekugi"
)

// Provenance, not paths or ignore rules, determines whether retained evidence
// describes an agent edit. Old observer/managed records remain in --history.
func authoredReview(file mekugi.ReviewFile) bool {
	return file.Origin == "" && file.OriginNote != execInventoryNote
}

func authoredChangeHistory(history mekugiHistory) mekugiHistory {
	filtered := slices.ContainsFunc(history.ReviewFiles, func(file mekugi.ReviewFile) bool { return !authoredReview(file) })
	if !filtered {
		return history
	}
	history.ReviewFiles = slices.DeleteFunc(slices.Clone(history.ReviewFiles), func(file mekugi.ReviewFile) bool { return !authoredReview(file) })
	if history.ExecOutcome != nil {
		outcome := *history.ExecOutcome
		outcome.Coverage = execCoverageExact
		if slices.ContainsFunc(history.ReviewFiles, func(file mekugi.ReviewFile) bool { return file.Incomplete != "" }) {
			outcome.Coverage = execCoveragePartial
		}
		history.ExecOutcome = &outcome
	}
	return history
}
