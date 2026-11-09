package launch

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (files *detailFiles) openSkills(attached *attachedPresentation, source *sessionpresentation.ActivitySource, agent string) {
	names := slices.Clone(source.ActiveSkills()[agent])
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	pages := activitypresentation.DetailPages([]activitypresentation.Value{{Kind: "op", Verb: "Skill", Label: activityui.SkillLabel(names), Body: "- " + strings.Join(names, "\n- ")}})
	files.openDetailSelection(&attached.session, attached.presentation.View().Version, source, pages, false)
}
