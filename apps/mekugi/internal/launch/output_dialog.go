package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func entryDetailPages(source *sessionpresentation.ActivitySource, seq uint64) []activitypresentation.DetailPage {
	for index, entry := range source.Entries {
		if entry.Source.Seq != seq {
			continue
		}
		blocks := source.SourceValues(index)
		for i := range blocks {
			blocks[i].Source = entry.Source.Seq
			if blocks[i].Verb == "" {
				blocks[i].Verb = entry.Source.Agent
			}
		}
		return activitypresentation.DetailPages(blocks)
	}
	return nil
}
