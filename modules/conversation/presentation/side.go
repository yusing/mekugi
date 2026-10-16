// Source: apps/mekugi/internal/router/app_server_btw.go:21:377@[4ded0a4e] transient side-answer presentation.
package presentation

import (
	"slices"
	"time"
	"unicode/utf8"
)

const SideCompactionRequired = "Side question requires compaction. Compact Main, then close this panel and retry /btw."

type Answer struct{ ID, Text string }

// SideAnswer retains only the current exchange. Codex owns the full side context.
type SideAnswer struct {
	Question, Status string
	Pending          Input
	Answer           []Answer
	Alert, Truncated bool
	AttachmentNotice string
	Started          time.Time
}

type SideView struct {
	Question, Status string
	Answer           []Answer
	Alert, Truncated bool
	AttachmentNotice string
	Started          time.Time
}

func (s *SideAnswer) View() SideView {
	return SideView{Question: s.Question, Status: s.Status, Answer: slices.Clone(s.Answer), Alert: s.Alert, Truncated: s.Truncated, AttachmentNotice: s.AttachmentNotice, Started: s.Started}
}

func (s *SideAnswer) Begin(question string, pending Input) {
	*s = SideAnswer{Question: question, Pending: pending, AttachmentNotice: pending.AttachmentNotice, Status: "Waiting for Main's submission…"}
}

func (s *SideAnswer) Fail(message string, closed bool) Input {
	s.Alert, s.Status = true, message
	pending := s.Pending
	s.Pending = Input{}
	if !closed && pending.Text != "" {
		return Join(Input{Text: "/btw"}, pending)
	}
	return Input{}
}

func (s *SideAnswer) Complete(status, failure string) {
	s.Pending = Input{}
	s.Status, s.Alert = status, status == "failed"
	if failure != "" {
		s.Status = failure
	}
}

func (s *SideAnswer) Close() { s.Pending, s.Answer = Input{}, nil }

// Completed items replace streamed deltas, under the same UTF-8-safe display bound.
func (s *SideAnswer) Append(id, text string, completed bool) {
	index := slices.IndexFunc(s.Answer, func(item Answer) bool { return item.ID == id })
	if index < 0 {
		if len(s.Answer) >= 256 {
			s.Truncated = true
			return
		}
		s.Answer = append(s.Answer, Answer{ID: id})
		index = len(s.Answer) - 1
	}
	if completed {
		s.Answer[index].Text = ""
	}
	room := 256 << 10
	for _, item := range s.Answer {
		room -= len(item.Text)
	}
	if len(text) > room {
		for room > 0 && !utf8.RuneStart(text[room]) {
			room--
		}
		text, s.Truncated = text[:room], true
	}
	s.Answer[index].Text += text
}
