package sessioncontrol

import (
	"crypto/rand"
	json "encoding/json/v2"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// SideUpdate carries application consequences, never renderer callbacks.
type SideUpdate struct {
	Handled, Flush, TurnRequested bool
	Restore                       conversationmodel.Input
	Notice                        string
}

type SideMain struct {
	Thread, Model, Effort, Tier string
	Blocked, Replacing          bool
}

func (s *Side) fail(message string) SideUpdate {
	s.Busy, s.Starting = false, false
	return SideUpdate{Handled: true, Restore: s.SideAnswer.Fail(message, s.Closed)}
}

func (r *Requests[T]) sendSide(s *Side, method string, params any) error {
	var owner T
	id, err := r.Send(method, params, "", owner)
	if err == nil {
		request := r.pending[id]
		request.Side = s
		r.pending[id] = request
	}
	return err
}

// StartSide receives acquired Conversation input and uses the one request ledger.
// Source: apps/mekugi/internal/router/app_server_btw.go:68:128@0d7819f3.
func (r *Requests[T]) StartSide(s *Side, main SideMain, now time.Time) (SideUpdate, error) {
	if !s.Ready(main.Replacing) {
		return SideUpdate{}, nil
	}
	if s.Thread == "" {
		if main.Blocked {
			return SideUpdate{}, nil
		}
		s.Starting, s.Status = true, "Branching…"
		params := map[string]any{"threadId": main.Thread, "ephemeral": true, "excludeTurns": true, "sandbox": "read-only", "approvalPolicy": "never"}
		if main.Model != "" {
			params["model"] = main.Model
		}
		if main.Effort != "" {
			params["config"] = map[string]any{"model_reasoning_effort": main.Effort}
		}
		params["serviceTier"] = nil
		if main.Tier != "" {
			params["serviceTier"] = main.Tier
		}
		return SideUpdate{}, r.sendSide(s, "thread/fork", params)
	}
	s.Starting, s.Status, s.Started = true, "Answering", now
	if err := s.Pending.Draft.IdentifyAttachments(rand.Text()); err != nil {
		return s.fail(err.Error()), nil
	}
	input := conversationbinding.Input(s.Pending.Draft)
	input = append(appserver.Input("This is a /btw side question. Answer only the side question using the conversation context. Do not continue the main task or use tools."), input...)
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(s.Pending.Attachments) > 0 || len(s.Pending.Skills) > 0) && textBytes > codexresponses.UserTextLimit {
		return s.fail("Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry."), nil
	}
	return SideUpdate{TurnRequested: true}, r.sendSide(s, "turn/start", map[string]any{"threadId": s.Thread, "input": input, "environments": []any{}})
}

func (r *Requests[T]) ReleaseSide(s *Side) error {
	method := s.Release()
	if method == "" {
		return nil
	}
	params := map[string]any{"threadId": s.Thread}
	if method == "turn/interrupt" {
		params["turnId"] = s.Turn
	}
	return r.sendSide(s, method, params)
}

// ObserveSide runs before Main's filtering. Closed docks retain their original
// operation through late acknowledgements and thread/closed notification.
// Source: apps/mekugi/internal/router/app_server_btw.go:162:305@0d7819f3.
func (r *Requests[T]) ObserveSide(m appserver.Message, response Request[T], main string, observations *Observations) (SideUpdate, error) {
	s, method := response.Side, response.Method
	if m.Method == "" {
		if s == nil {
			return SideUpdate{}, nil
		}
		update := SideUpdate{Handled: true}
		if method == "turn/interrupt" {
			s.InterruptAcknowledged()
			if m.Error != nil {
				update.Notice = "Could not interrupt side answer: " + m.Error.Message
			}
			return update, r.ReleaseSide(s)
		}
		if method == "thread/unsubscribe" {
			if m.Error != nil {
				update.Notice = "Could not unload side conversation: " + m.Error.Message
			} else {
				observations.SetSideThread(s.Thread, false)
			}
			return update, nil
		}
		s.Starting = false
		if m.Error != nil {
			update = s.fail(m.Error.Message)
			if s.Closed {
				return update, r.ReleaseSide(s)
			}
			return update, nil
		}
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			return s.fail("Invalid side-thread response: " + err.Error()), nil
		}
		switch method {
		case "thread/fork":
			if result.Thread.ID == "" || result.Thread.ID == main {
				return s.fail("Codex did not return a separate side thread"), nil
			}
			s.Thread = result.Thread.ID
			observations.SetSideThread(s.Thread, true)
			r.TrackSide(s.Thread, s)
		case "turn/start":
			if result.Turn.ID == "" {
				update = s.fail("Codex returned no side-turn identity")
			}
			s.StartAcknowledged(result.Turn.ID)
		}
		if s.Closed {
			return update, r.ReleaseSide(s)
		}
		update.Flush = true
		return update, nil
	}
	if !r.HasSides() {
		return SideUpdate{}, nil
	}
	var p struct {
		binding.Notification
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return SideUpdate{}, nil
	}
	thread := p.ThreadID
	if m.Method == "thread/started" {
		thread = p.Thread.ID
	}
	s = r.Side(thread)
	if s == nil {
		return SideUpdate{}, nil
	}
	update := SideUpdate{Handled: true}
	if m.Method == "thread/closed" {
		observations.SetSideThread(thread, false)
		r.CloseSide(thread)
		if !s.Closed {
			s.Thread, s.Turn, s.Busy, s.Starting = "", "", false, false
			s.Status, s.Alert = "Side conversation closed · /btw starts a new branch", true
		}
		return update, nil
	}
	if len(m.ID) != 0 {
		s.Status, s.Alert = "Side answer blocked on "+m.Method+" · Esc closes", true
		return update, nil
	}
	switch m.Method {
	case "turn/started":
		s.Turn = p.Turn.ID
	case "turn/completed":
		if !s.Complete(p.Turn.ID) {
			return update, nil
		}
		if s.Status == conversationmodel.SideCompactionRequired || p.Turn.Error != nil && strings.Contains(p.Turn.Error.Message, conversationmodel.SideCompactionRequired) {
			return s.fail(conversationmodel.SideCompactionRequired), nil
		}
		failure := ""
		if p.Turn.Error != nil {
			failure = p.Turn.Error.Message
		}
		s.SideAnswer.Complete(p.Turn.Status, failure)
	case "error":
		if p.Error != nil {
			if strings.Contains(p.Error.Message, conversationmodel.SideCompactionRequired) {
				return s.fail(conversationmodel.SideCompactionRequired), nil
			}
			s.Status, s.Alert = p.Error.Message, true
		}
	case "item/agentMessage/delta", "item/completed":
		if s.Closed || p.TurnID != s.Turn {
			return update, nil
		}
		if m.Method == "item/completed" && p.Item.Type != "agentMessage" {
			return update, nil
		}
		id := p.ItemID
		if id == "" {
			id = p.Item.ID
		}
		if m.Method == "item/completed" {
			s.Append(id, p.Item.Text, true)
		} else {
			s.Append(id, p.Delta, false)
		}
	}
	return update, nil
}
