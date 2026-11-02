package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// The application retains refresh timing. Detached source facts belong to the
// presentation panel and navigation belongs to the attached renderer.
type sessionMetricsPanel struct {
	refreshed time.Time
}

func (session *nativeSession) showSessionMetrics(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker) {
	(*panel) = newStatusReport(attached.presentation, session.replayDebugDirectory)
	(*panel).Workspace = attached.cwd
	(*panel).metrics = new(sessionMetricsPanel)
	(*panel).Metrics = new(transportcapture.MetricsSnapshot)
	session.deleteDraftRange(attached, terminal, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), 0, len((*editor).Text))
	refreshSessionMetrics((*panel), session.sessionCapture, presentationTime(clock), true)
}
func refreshSessionMetrics(p *appServerStatusReport, capture *transportcapture.Recorder, at time.Time, force bool) bool {
	if p == nil || p.metrics == nil {
		return false
	}
	m := p.metrics
	if !force && at.Sub(m.refreshed) < time.Second {
		return false
	}
	m.refreshed = at
	if capture != nil {
		*p.Metrics = capture.Snapshot()
	}
	p.RenderMetrics()
	return true
}
