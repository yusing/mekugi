package router

import (
	"slices"
	"strings"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

type activeSkillSet = activitypresentation.SkillSet

func (u *terminalUI) openSkills(view *activityAdapter, agent string) {
	names := slices.Clone(view.ActiveSkills()[agent])
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	u.openBlocks(view, []activityui.Block{{Kind: "op", Verb: "Skill", Label: activityui.SkillLabel(names), Body: "- " + strings.Join(names, "\n- ")}})
}
