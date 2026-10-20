// Source: apps/mekugi/internal/router/app_server_btw.go:21:377@[4ded0a4e] transient side-answer presentation.
package presentation

import (
	"crypto/rand"
	"slices"
	"time"
	"unicode/utf8"
)

const SideCompactionRequired = "Side question requires compaction. Close this answer, compact Main, then retry."

type Answer struct{ ID, Text string }

// SideExchange is a completed display exchange, independent of host context.
type SideExchange struct {
	ID                                  uint64
	Question, AttachmentNotice, Failure string
	Answer                              []Answer
}

// SideAnswer retains bounded display history. Codex owns the full side context.
type SideAnswer struct {
	OneOff           bool
	Key              string
	Drafts           Session
	History          []SideExchange
	ID               uint64
	Question, Status string
	Pending          Input
	Answer           []Answer
	Alert, Truncated bool
	AttachmentNotice string
	Started          time.Time
}

type SideView struct {
	History          []SideExchange
	ID               uint64
	Question, Status string
	Answer           []Answer
	Alert, Truncated bool
	AttachmentNotice string
	Started          time.Time
}

func (s *SideAnswer) Begin(question string, pending Input) {
	history := s.History
	if s.ID != 0 && s.Question != "" {
		exchange := SideExchange{ID: s.ID, Question: s.Question, AttachmentNotice: s.AttachmentNotice, Answer: s.Answer}
		if s.Alert {
			exchange.Failure = s.Status
		}
		history = append(history, exchange)
	}
	key := s.Key
	if key == "" {
		key = rand.Text()
	}
	*s = SideAnswer{OneOff: s.OneOff, Key: key, Drafts: s.Drafts, History: history, Truncated: s.Truncated, ID: s.ID + 1, Question: question, Pending: pending, AttachmentNotice: pending.AttachmentNotice, Status: "Waiting for Main's submission…"}
	s.bound()
}

func (s *SideAnswer) Fail(message string, closed bool) Input {
	s.Alert, s.Status = true, message
	pending := s.Pending
	s.Pending = Input{}
	if !closed && pending.Text != "" {
		return pending
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

func (s *SideAnswer) Close() { s.Pending, s.Answer, s.History = Input{}, nil, nil }

// Completed items replace streamed deltas, under the same UTF-8-safe display bound.
func (s *SideAnswer) Append(id, text string, completed bool) {
	index := slices.IndexFunc(s.Answer, func(item Answer) bool { return item.ID == id })
	if index < 0 {
		if len(s.Answer) >= 255 {
			s.Truncated = true
			return
		}
		s.Answer = append(s.Answer, Answer{ID: id})
		index = len(s.Answer) - 1
	}
	if completed {
		s.Answer[index].Text = ""
	}
	s.Answer[index].Text += text
	s.bound()
}

// Evict whole old exchanges first, then truncate the current exchange on UTF-8 boundaries.
func (s *SideAnswer) bound() {
	bytes, items := len(s.Question)+len(s.AttachmentNotice), 1+len(s.Answer)
	for _, a := range s.Answer {
		bytes += len(a.Text)
	}
	for _, h := range s.History {
		bytes += len(h.Question) + len(h.AttachmentNotice) + len(h.Failure)
		items += 1 + len(h.Answer)
		for _, a := range h.Answer {
			bytes += len(a.Text)
		}
	}
	for len(s.History) > 0 && (bytes > 256<<10 || items > 256) {
		h := s.History[0]
		bytes -= len(h.Question) + len(h.AttachmentNotice) + len(h.Failure)
		items -= 1 + len(h.Answer)
		for _, a := range h.Answer {
			bytes -= len(a.Text)
		}
		s.History = slices.Delete(s.History, 0, 1)
		s.Truncated = true
	}
	room := 256 << 10
	retain := func(text *string) {
		if len(*text) > room {
			for room > 0 && !utf8.RuneStart((*text)[room]) {
				room--
			}
			*text = (*text)[:room]
			s.Truncated = true
		}
		room -= len(*text)
	}
	retain(&s.Question)
	retain(&s.AttachmentNotice)
	for i := range s.Answer {
		retain(&s.Answer[i].Text)
	}
}

// Label and Caller identify the shared side destination without changing host identity.
func (s *SideAnswer) Label() string {
	if s.OneOff {
		return "One-off"
	}
	return "Side"
}
func (s *SideAnswer) Caller() string {
	if s.OneOff {
		return "/oneoff"
	}
	return "/side"
}
