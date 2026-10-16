package router

import (
	"fmt"
	"slices"
	"strings"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

type activeSkillSet = activitypresentation.SkillSet

func skillLabel(names activeSkillSet) string {
	if len(names) == 1 {
		return "1 loaded skill"
	}
	return fmt.Sprintf("%d loaded skills", len(names))
}
func contextBoundary(entry activityPaneEntry) bool {
	return entry.Kind == "progress" && entry.Text == "Context compacted"
}

func (u *terminalUI) openSkills(view *liveActivityView, agent string) {
	names := slices.Clone(view.ActiveSkills()[agent])
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	u.openBlocks(view, []activityui.Block{{Kind: "op", Verb: "Skill", Label: skillLabel(names), Body: "- " + strings.Join(names, "\n- ")}})
}
