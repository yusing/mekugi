// Source: apps/mekugi/internal/router/app_server_approvals.go:27:72,519:536,653:740@[42afca9c] approval presentation.
package prompt

import (
	"slices"

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
}

func (m *Approvals) Add(a *Approval) { m.Pending = append(m.Pending, a); m.AutoOpen = true }
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
func (a *Approval) SelectDenial() bool {
	if n := a.DenialChoice(); n >= 0 {
		a.Selected = n
		return true
	}
	return false
}

type ApprovalView struct {
	Selected                                        int
	ID, Thread, Turn, Item, Title, Subject, Outcome string
	Details                                         []string
	Choices                                         []Choice
}

func (a *Approval) View() ApprovalView {
	return ApprovalView{Selected: a.Selected, ID: a.ID, Thread: a.Thread, Turn: a.Turn, Item: a.Item, Title: a.Title, Subject: a.Subject, Outcome: a.Outcome, Details: slices.Clone(a.Details), Choices: slices.Clone(a.Choices)}
}
