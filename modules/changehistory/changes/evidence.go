package changes

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
)

// Source: internal/router/authored_changes.go@[1a1981ba] retained provenance.
// AuthoredReview uses captured provenance, never path resemblance or ignore rules.
func AuthoredReview(file diffmodel.ReviewFile) bool {
	return file.Origin == "" && file.OriginNote != "observed during command window"
}

// GitDiagnosticReview identifies retained Git provenance without reclassifying
// a command or treating its imported/discarded content as authored changes.
func GitDiagnosticReview(file diffmodel.ReviewFile) bool {
	return file.Origin == "git" || strings.HasPrefix(file.Origin, "git ")
}

// AuthoredReviews omits observer/managed evidence and recomputes its coverage.
// Unfiltered input retains its existing coverage and slice.
// Filtering needs no text reads or whole-batch materialization.
func AuthoredReviews(files []evidence.Review, coverage string) ([]evidence.Review, string) {
	if !slices.ContainsFunc(files, func(file evidence.Review) bool { return !AuthoredReview(file.ReviewFile) }) {
		return files, coverage
	}
	files = slices.DeleteFunc(slices.Clone(files), func(file evidence.Review) bool { return !AuthoredReview(file.ReviewFile) })
	coverage = "exact"
	if slices.ContainsFunc(files, func(file evidence.Review) bool { return file.Incomplete != "" }) {
		coverage = "partial"
	}
	return files, coverage
}

// Source: modules/changehistory/capture/command.go:36:39@6f68e18a.
// SharedOriginSuffix follows the managed tools whose scope also held a direct
// path. One snapshot pair spans the whole command, so the note says the tool
// ran, not that it changed the file.
const SharedOriginSuffix = " also ran"

// Source: modules/changehistory/capture/command.go:815:817@6f68e18a.
// PathWithin compares captured paths without reading the workspace.
func PathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
