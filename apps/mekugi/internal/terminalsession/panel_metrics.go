package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"slices"
)

var metricViews = []string{"Overview", "Transport", "Exchanges"}

// Source: apps/mekugi/internal/router/app_server_session_metrics.go:97:299@630ce384.
func (p *StatusPanel) renderMetrics() {
	p.Title = "Session · " + metricViews[p.Tab]
	p.Controls = "←→ views · ↑↓ scroll · r refresh · Esc close"
	if p.Tab == 2 {
		p.Controls = "←→ views · [ older / ] newer · ↑↓ scroll · Esc close"
	}
	p.Fields, p.Groups = nil, nil
	p.Fields = sessionpresentation.MetricFields(p.body, p.Tab, p.exchangeIndex())
	for i := range p.Fields {
		field := &p.Fields[i]
		if field.Group == "Exchange" && field.Label == "Retained" {
			field.Value += " · [ older / ] newer"
		}
		if !slices.Contains(p.Groups, field.Group) {
			p.Groups = append(p.Groups, field.Group)
		}
	}
}
