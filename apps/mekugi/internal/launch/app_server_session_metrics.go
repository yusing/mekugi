package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// The application retains refresh timing. Detached source facts belong to the
// presentation panel and navigation belongs to the attached renderer.
type sessionMetricsPanel struct {
	refreshed time.Time
}

func showSessionMetrics(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, capture *transportcapture.Recorder, debugDirectory string) {
	(*panel) = newStatusReport(attached.presentation, debugDirectory)
	(*panel).metrics = new(sessionMetricsPanel)
	(*panel).Metrics = new(transportcapture.MetricsSnapshot)
	deleteDraftRange(attached, terminal, control, picker, editor, settings, tiers, title, backend, (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), 0, len((*editor).Text))
	refreshSessionMetrics((*panel), capture, presentationTime(clock), true)
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
	return true
}
