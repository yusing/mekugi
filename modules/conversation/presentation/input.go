// Source: modules/conversation/presentation/input.go:1:179@[14a6d1f3]
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

// CompactIntent marks a queued command, independently of literal message text.
// Host operation gates stay in session control.
type CompactIntent struct{ WaitForIdle, Continue bool }

// Input retains immutable attachment snapshots and neutral question identity.
// It contains no host request, callback, secret, or renderer state.
type Input struct {
	composer.Draft
	// Origin identifies the requesting editor lifetime for detached restoration.
	// Empty retains the in-process editor path. It confers no execution authority.
	Origin string
	// NextTurn retains queue disposition through asynchronous recovery.
	NextTurn bool
	// Delivery identifies reserved restoration capacity at the originating view.
	// It is opaque provenance, not an execution capability or renderer owner.
	Delivery   uint64
	Compact    *CompactIntent
	Question   *prompt.Call
	ReplyCount int
}

type Submission struct {
	composer.Draft
	Parts []Input
}

type Session struct {
	Prediction      Prediction
	Completion      completion.State
	Editor          composer.Editor
	PromptEditor    prompt.Editor
	Questions       prompt.Questions
	Approvals       prompt.Approvals
	Unsent, Queued  []Input
	Submitted       map[string]Submission
	Shell           Input
	ShellFromEditor bool // Only a consumed local draft may be restored after rejection.
	Side            *SideAnswer
	BTW             *SideAnswer
}

// Source: apps/mekugi/internal/router/app_server_shell.go:15:17@[cf6c7832]
func (m *Session) ShellMode() bool {
	return ShellDraftMode(m.Editor.Text, m.Questions.Current() != nil || m.Approvals.Open)
}

// ShellDraftMode applies the shared draft interpretation to a client-local
// editor without constructing a session or sharing its prompt selection.
func ShellDraftMode(text string, promptOpen bool) bool {
	return !promptOpen && strings.HasPrefix(text, "!")
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
	if stack[0].Question == nil && stack[0].Compact == nil {
		for n < len(stack) && stack[n].Question == nil && stack[n].Compact == nil {
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
	RetryAttachments
)

type PendingView struct {
	CanSteer        bool
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
	return PendingView{CanSteer: slices.ContainsFunc(m.Queued, steerable), Reason: reason, Waiting: values(slices.Concat(m.Parts(ids), m.Unsent)), Queued: values(m.Queued)}
}
