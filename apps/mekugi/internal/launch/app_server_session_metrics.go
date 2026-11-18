package launch

import (
	"errors"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/pathdisplay"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// The application retains refresh timing. Detached source facts belong to the
// presentation panel and navigation belongs to the attached renderer.
type sessionMetricsPanel struct {
	refreshed time.Time
	// Feature use compares the thread displayed at open with its descendants.
	thread     string
	connection *nativeConnection
	name       string

	pending  <-chan sessionpresentation.FeatureSnapshot
	features sessionpresentation.FeatureSnapshot
}

func (session *nativeSession) showSessionMetrics(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker) {
	(*panel) = newStatusReport(attached.presentation, session.replayDebugDirectory)
	(*panel).Workspace = attached.cwd
	(*panel).metrics = &sessionMetricsPanel{thread: attached.presentation.DisplayedThread(), features: sessionpresentation.FeatureSnapshot{Err: errors.New("native feature measurement is pending")}}
	(*panel).metrics.connection = session.connection
	(*panel).Metrics = new(transportcapture.MetricsSnapshot)
	session.deleteDraftRange(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), 0, len((*editor).Text))
	refreshSessionMetrics((*panel), session.sessionCapture, presentationTime(clock), true)
}
func refreshSessionMetrics(p *appServerStatusReport, capture *transportcapture.Recorder, at time.Time, force bool) bool {
	if p == nil || p.metrics == nil {
		return false
	}
	m := p.metrics
	received := false
	if m.pending != nil {
		select {
		case m.features = <-m.pending:
			m.pending, received = nil, true
		default:
		}
	}
	refresh := force || at.Sub(m.refreshed) >= time.Second
	if !refresh && !received {
		return false
	}
	if refresh {
		m.refreshed = at
		if capture != nil {
			*p.Metrics = capture.Snapshot()
		}
		if force && p.MetricTab == sessionpresentation.MetricFeatures && m.pending == nil && m.connection != nil {
			c := m.connection
			var descendants []string
			if self := c.agent(m.thread); self != nil {
				m.name = pathdisplay.AgentDisplayName(self.AgentPath())
				if self.ParentThreadID == "" {
					m.name = "Main"
				}
				if self.AgentPath() != "" {
					root, _ := c.family(m.thread)
					for _, agent := range c.families[root].threads {
						if agent.AgentPath() != "" && strings.HasPrefix(agent.AgentPath(), self.AgentPath()+"/") {
							descendants = append(descendants, agent.ID)
						}
					}
				}
			}
			if m.features.Err != nil {
				m.features.Threads = append([]string(nil), descendants...)
			}
			result := make(chan sessionpresentation.FeatureSnapshot, 1)
			m.pending = result
			thread := m.thread
			c.workers.Add(1)
			go func() {
				defer c.workers.Done()
				result <- sessionpresentation.ReadNativeFeatureUse(c.root.ctx, thread, descendants, c.threads.features)
			}()
		}
	}
	p.Features = nil
	if m.connection != nil {
		p.Features = sessionpresentation.NewFeatureMetrics(m.name, m.features)
	}
	p.RenderMetrics()
	return true
}
