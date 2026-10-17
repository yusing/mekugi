package router

import (
	"slices"

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
	return activitypresentation.VCSOutputRows(frontends.ShellDisplaySource(appServerDisplayCommand(item.Command)), item.Cwd, *item.AggregatedOutput)
}

// A prior unclassified command may change the shell cwd. Do not enrich a later
// commit against the invocation's initial cwd in that case.
// Literal VCS commands change only their own process directory, if any.
func vcsSegmentCwd(cwd string, prior []commandSegment) string {
	for _, segment := range prior {
		if segment.skipped {
			continue
		}
		if !activitypresentation.VCSPreservesDirectory(frontends.ShellDisplaySource(segment.source)) {
			return ""
		}
	}
	return cwd
}

// startCommitReads projects cached immutable files and admits bounded background reads.
// The attached Activity owner coalesces completion notifications; no work blocks input.
func (u *appServerUI) startCommitReads() {
	if u.commits == nil {
		return
	}
	for _, view := range []*activityAdapter{u.view, u.agents} {
		if view == nil {
			continue
		}
		for i, record := range view.Entries {
			native := record.Source.native
			if native == nil {
				continue
			}
			var keys []activitypresentation.CommitKey
			if native.commit.Hash != "" {
				keys = append(keys, native.commit)
			}
			for _, segment := range native.segments {
				if segment.commit.Hash != "" {
					keys = append(keys, segment.commit)
				}
			}
			for _, key := range keys {
				files, ready := u.commits.Request(key)
				if !ready {
					continue
				}
				view.Model.Mutate(i, func(record *liveActivityRecord) {
					// Source records and published rows can outlive this revision.
					updated := *record.Source.native
					if updated.commit == key {
						updated.changes = activitypresentation.CommitChanges(updated.changes, key, files)
						updated.commit = activitypresentation.CommitKey{}
					}
					updated.segments = slices.Clone(updated.segments)
					for j := range updated.segments {
						segment := &updated.segments[j]
						if segment.commit == key {
							segment.changes = activitypresentation.CommitChanges(segment.changes, key, files)
							segment.commit = activitypresentation.CommitKey{}
						}
					}
					record.Source.native = &updated
					complete := func(value *activitypresentation.Value) {
						observed := native.commit
						if segment := value.Operation - 1; value.Segment && value.Output != 1 && segment >= 0 && segment < len(native.segments) && native.segments[segment].commit.Hash != "" {
							observed = native.segments[segment].commit
						}
						// An abbreviated hash can name different commits in different
						// repositories. Match the owning operation's full key first.
						if observed == key {
							value.Changes = activitypresentation.CommitChanges(value.Changes, key, files)
						}
					}
					for j := range record.Values {
						value := &record.Values[j]
						complete(value)
						value.Members = slices.Clone(value.Members)
						for k := range value.Members {
							complete(&value.Members[k])
						}
					}
				})
				u.dirty = true
			}
		}
	}
}
