package presentation

import (
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/notifications/notice"
)

// Model belongs to one attached view. Geometry and display receipts stay with
// the renderer; complete text and progress identity do not depend on its size.
// Source: internal/router/composer_notice.go:14:29,156:200@b4d36aa2
type Model struct {
	Revision uint64
	Text     string
	Alert    bool
	Until    time.Time
	progress map[string]bool
}

func (m *Model) Set(text string, alert bool, now time.Time) {
	m.Revision++
	m.Text, m.Alert, m.Until = text, alert, time.Time{}
	if text != "" && !alert {
		m.Until = now.Add(3 * time.Second)
	}
}

func (m *Model) Expire(now time.Time) bool {
	if m.Until.IsZero() || now.Before(m.Until) {
		return false
	}
	m.Set("", false, now)
	return true
}

// Receive restores complete errors and returns only newly observed progress.
func (m *Model) Receive(d *notice.Delivery, now time.Time) []notice.Message {
	if len(d.Errors.Messages) != 0 {
		texts := make([]string, 0, len(d.Errors.Messages))
		for _, message := range d.Errors.Messages {
			texts = append(texts, message.Text)
		}
		m.Set(strings.Join(texts, "\n\n"), true, now)
	}
	var progress []notice.Message
	for _, message := range d.Progress.Messages {
		if m.progress[message.ID] {
			continue
		}
		if m.progress == nil {
			m.progress = make(map[string]bool)
		}
		m.progress[message.ID] = true
		progress = append(progress, message)
	}
	return progress
}

// DesktopPolicy consumes normalized settings. A nil event list allows every
// eligible event; an empty nonnil list allows none. The renderer owns readiness,
// focus observation, generic message text, output capability and signal encoding.
type DesktopPolicy struct {
	Enabled bool
	Always  bool
	Events  []string
}

func (p DesktopPolicy) Allows(event string, focused bool) bool {
	return p.Enabled && (!focused || p.Always) && (p.Events == nil || slices.Contains(p.Events, event))
}
