package sessionpresentation

import (
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

// Source: apps/mekugi/internal/router/exec_track.go:35:49@45fbe279
// ActivitySegment is one segment as the Activity shows it.
type ActivitySegment struct {
	Timing  segmenttransport.Timing          `json:"-"`
	Text    string                           `json:"-"` // Display operations for the segment's source.
	Running bool                             `json:"-"`
	Skipped bool                             `json:"-"` // The list short-circuited before it.
	Exit    int                              `json:"-"`
	Tail    []string                         `json:"-"`
	Omit    int                              `json:"-"`
	Changes []activitypresentation.ChangeRow `json:"-"`
	Output  *activitymodel.Output            `json:"-"`
	Source  string                           `json:"-"`
	Raw     string                           `json:"-"` // A finished VCS segment's complete output, for its change rows.
	Commit  activitypresentation.CommitKey   `json:"-"` // The commit object whose files complete changes.
}
