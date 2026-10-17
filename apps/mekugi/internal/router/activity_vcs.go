package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/contracts/observation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// commandOutputRows adapts observed change-history or VCS output, retaining the
// immutable commit key whose files can complete the output-observed rows.
func commandOutputRows(item observation.Item) ([]activitypresentation.ChangeRow, activitypresentation.CommitKey) {
	if rows := mchangesOutputRows(item); rows != nil || item.AggregatedOutput == nil {
		return rows, activitypresentation.CommitKey{}
	}
	return activitypresentation.VCSOutputRows(frontends.ShellDisplaySource(sessionpresentation.DisplayCommand(item.Command)), item.Cwd, *item.AggregatedOutput)
}

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
func (u *appServerUI) startCommitReads() {
	for _, view := range []*activityAdapter{u.view, u.agents} {
		if view != nil && view.RefreshCommits(u.commits) {
			u.dirty = true
		}
	}
}
