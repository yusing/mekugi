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
	u.statusPanel = u.newStatusReport()
	u.statusPanel.metrics = new(sessionMetricsPanel)
	u.statusPanel.Metrics = new(transportcapture.MetricsSnapshot)
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
		*p.Metrics = u.sessionCapture.Snapshot()
	}
	u.dirty = true
}
