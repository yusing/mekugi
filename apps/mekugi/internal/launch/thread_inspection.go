package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func threadInspection(session *sessionpresentation.Attachment, thread string, applied preferences.Applied, tiers *inferencegateway.Tiers) control.SessionDetails {
	details := control.SessionDetails{SessionMetrics: session.InspectionMetrics(thread)}
	if applied.Model != "" {
		details.Model = new(applied.Model)
		details.ReasoningEffort = new(applied.Effort)
		details.ServiceTier = new(tiers.Effective(applied.Model, applied.Tier, thread))
	}
	return details
}

func restoreCompactionMetrics(session *sessionpresentation.Attachment, info binding.ThreadInfo) {
	var completed []string
	for _, turn := range info.Turns {
		for _, item := range turn.Items {
			if item.Type == "contextCompaction" && item.Status != "inProgress" && item.Status != "failed" && item.Status != "interrupted" {
				completed = append(completed, item.ID)
			}
		}
	}
	session.RestoreCompactions(info.ID, completed)
}
