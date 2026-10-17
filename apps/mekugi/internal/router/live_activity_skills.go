package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

type activeSkillSet = activitypresentation.SkillSet

func (main *appServerUI) openSkills(u *terminalsession.Attachment, view *activityui.View, agent string) {
	names := slices.Clone(main.activitySource(view).ActiveSkills()[agent])
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	main.openBlocks(u, view, []activityui.Block{{Kind: "op", Verb: "Skill", Label: activityui.SkillLabel(names), Body: "- " + strings.Join(names, "\n- ")}})
}
