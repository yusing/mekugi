package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// A prior unclassified command may change the shell cwd. Do not enrich a later
// commit against the invocation's initial cwd in that case.
// Literal VCS commands change only their own process directory, if any.
func vcsSegmentCwd(cwd string, prior []sessionpresentation.ActivitySegment) string {
	for _, segment := range prior {
		if segment.Skipped {
			continue
		}
		if !activitypresentation.VCSPreservesDirectory(frontends.ShellDisplaySource(segment.Source)) {
			return ""
		}
	}
	return cwd
}

// startCommitReads projects cached immutable files and admits bounded background reads.
// The attached Activity owner coalesces completion notifications; no work blocks input.
func startCommitReads(attached *attachedPresentation, commits *activitypresentation.CommitReader) (changed bool) {
	for _, view := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		if view != nil && view.RefreshCommits(commits) {
			changed = true
		}
	}
	return changed
}
