package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func observeReview(service *sessionreview.Review, workspace, thread string, metadata codexresponses.TurnMetadata) {
	if metadata.ActivityIdentityInvalid || metadata.RequestKind != "turn" {
		return
	}
	service.Observe(workspace, thread, metadata.ParentThreadID, metadata.SubagentKind == "")
}

func beginLiveDiffTurn(service *previewmodel.Session, workspace, thread string, metadata codexresponses.TurnMetadata) {
	if metadata.ActivityIdentityInvalid || metadata.RequestKind != "turn" || metadata.SubagentKind != "" || metadata.ThreadID != "" && metadata.ThreadID != thread {
		return
	}
	service.BeginTurn(workspace, thread, metadata.TurnID)
}
