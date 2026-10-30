package sessioncontrol

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// AgentQuestion is an agent side question handed to the sole request owner.
type AgentQuestion struct {
	main    SideMain
	text    string
	result  chan questionResult
	settled bool
}

type questionResult struct {
	answer string
	err    error
}

func (r *Requests[T]) Questions() <-chan *AgentQuestion { return r.questions }

// SideQuestion runs the restricted human /btw fork workflow without a human slot.
// A caller that stops waiting abandons the answer; the fork still cleans up.
func (r *Requests[T]) SideQuestion(ctx context.Context, target *control.Member, text string) (string, error) {
	// Side requests use low reasoning, so the source effort is not inherited.
	q := &AgentQuestion{main: SideMain{Thread: target.Thread, Model: target.Settings.Model}, text: text, result: make(chan questionResult, 1)}
	if target.Settings.Tier != nil {
		q.main.Tier = *target.Settings.Tier
	}
	select {
	case r.questions <- q:
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.closed:
		return "", errors.New("side-question owner closed")
	}
	select {
	case result := <-q.result:
		return result.answer, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.closed:
		return "", errors.New("side-question owner closed")
	}
}

func (r *Requests[T]) DispatchQuestion(q *AgentQuestion) {
	s := &Side{SideAnswer: &conversation.SideAnswer{}, LowReasoning: true, Busy: true, question: q}
	s.Begin(q.text, conversation.Input{Text: q.text})
	update, err := r.StartSide(s, q.main, time.Now())
	if err != nil {
		s.Starting = false
		q.finish("", err)
	} else if update.Side != nil && update.Side.Alert {
		q.finish("", errors.New(update.Side.Status))
	}
}

// finish delivers the first outcome; later outcomes come from cleanup.
func (q *AgentQuestion) finish(answer string, err error) {
	if !q.settled {
		q.settled = true
		q.result <- questionResult{answer, err}
	}
}

// ObserveAgentQuestion consumes question events before interactive side rendering.
// The caller receives its outcome immediately; the fork is then released through
// the ordinary side ledger.
func (r *Requests[T]) ObserveAgentQuestion(m appserver.Message, response Request[T], observations *Observations) bool {
	s := response.Side
	if m.Method != "" {
		if !r.HasSides() {
			return false
		}
		if event, err := binding.DecodeNotification(m.Method, m.Params); err == nil {
			s = r.Side(event.ThreadID)
		}
	}
	if s == nil || s.question == nil {
		return false
	}
	q := s.question
	update, err := r.ObserveSide(m, response, q.main.Thread, observations)
	switch {
	case err != nil:
		s.Interrupting, s.Unloading = false, false
		q.finish("", err)
		return true
	case update.NoticeError:
		q.finish("", errors.New(update.Notice))
		return true
	case s.Archived:
		s.Close()
		r.CloseSide(s.Thread)
		q.finish("", errors.New("side question closed before answering"))
		return true
	case s.Closed:
		return true
	case s.Alert || s.Unloaded:
		q.finish("", errors.New(s.Status))
	case m.Method == "turn/completed":
		var answer strings.Builder
		for _, item := range s.Answer {
			answer.WriteString(item.Text)
		}
		switch {
		case s.Status != "completed":
			q.finish("", errors.New("side answer did not complete: "+s.Status))
		case s.Truncated:
			q.finish("", errors.New("side answer exceeded the retained output limit"))
		default:
			q.finish(answer.String(), nil)
		}
	case update.Flush:
		if _, err := r.StartSide(s, q.main, time.Now()); err == nil {
			return true
		} else {
			s.Starting = false
			q.finish("", err)
		}
	default:
		return true
	}
	s.Close()
	if r.ReleaseSide(s) != nil {
		// Joined shutdown retries the release.
		s.Interrupting, s.Unloading = false, false
	}
	return true
}
