package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/threadcontrol/control"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// The application retains refresh timing. Detached source facts belong to the
// presentation panel and navigation belongs to the attached renderer.
type sessionMetricsPanel struct {
	refreshed time.Time
	// Feature use compares the thread displayed at open with its descendants.
	thread       string
	coordination *control.Service
	deduplicate  bool
}

func (session *nativeSession) showSessionMetrics(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker) {
	(*panel) = newStatusReport(attached.presentation, session.replayDebugDirectory)
	(*panel).Workspace = attached.cwd
	(*panel).metrics = &sessionMetricsPanel{thread: attached.presentation.DisplayedThread(), deduplicate: session.deduplicateOutput}
	if session.preparer != nil && session.preparer.ThreadControl != nil {
		(*panel).metrics.coordination = session.preparer.ThreadControl.Coordination()
	}
	(*panel).Metrics = new(transportcapture.MetricsSnapshot)
	session.deleteDraftRange(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), 0, len((*editor).Text))
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
	if m.coordination != nil {
		p.Features = sessionpresentation.NewFeatureMetrics(m.thread, m.coordination, p.Metrics, m.deduplicate)
	}
	// Features carries the joined totals; panel clones and publications skip the per-thread map.
	p.Metrics.OutputDeduplicationThreads = nil
	p.RenderMetrics()
	return true
}
