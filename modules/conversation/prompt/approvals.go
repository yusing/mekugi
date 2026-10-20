// Source: apps/mekugi/internal/router/app_server_approvals.go:27:72,519:536,653:740@[42afca9c] approval presentation.
package prompt

import (
	"slices"
	"strconv"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

type Choice struct {
	ID                     int
	Label                  string
	Approve, Session, Deny bool
	Outcome                string
}
type Approval struct {
	ID, Thread, Turn, Item string
	Title, Subject         string
	Details                []string
	Choices                []Choice
	Selected               int
	Editor                 composer.State
	Outcome                string
}

func (a *Approval) DenialChoice() int {
	return slices.IndexFunc(a.Choices, func(c Choice) bool { return c.Deny })
}

type Approvals struct {
	Pending        []*Approval
	Open, AutoOpen bool
	nextLocal      uint64
}

func (m *Approvals) Add(a *Approval) {
	if a.ID == "" {
		m.nextLocal++
		a.ID = "local/" + strconv.FormatUint(m.nextLocal, 10)
	}
	m.Pending = append(m.Pending, a)
	m.AutoOpen = true
}

// Decision resolves only offered choices on a still-pending request. Host
// response payloads and local command grants remain with the application.
func (m *Approvals) Decision(id string, choice int) (*Approval, Choice, bool) {
	for _, a := range m.Pending {
		if a.ID == id {
			for _, c := range a.Choices {
				if c.ID == choice {
					return a, c, true
				}
			}
			break
		}
	}
	return nil, Choice{}, false
}
func (m *Approvals) Remove(a *Approval) bool {
	i := slices.Index(m.Pending, a)
	if i < 0 {
		return false
	}
	m.Pending = slices.Delete(m.Pending, i, i+1)
	return true
}
func (m *Approvals) ShouldOpen(vacant, questionOpen bool) bool {
	return m.AutoOpen && !m.Open && len(m.Pending) > 0 && !questionOpen && vacant
}
func (m *Approvals) Show() bool {
	if len(m.Pending) == 0 {
		return false
	}
	m.Open = true
	return true
}
func (m *Approvals) Hide()        { m.Open = false }
func (a *Approval) Move(step int) { a.Selected = (a.Selected + step + len(a.Choices)) % len(a.Choices) }

type ApprovalView struct {
	Selected                                        int
	ID, Thread, Turn, Item, Title, Subject, Outcome string
	Details                                         []string
	Choices                                         []Choice
}

func (a *Approval) View() ApprovalView {
	return ApprovalView{Selected: a.Selected, ID: a.ID, Thread: a.Thread, Turn: a.Turn, Item: a.Item, Title: a.Title, Subject: a.Subject, Outcome: a.Outcome, Details: slices.Clone(a.Details), Choices: slices.Clone(a.Choices)}
}

type ApprovalDockView struct {
	Count        int
	Open, Reason bool
	Approval     ApprovalView
}

func (m *Approvals) View() ApprovalDockView {
	v := ApprovalDockView{Count: len(m.Pending), Open: m.Open}
	if len(m.Pending) > 0 {
		v.Approval = m.Pending[0].View()
		v.Reason = m.Pending[0].DenialChoice() >= 0
	}
	return v
}
