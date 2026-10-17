package sessionpresentation

import (
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// Source: apps/mekugi/internal/terminalsession/draft.go:16:33@7e374dcb.
// DraftSpans supplies the same atomic token annotations for editing and display.
func DraftSpans(d composer.Draft) []activitypresentation.TextSpan {
	var spans []activitypresentation.TextSpan
	for _, span := range d.Spans() {
		kind := activitypresentation.ImageToken
		switch span.Kind {
		case composer.File:
			kind = activitypresentation.FileToken
		case composer.Skill:
			kind = activitypresentation.SkillToken
		case composer.Selection:
			kind = activitypresentation.SelectionToken
		}
		spans = append(spans, activitypresentation.TextSpan{Start: span.Start, End: span.End, Kind: kind})
	}
	return spans
}
