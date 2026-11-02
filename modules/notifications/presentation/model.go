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
	setAt    time.Time
	errors   []notice.Message
}

func (m *Model) Set(text string, alert bool, now time.Time) {
	m.Revision++
	m.Text, m.Alert, m.Until = text, alert, time.Time{}
	m.setAt, m.errors = now, nil
	if text != "" && !alert {
		m.Until = now.Add(3 * time.Second)
	}
}

// ClearSucceeded preserves unrelated-thread errors and non-error feedback.
func (m *Model) ClearSucceeded(notices *notice.Service, thread string) bool {
	if !m.Alert {
		return false
	}
	m.BindThread(thread)
	before := len(m.errors)
	m.errors = slices.DeleteFunc(m.errors, func(source notice.Message) bool { return notices.SucceededSince(source.Thread, source.Occurred) })
	if len(m.errors) == before {
		return false
	}
	texts := make([]string, 0, len(m.errors))
	for _, source := range m.errors {
		texts = append(texts, source.Text)
	}
	m.Revision++
	m.Text, m.Alert = strings.Join(texts, "\n\n"), len(texts) != 0
	return true
}

func (m *Model) Expire(now time.Time) bool {
	if m.Until.IsZero() || now.Before(m.Until) {
		return false
	}
	m.Set("", false, now)
	return true
}

// BindThread captures a local error's destination before display navigation.
func (m *Model) BindThread(thread string) {
	if !m.Alert || thread == "" {
		return
	}
	if len(m.errors) == 0 {
		m.errors = []notice.Message{{Thread: thread, Text: m.Text, Occurred: m.setAt}}
	}
	for i := range m.errors {
		if m.errors[i].Thread == "" {
			m.errors[i].Thread = thread
		}
	}
}

// Receive restores complete errors as actionable feedback.
func (m *Model) Receive(d *notice.Delivery, now time.Time) {
	if len(d.Errors.Messages) != 0 {
		texts := make([]string, 0, len(d.Errors.Messages))
		for _, message := range d.Errors.Messages {
			texts = append(texts, message.Text)
		}
		m.Set(strings.Join(texts, "\n\n"), true, now)
		m.errors = slices.Clone(d.Errors.Messages)
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
