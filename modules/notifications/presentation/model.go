package presentation

import (
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/notifications/notice"
)

// Model belongs to one attached view. Geometry and display receipts stay with
// the renderer; complete text does not depend on its size.
// Source: internal/router/composer_notice.go:14:29,156:200@b4d36aa2
type Model struct {
	Revision uint64
	Text     string
	Alert    bool
	Until    time.Time
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

// Receive restores complete errors as actionable feedback.
func (m *Model) Receive(d *notice.Delivery, now time.Time) {
	if len(d.Errors.Messages) != 0 {
		texts := make([]string, 0, len(d.Errors.Messages))
		for _, message := range d.Errors.Messages {
			texts = append(texts, message.Text)
		}
		m.Set(strings.Join(texts, "\n\n"), true, now)
	}
}

// Progress belongs to one transcript, shared by every view attached to it. It
// keeps the row each progress notice occupies, so a retried claim from any
// view names the existing row instead of adding another.
type Progress struct{ rows map[string]uint64 }

// Rows returns the claimed messages' rows, adding unseen or retired rows through add.
func (p *Progress) Rows(batch notice.Batch, present func(uint64) bool, add func(notice.Message) uint64) []uint64 {
	var rows []uint64
	for _, message := range batch.Messages {
		row, ok := p.rows[message.ID]
		if !ok || !present(row) {
			if p.rows == nil {
				p.rows = make(map[string]uint64)
			}
			row = add(message)
			p.rows[message.ID] = row
		}
		rows = append(rows, row)
	}
	return rows
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
