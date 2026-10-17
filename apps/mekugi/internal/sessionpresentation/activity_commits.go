package sessionpresentation

import (
	"slices"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Source: apps/mekugi/internal/router/activity_vcs.go:39:106@45fbe279
// RefreshCommits projects completed immutable reads into this feed without
// blocking or maintaining a second acquisition/cache owner.
func (v *ActivitySource) RefreshCommits(commits *activitypresentation.CommitReader) bool {
	if commits == nil {
		return false
	}
	changed := false

	for i, record := range v.Entries {
		native := record.Source.Native
		if native == nil {
			continue
		}
		var keys []activitypresentation.CommitKey
		if native.Commit.Hash != "" {
			keys = append(keys, native.Commit)
		}
		for _, segment := range native.Segments {
			if segment.Commit.Hash != "" {
				keys = append(keys, segment.Commit)
			}
		}
		for _, key := range keys {
			files, ready := commits.Request(key)
			if !ready {
				continue
			}
			v.Model.Mutate(i, func(record *ActivityRecord) {
				// Source records and published rows can outlive this revision.
				updated := *record.Source.Native
				if updated.Commit == key {
					updated.Changes = activitypresentation.CommitChanges(updated.Changes, key, files)
					updated.Commit = activitypresentation.CommitKey{}
				}
				updated.Segments = slices.Clone(updated.Segments)
				for j := range updated.Segments {
					segment := &updated.Segments[j]
					if segment.Commit == key {
						segment.Changes = activitypresentation.CommitChanges(segment.Changes, key, files)
						segment.Commit = activitypresentation.CommitKey{}
					}
				}
				record.Source.Native = &updated
				complete := func(value *activitypresentation.Value) {
					observed := native.Commit
					if segment := value.Operation - 1; value.Segment && value.Output != 1 && segment >= 0 && segment < len(native.Segments) && native.Segments[segment].Commit.Hash != "" {
						observed = native.Segments[segment].Commit
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
			changed = true
		}
	}
	return changed
}
