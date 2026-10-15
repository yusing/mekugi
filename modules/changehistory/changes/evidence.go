package changes

import (
	"slices"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Source: internal/router/authored_changes.go@[1a1981ba] retained provenance.
// AuthoredReview uses captured provenance, never path resemblance or ignore rules.
func AuthoredReview(file diffmodel.ReviewFile) bool {
	return file.Origin == "" && file.OriginNote != "observed during command window"
}

// AuthoredEvidence omits observer/managed evidence and recomputes its coverage.
// Unfiltered input retains its existing coverage and slice.
func AuthoredEvidence(files []diffmodel.ReviewFile, coverage string) ([]diffmodel.ReviewFile, string) {
	if !slices.ContainsFunc(files, func(file diffmodel.ReviewFile) bool { return !AuthoredReview(file) }) {
		return files, coverage
	}
	files = slices.DeleteFunc(slices.Clone(files), func(file diffmodel.ReviewFile) bool { return !AuthoredReview(file) })
	coverage = "exact"
	if slices.ContainsFunc(files, func(file diffmodel.ReviewFile) bool { return file.Incomplete != "" }) {
		coverage = "partial"
	}
	return files, coverage
}
