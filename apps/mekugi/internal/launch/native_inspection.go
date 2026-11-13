package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/observation"
)

func (session *nativeSession) observeInspection(inspection *sessioncontrol.Inspection) {
	if session.Client.Requests.DispatchMeasurement(inspection) {
		return
	}
	target := inspection.Target()
	var native *appserver.AgentObservation
	if connection := session.connection; connection != nil && connection.root == session {
		native = connection.agent(target)
	}
	ownedNative := native != nil && native.Loaded
	switch {
	case inspection.ContextWindow() != "" && (target == session.Client.Thread || session.Client.Requests.HasSideThread(target) || ownedNative):
		inspection.CompleteAttachments(target, session.conversationState.Attachments(session.attached.session.Thread(target)))
	case target == session.Client.Thread:
		inspection.Complete(target, threadInspection(&session.attached.session, target, session.settings, session.serviceTiers))
	case session.Client.Requests.HasSideThread(target):
		inspection.Complete(target, observation.SessionDetails{SessionMetrics: session.attached.session.InspectionMetrics(target)})
	case ownedNative:
		inspection.Complete(target, sessioncontrol.NativeAgentDetails(*native))
	default:
		inspection.Complete(session.Client.Thread, observation.SessionDetails{})
	}
}
