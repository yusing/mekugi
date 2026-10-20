// Source: apps/mekugi/internal/router/app_server_input_queue.go:35:430@[4ded0a4e] payload ordering and snapshots.
package presentation

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

// Service accesses only conversation-owned entries in application scope stores.
type Service struct {
	session    scope.Key[Session]
	transcript scope.Key[transcript.State]
}

func (s *Service) Transcript(store *scope.Store) *transcript.State { return s.transcript.Value(store) }

func (s *Service) Session(store *scope.Store) *Session { return s.session.Value(store) }

// CompactIntent accompanies a queued command. Host operation gates stay in session control.
type CompactIntent struct{ WaitForIdle, Continue bool }

// Input retains immutable attachment snapshots and neutral question identity.
// It contains no host request, callback, secret, or renderer state.
type Input struct {
	composer.Draft
	Compact    CompactIntent
	Question   *prompt.Call
	ReplyCount int
}

type Submission struct {
	composer.Draft
	Parts []Input
}

type Session struct {
	Completion     completion.State
	Editor         composer.Editor
	PromptEditor   prompt.Editor
	Questions      prompt.Questions
	Approvals      prompt.Approvals
	Unsent, Queued []Input
	Submitted      map[string]Submission
	Shell          Input
	Side           *SideAnswer
}

// Source: apps/mekugi/internal/router/app_server_shell.go:15:17@[cf6c7832]
func (m *Session) ShellMode() bool {
	return m.Questions.Current() == nil && !m.Approvals.Open && strings.HasPrefix(m.Editor.Text, "!")
}

// CarryInput transfers locally editable state and still-live approvals during
// Main replacement. Host questions and submitted operations stay retired.
// Source: apps/mekugi/internal/router/app_server_commands.go:126:192@[59a0d2b9]
func (m *Session) CarryInput(previous *Session) {
	m.Editor, previous.Editor = previous.Editor, composer.Editor{}
	m.PromptEditor, previous.PromptEditor = previous.PromptEditor, prompt.Editor{}
	m.Approvals, previous.Approvals = previous.Approvals, prompt.Approvals{}
	m.Unsent, previous.Unsent = previous.Unsent, nil
	m.Queued, previous.Queued = previous.Queued, nil
}

func Drafts(parts []Input) []composer.Draft {
	values := make([]composer.Draft, len(parts))
	for i := range parts {
		values[i] = parts[i].Draft
	}
	return values
}
func Join(parts ...Input) Input { return Input{Draft: composer.Join(Drafts(parts)...)} }

func (m *Session) TakeWaiting() []Input {
	parts := slices.Concat(m.Unsent, m.Queued)
	m.Unsent, m.Queued = nil, nil
	return parts
}

func (m *Session) Parts(ids []string) []Input {
	var parts []Input
	for _, id := range ids {
		parts = append(parts, m.Submitted[id].Parts...)
	}
	return parts
}

func (m *Session) Retain(ids []string) {
	for id := range m.Submitted {
		if !slices.Contains(ids, id) {
			delete(m.Submitted, id)
		}
	}
}

func (m *Session) Stage(id string, submission Submission) {
	submission.Draft = submission.Draft.Clone()
	submission.Parts = slices.Clone(submission.Parts)
	for i := range submission.Parts {
		submission.Parts[i].Draft = submission.Parts[i].Draft.Clone()
	}
	if m.Submitted == nil {
		m.Submitted = make(map[string]Submission)
	}
	m.Submitted[id] = submission
}

// AdvancePrompt lets ordinary input pass answers waiting for tool-input generation.
func (m *Session) AdvancePrompt() bool {
	first := 0
	for first < len(m.Unsent) && m.Unsent[first].Question != nil {
		first++
	}
	if first == len(m.Unsent) {
		return false
	}
	next, rest := Batch(m.Unsent[first:])
	m.Unsent = slices.Concat(next, m.Unsent[:first], rest)
	return true
}

// A question reply or compaction command is never joined with another submission.
func Batch(stack []Input) ([]Input, []Input) {
	if len(stack) == 0 {
		return nil, nil
	}
	n := 1
	if stack[0].Question == nil && stack[0].Text != "/compact" {
		for n < len(stack) && stack[n].Question == nil && stack[n].Text != "/compact" {
			n++
		}
	}
	return stack[:n], stack[n:]
}

// WaitingReason describes admission without host handles or terminal hints.
type WaitingReason uint8

const (
	Waiting WaitingReason = iota
	Steering
	CompactAfterInterrupt
	ShellRunning
	SendingAfterInterrupt
	RestoringAfterInterrupt
	CompactRunning
	CompactQueued
	ApplyingSettings
)

type PendingView struct {
	Reason          WaitingReason
	Waiting, Queued []PendingText
}
type PendingText struct {
	Text    string
	Replies int
}

func (m *Session) Pending(ids []string, reason WaitingReason) PendingView {
	values := func(parts []Input) []PendingText {
		out := make([]PendingText, len(parts))
		for i, part := range parts {
			out[i] = PendingText{Text: part.Text, Replies: part.ReplyCount}
		}
		return out
	}
	return PendingView{Reason: reason, Waiting: values(slices.Concat(m.Parts(ids), m.Unsent)), Queued: values(m.Queued)}
}
