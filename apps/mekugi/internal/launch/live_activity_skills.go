package launch

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

type activeSkillSet = activitypresentation.SkillSet

func openSkills(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, surface *terminalsession.Attachment, view *activityui.View, agent string) {
	names := slices.Clone(activitySource(attached, terminal, view).ActiveSkills()[agent])
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	openBlocks(attached, terminal, files, surface, view, []activityui.Block{{Kind: "op", Verb: "Skill", Label: activityui.SkillLabel(names), Body: "- " + strings.Join(names, "\n- ")}})
}
