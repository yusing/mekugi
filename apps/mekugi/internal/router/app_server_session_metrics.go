package router

import (
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"time"
)

// The application retains refresh timing and detached source facts. Terminal
// navigation belongs to the attached renderer.
type sessionMetricsPanel struct {
	snapshot  transportcapture.MetricsSnapshot
	refreshed time.Time
}

func (u *appServerUI) showSessionMetrics() {
	u.statusPanel = u.newStatusReport()
	u.statusPanel.metrics = new(sessionMetricsPanel)
	u.deleteDraftRange(0, len(u.Text))
	u.refreshSessionMetrics(true)
}
func (u *appServerUI) refreshSessionMetrics(force bool) {
	p := u.statusPanel
	if p == nil || p.metrics == nil {
		return
	}
	m := p.metrics
	if !force && u.now().Sub(m.refreshed) < time.Second {
		return
	}
	m.refreshed = u.now()
	if u.sessionCapture != nil {
		m.snapshot = u.sessionCapture.Snapshot()
	}
	u.dirty = true
}
