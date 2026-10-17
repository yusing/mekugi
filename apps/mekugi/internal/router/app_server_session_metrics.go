package router

import (
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// The application retains refresh timing. Detached source facts belong to the
// presentation panel and navigation belongs to the attached renderer.
type sessionMetricsPanel struct {
	refreshed time.Time
}

func (u *appServerUI) showSessionMetrics() {
	u.statusPanel = newStatusReport(u.attached.presentation, u.replayDebugDirectory)
	u.statusPanel.metrics = new(sessionMetricsPanel)
	u.statusPanel.Metrics = new(transportcapture.MetricsSnapshot)
	deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))
	u.dirty = refreshSessionMetrics(u.statusPanel, u.sessionCapture, presentationTime(u.clock), true) || u.dirty
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
