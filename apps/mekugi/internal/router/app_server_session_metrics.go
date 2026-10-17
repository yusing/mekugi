package router

import (
	"time"

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
	u.deleteDraftRange(0, len(u.Text))
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
