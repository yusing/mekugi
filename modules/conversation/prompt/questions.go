// Source: apps/mekugi/internal/router/app_server_questions.go:25:758@[42afca9c] question presentation and lifecycle.
package prompt

import (
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

type Option struct{ Label, Description string }

// Question contains editable non-secret state. Secret editors and answers stay
// with the adapter that owns the authentication request.
type Question struct {
	ID, Header, Question string
	IsSecret             bool
	Choices              []Option
	Editor               composer.State
	Selected             int
	Note, Done, Skipped  bool
	Answer               []string
	Outcome              string
}

type Call struct {
	ID, Thread, Turn, Item string
	Sync, Local            bool
	Questions              []Question
	Sent, Resolved         bool
	SubmissionID           string
}

// Questions belongs to one attached session. Adapters retain request handles by
// Call identity; the model never receives a host client or a live handle.
type Questions struct {
	Calls             []*Call
	Active            *Call
	Index             int
	Confirm, AutoOpen bool
	inputTurns        map[string]bool
}

func (m *Questions) Add(c *Call, replay bool) bool {
	if len(c.Questions) == 0 || slices.ContainsFunc(m.Calls, func(old *Call) bool { return old.ID == c.ID }) {
		return false
	}
	m.InputTurn(c.Turn)
	m.Calls = append(m.Calls, c)
	if !replay {
		m.AutoOpen = true
	}
	return true
}
func (m *Questions) InputTurn(turn string) bool {
	if m.inputTurns == nil {
		m.inputTurns = make(map[string]bool)
	}
	first := !m.inputTurns[turn]
	m.inputTurns[turn] = true
	return first
}
func (m *Questions) Count(thread string) int {
	n := 0
	for _, c := range m.Calls {
		if c.Thread != thread && !c.Local || c.Resolved || c.Sent {
			continue
		}
		for _, q := range c.Questions {
			if q.Outcome == "" {
				n++
			}
		}
	}
	return n
}
func (m *Questions) Waiting(thread string) bool {
	return slices.ContainsFunc(m.Calls, func(c *Call) bool { return c.Thread == thread && (c.Sync || c.Local) && !c.Resolved })
}
func (m *Questions) Next() *Call {
	for _, c := range m.Calls {
		if (c.Sync || c.Local) && !c.Resolved && !c.Sent {
			return c
		}
	}
	for _, c := range m.Calls {
		if !c.Resolved && !c.Sent {
			return c
		}
	}
	return nil
}
func (m *Questions) Open(c *Call) bool {
	if m.Active != nil || c == nil {
		return false
	}
	m.Active, m.Index, m.Confirm = c, 0, false
	for i, q := range c.Questions {
		if q.Outcome == "" {
			m.Index = i
			break
		}
	}
	return true
}
func (m *Questions) Hide() { m.Active = nil; m.Confirm = false }
func (m *Questions) Current() *Question {
	if m.Active == nil {
		return nil
	}
	return &m.Active.Questions[m.Index]
}
func (m *Questions) Move(step int) {
	m.Index = (m.Index + step + len(m.Active.Questions)) % len(m.Active.Questions)
	m.Confirm = false
}
func (m *Questions) ShouldOpen(vacant bool) bool {
	return m.AutoOpen && m.Active == nil && vacant && m.Next() != nil
}
func (m *Questions) Remove(c *Call) {
	m.Calls = slices.DeleteFunc(m.Calls, func(old *Call) bool { return old == c })
}

func (q *Question) Choose(index int) { q.Selected = index; q.Done = false }
func (q *Question) Edit(other bool) {
	if other {
		q.Selected = len(q.Choices)
	}
	q.Done, q.Skipped = false, false
}
func (q *Question) Complete(text string, sync, skip bool) bool {
	q.Skipped, q.Done, q.Answer = skip, true, nil
	if skip {
		return true
	}
	if q.Selected < len(q.Choices) {
		q.Answer = []string{q.Choices[q.Selected].Label}
		if sync && text != "" {
			q.Answer = append(q.Answer, text)
		}
	} else if text != "" {
		q.Answer = []string{text}
	} else {
		q.Done = false
	}
	return q.Done
}
func (c *Call) Gaps() int {
	n := 0
	for _, q := range c.Questions {
		if q.Outcome == "" && (q.Skipped || !q.Done) {
			n++
		}
	}
	return n
}
func (m *Questions) Advance() (step int, submit bool) {
	for j := m.Index + 1; j < len(m.Active.Questions); j++ {
		q := m.Active.Questions[j]
		if !q.Done && q.Outcome == "" {
			return j - m.Index, false
		}
	}
	m.Confirm = m.Active.Gaps() > 0
	return 0, !m.Confirm
}

// Resolve returns drafts to remember in local history. The caller parks the
// active editor first and records returned facts without issuing a host request.
func (c *Call) Resolve(outcome string) []composer.Draft {
	if c.Resolved {
		return nil
	}
	var drafts []composer.Draft
	for i := range c.Questions {
		q := &c.Questions[i]
		if q.Outcome != "" {
			continue
		}
		q.Outcome = outcome
		if c.Sync && c.Sent {
			if q.Skipped || !q.Done {
				q.Outcome = "skipped"
			} else {
				q.Outcome = "answered"
			}
		} else if !q.IsSecret && q.Editor.Snapshot.Text != "" {
			drafts = append(drafts, q.Editor.Snapshot.Draft.Clone())
		}
		q.Editor = composer.State{}
	}
	c.Resolved = true
	return drafts
}
func (c *Call) Reject() bool {
	if c.Resolved || c.Sync {
		return false
	}
	c.Sent = false
	c.SubmissionID = ""
	return true
}
func (c *Call) Commit(index int, clientID, answer string) (composer.Draft, bool) {
	if index < 0 || index >= len(c.Questions) || c.Sync {
		return composer.Draft{}, false
	}
	q := &c.Questions[index]
	if q.Outcome != "" {
		return composer.Draft{}, false
	}
	var discarded composer.Draft
	outcome := "answered elsewhere"
	if c.SubmissionID != "" && (clientID == c.SubmissionID || clientID == "" && answer == strings.Join(q.Answer, "\n")) {
		outcome = "answered"
	} else {
		q.Done = false
		if !q.IsSecret {
			discarded = q.Editor.Snapshot.Draft.Clone()
		}
	}
	q.Outcome = outcome
	if !q.IsSecret {
		q.Answer = []string{answer}
	}
	q.Editor = composer.State{}
	c.Resolved = !slices.ContainsFunc(c.Questions, func(q Question) bool { return q.Outcome == "" })
	return discarded, true
}

// View is detached plain data. It intentionally excludes draft text, secret
// answers, host payloads, and renderer-specific geometry.
type QuestionView struct {
	Selected                int
	Note, Pending           bool
	ID, Header, Text, State string
	Choices                 []Option
	Answer                  []string
	Secret                  bool
}
type CallView struct {
	Sync, Local            bool
	ID, Thread, Turn, Item string
	Questions              []QuestionView
	Sent, Resolved         bool
}

// QuestionDockView excludes parked and active draft contents, including secrets.
type QuestionDockView struct {
	Count, Index, Gaps int
	Confirm            bool
	Call               *CallView
}

func (m *Questions) View(thread string) QuestionDockView {
	v := QuestionDockView{Count: m.Count(thread), Index: m.Index, Confirm: m.Confirm}
	if m.Active != nil {
		call := m.Active.View()
		v.Call, v.Gaps = &call, m.Active.Gaps()
	}
	return v
}

func (c *Call) View() CallView {
	v := CallView{Sync: c.Sync, Local: c.Local, ID: c.ID, Thread: c.Thread, Turn: c.Turn, Item: c.Item, Sent: c.Sent, Resolved: c.Resolved}
	for i, q := range c.Questions {
		state := q.Outcome
		if state == "" {
			switch {
			case c.Sent:
				state = "sending"
			case c.Sync || c.Local:
				state = "waiting"
			default:
				state = "open"
			}
		}
		r := QuestionView{Selected: q.Selected, Note: q.Note, Pending: q.Outcome == "", ID: c.ID + "/" + strconv.Itoa(i), Header: q.Header, Text: q.Question, State: state, Choices: slices.Clone(q.Choices), Secret: q.IsSecret}
		if !q.IsSecret && q.Outcome != "" {
			r.Answer = slices.Clone(q.Answer)
			if c.Sync && len(r.Answer) == 1 && q.Selected == len(q.Choices) {
				r.Answer[0] = "Other:\n\n" + r.Answer[0]
			}
		}
		v.Questions = append(v.Questions, r)
	}
	return v
}

// Submitted distinguishes local answer admission from host commitment.
func (c *Call) Submitted() {
	c.Sent = true
	if c.Sync {
		for i := range c.Questions {
			c.Questions[i].Editor = composer.State{}
		}
		return
	}
	c.Resolved = true
	for i := range c.Questions {
		q := &c.Questions[i]
		if q.Outcome != "" {
			continue
		}
		if q.Skipped || !q.Done {
			q.Outcome = "skipped"
			q.Editor = composer.State{}
		} else {
			c.Resolved = false
		}
	}
}
