package router

import previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

func observeReview(service *savedReview, workspace, thread string, metadata codexTurnMetadata) {
	if metadata.activityIdentityInvalid || metadata.RequestKind != "turn" {
		return
	}
	service.Observe(workspace, thread, metadata.ParentThreadID, metadata.SubagentKind == "")
}

func beginLiveDiffTurn(service *previewmodel.Session, workspace, thread string, metadata codexTurnMetadata) {
	if metadata.activityIdentityInvalid || metadata.RequestKind != "turn" || metadata.SubagentKind != "" || metadata.ThreadID != "" && metadata.ThreadID != thread {
		return
	}
	service.BeginTurn(workspace, thread, metadata.TurnID)
}
