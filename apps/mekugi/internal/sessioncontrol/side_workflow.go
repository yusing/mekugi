package sessioncontrol

import (
	"context"
	"crypto/rand"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/observation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// ArchiveSides drains only side cleanup after ordinary input has stopped. The
// host connection remains open until its archive acknowledgements arrive.
func (r *Requests[T]) ArchiveSides(ctx context.Context, messages <-chan appserver.Message, additional ...*Side) error {
	operations := make(map[*Side]bool)
	for _, request := range r.pending {
		if request.Side != nil {
			operations[request.Side] = true
		}
	}
	r.sidesMu.RLock()
	for _, side := range r.sides {
		operations[side] = true
	}
	r.sidesMu.RUnlock()
	for _, side := range additional {
		if side != nil {
			operations[side] = true
		}
	}
	for side := range operations {
		side.Close()
		if err := r.ReleaseSide(side); err != nil {
			return err
		}
	}
	for {
		pending := false
		for side := range operations {
			pending = pending || !side.Archived() && (side.Thread != "" || side.Starting)
		}
		if !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("side history cleanup: %w", ctx.Err())
		case message, ok := <-messages:
			if !ok {
				return errors.New("host disconnected before side history was archived")
			}
			var response Request[T]
			if message.Method == "" {
				response, _ = r.Take(string(message.ID))
			}
			update, err := r.ObserveSide(message, response, "")
			if err != nil {
				return err
			}
			if update.NoticeError {
				return errors.New(update.Notice)
			}
		}
	}
}

// SideUpdate carries application consequences, never renderer callbacks.
type SideUpdate struct {
	Handled, Flush, TurnRequested bool
	Forward                       bool
	ReplyThread, ReplyTurn        string
	ClosedThread                  string
	Restore                       conversationmodel.Input
	Notice                        string
	NoticeError                   bool
	Side                          *Side
}

type SideMain struct {
	Thread, Model, Effort, Tier string
	Blocked, Replacing          bool
	Input                       *InputRequest
}

func (s *Side) fail(message string) SideUpdate {
	s.Busy, s.Starting, s.ForkPending = false, false, false
	return SideUpdate{Handled: true, Side: s, Restore: s.SideAnswer.Fail(message, s.Closed)}
}

func (r *Requests[T]) sendSide(s *Side, method string, params any, input *InputRequest) error {
	var owner T
	id, err := r.Send(method, params, "", owner)
	if err == nil {
		request := r.pending[id]
		request.Side = s
		if method == "turn/start" || method == "turn/steer" {
			draft := s.Pending.Draft.Clone()
			request.SideDraft = &draft
		}
		if input != nil && input.Settled != nil {
			request.input = input
		}
		r.pending[id] = request
	} else if input != nil && input.Settled != nil {
		input.Settled("", &binding.DispatchError{Outcome: binding.Uncertain, Method: method, Cause: err})
	}
	return err
}

// ApplySideSettings retains the original side operation through view switches.
func (r *Requests[T]) ApplySideSettings(s *Side, plan SettingsPlan) error {
	if plan.Action == SettingsThread || plan.Action == SettingsTurn {
		params := maps.Clone(plan.Change)
		params["threadId"] = s.Thread
		method := "thread/settings/update"
		if plan.Action == SettingsTurn {
			method, params["turnId"] = "turn/settings/update", plan.Turn
		}
		if err := r.sendSide(s, method, params, nil); err != nil {
			return err
		}
	}
	s.Settings.Admit(plan)
	return nil
}

// SideThreadSource distinguishes retained internal forks from selectable sessions.
const SideThreadSource = "mekugi_side"

// StartSide receives acquired Conversation input and uses the one request ledger.
// Source: apps/mekugi/internal/router/app_server_btw.go:68:128@0d7819f3.
func (r *Requests[T]) StartSide(s *Side, main SideMain, now time.Time) (SideUpdate, error) {
	if !s.Ready(main.Replacing) && (main.Input == nil || main.Replacing || s.Closed || s.Unloaded() || s.Starting || s.Settings.Pending()) {
		return SideUpdate{}, nil
	}
	if s.Thread == "" {
		if main.Blocked {
			return SideUpdate{}, nil
		}
		if main.Effort == "ultra" {
			if r.CatalogPending {
				return SideUpdate{}, nil
			}
			if r.InheritedEffort == nil {
				update := s.fail("Inherited ultra requires the selected model catalog")
				update.Notice, update.NoticeError = s.Status, true
				return update, nil
			}
			var err error
			main.Effort, err = r.InheritedEffort(main.Model, main.Effort)
			if err != nil {
				update := s.fail("Could not select inherited reasoning effort: " + err.Error())
				update.Notice, update.NoticeError = s.Status, true
				return update, nil
			}
		}
		s.Source, s.Model, s.Effort, s.Tier = main.Thread, main.Model, main.Effort, main.Tier
		s.Starting, s.Status = true, "Branching…"
		params := map[string]any{"threadId": main.Thread, "threadSource": SideThreadSource, "excludeTurns": true}
		if !s.OneOff {
			params["sandbox"], params["approvalPolicy"] = "read-only", "never"
		}
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
		return SideUpdate{}, r.sendSide(s, "thread/fork", params, nil)
	}
	if s.Pending.Text == "" {
		return SideUpdate{}, nil
	}
	s.Starting, s.Status, s.Started = true, "Answering", now
	if s.OneOff {
		s.Status = "Working"
	}
	id, turn := rand.Text(), ""
	if main.Input != nil {
		id, turn = main.Input.ID, main.Input.Turn
	}
	input, err := s.prepareInput(&s.Pending.Draft, id, turn == "")
	if err != nil {
		return s.fail(err.Error()), nil
	}
	params := map[string]any{"threadId": s.Thread, "input": input}
	method := "turn/start"
	if main.Input != nil {
		params["clientUserMessageId"] = id
	}
	if turn != "" {
		method, params["expectedTurnId"] = "turn/steer", turn
		s.Turn, s.Starting = turn, false
	}
	if !s.OneOff && turn == "" {
		params["environments"] = []any{}
	}
	return SideUpdate{TurnRequested: true}, r.sendSide(s, method, params, main.Input)
}

func (r *Requests[T]) ReleaseSide(s *Side) error {
	if s.Closed {
		r.interruptSideUsageWaits(s)
	}
	if s.Archived() {
		if s.Closed {
			r.CloseSide(s.Thread)
		}
		return nil
	}
	method := s.Release()
	if method == "" {
		return nil
	}
	params := map[string]any{"threadId": s.Thread}
	if method == "turn/interrupt" {
		params["turnId"] = s.Turn
	}
	err := r.sendSide(s, method, params, nil)
	if err != nil {
		s.release.Failed()
	}
	return err
}

func (r *Requests[T]) interruptSideUsageWaits(s *Side) {
	if r.Coordination == nil || r.InterruptWait == nil {
		return
	}
	for _, thread := range r.SideThreads(s) {
		if wait := r.Coordination.WaitPublication(thread); wait != nil && wait.UsageLimit && wait.Stop == "committed" {
			r.InterruptWait(thread, wait.ID)
		}
	}
}

// ObserveSide runs before Main's filtering. Closed docks retain their original
// operation through late acknowledgements and thread/closed notification.
// Source: apps/mekugi/internal/router/app_server_btw.go:162:305@0d7819f3.
func (r *Requests[T]) ObserveSide(m appserver.Message, response Request[T], main string) (SideUpdate, error) {
	s, method := response.Side, response.Method
	if m.Method == "" {
		if s == nil {
			return SideUpdate{}, nil
		}
		update := SideUpdate{Handled: true, Side: s}
		if method == "turn/interrupt" {
			s.InterruptAcknowledged()
			if m.Error != nil {
				update.Notice, update.NoticeError = "Could not interrupt side answer: "+m.Error.Message, true
			}
			return update, r.ReleaseSide(s)
		}
		if method == "thread/archive" {
			if m.Error != nil {
				s.release.Failed()
				update.Notice, update.NoticeError = "Could not archive side conversation: "+m.Error.Message, true
			} else {
				s.release.Complete()
				if s.Closed {
					r.CloseSide(s.Thread)
				}
			}
			return update, nil
		}
		if s.Unloaded() {
			if method == "turn/start" && (response.SideInput == nil || response.SideInput.Question == nil || response.SideInput.Question.Thread == s.Thread) {
				s.Starting = false
			}
			if s.Closed {
				return update, r.ReleaseSide(s)
			}
			return update, nil
		}
		if method == "thread/settings/update" || method == "turn/settings/update" {
			ack, err := s.Settings.Acknowledge(method, m.Result, m.Error != nil)
			if s.Closed {
				return update, err
			}
			if m.Error != nil {
				update.Notice, update.NoticeError = "Settings update failed: "+m.Error.Message, true
			} else if method == "turn/settings/update" {
				update.Notice = "Settings saved for next turn"
				if ack.Status == "applied" {
					update.Notice = "Settings updated"
				}
			}
			update.Flush = !s.Settings.Pending()
			return update, err
		}
		if input := response.SideInput; input != nil {
			root := input.Question == nil || input.Question.Thread == s.Thread
			if root && method == "turn/start" {
				s.Starting = false
			}
			if m.Error != nil {
				if input.Question != nil {
					input.Question.Reject()
				}
				if !s.Closed {
					update.Restore, update.Side, update.Notice = *input, s, m.Error.Message
					update.NoticeError = true
				}
				if root && method == "turn/start" {
					s.Busy = false
				}
			} else if method == "turn/steer" && root && !s.OneOff && !s.Closed {
				s.AppendQuestion(response.SideExchange, *input)
			} else if method == "turn/start" {
				var result struct {
					Turn struct {
						ID string `json:"id"`
					} `json:"turn"`
				}
				if err := json.Unmarshal(m.Result, &result); err != nil || result.Turn.ID == "" {
					if input.Question != nil {
						input.Question.Reject()
					}
					if root {
						s.Busy = false
					}
					if !s.Closed {
						update.Restore, update.Notice = *input, "Codex returned no one-off reply turn identity"
						update.NoticeError = true
					}
					return update, nil
				}
				if root {
					s.StartAcknowledged(result.Turn.ID)
				} else {
					if !s.Closed {
						update.ReplyThread, update.ReplyTurn = input.Question.Thread, result.Turn.ID
					}
				}
			}
			if m.Error == nil && !s.Closed {
				s.AcknowledgeInput(*input)
			}
			if s.Closed {
				return update, r.ReleaseSide(s)
			}
			update.Flush = true
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
			AcceptedThread
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			return s.fail("Invalid side-thread response: " + err.Error()), nil
		}
		switch method {
		case "thread/fork":
			if result.Thread.ID == "" || result.Thread.ID == main || result.Thread.ID == s.Source || r.Side(result.Thread.ID) != nil {
				return s.fail("Codex did not return a separate side thread"), nil
			}
			s.Thread, s.ForkPending = result.Thread.ID, false
			if result.Model != "" {
				s.Model, s.Effort, s.Tier = result.Model, result.ReasoningEffort, result.ServiceTier
			}
			if s.Pending.Text == "" {
				s.Status = "Ready"
				s.Busy = false
			}
			r.TrackSide(s.Thread, s)
		case "turn/start":
			if result.Turn.ID == "" {
				update = s.fail("Codex returned no side-turn identity")
			}
			s.StartAcknowledged(result.Turn.ID)
		}
		if !s.Closed && response.SideDraft != nil && (method == "turn/steer" || method == "turn/start" && result.Turn.ID != "") {
			s.AcknowledgeInput(conversationmodel.Input{Draft: *response.SideDraft})
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
	var p binding.Notification
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return SideUpdate{}, nil
	}
	thread := p.ThreadID
	if m.Method == "thread/started" {
		thread = p.Thread.ID
		r.AdmitSideChild(thread, p.Thread.ParentThreadID)
	}
	s = r.Side(thread)
	if s == nil {
		return SideUpdate{}, nil
	}
	update := SideUpdate{Handled: true, Side: s, Forward: !s.Closed && !s.Unloaded() && (s.OneOff || m.Method == "thread/tokenUsage/updated" || m.Method == "thread/settings/updated")}
	if thread != s.Thread {
		// Descendants share admission and cleanup, never their root's turn lifecycle.
		return update, nil
	}
	if m.Method == "thread/closed" || m.Method == "thread/archived" {
		update.ClosedThread = thread
		s.release.HostClosed()
		r.interruptSideUsageWaits(s)
		if m.Method == "thread/archived" {
			s.release.Complete()
		}
		if s.Closed && s.Archived() {
			r.CloseSide(thread)
		} else if !s.Closed {
			s.Turn, s.Busy, s.Starting = "", false, false
			s.ForkPending = false
			update.Restore = s.SideAnswer.Fail("Side conversation closed · return to Main to start a new conversation", false)
		}
		return update, nil
	}
	if s.Unloaded() {
		return update, nil
	}
	if len(m.ID) != 0 {
		if s.OneOff {
			return update, nil
		}
		s.Status, s.Alert = "Side answer blocked on "+m.Method+" · Esc closes", true
		return update, nil
	}
	if observation.Kind(m.Method).ResponseProgress() && p.TurnID == s.Turn && s.Turn != "" && !s.Closed && !s.Alert {
		s.Status = "Answering"
		if s.OneOff {
			s.Status = "Working"
		}
	}
	switch m.Method {
	case "thread/settings/updated":
		if s.Closed {
			return update, nil
		}
		var event struct {
			Settings struct {
				Model       string `json:"model"`
				Effort      string `json:"effort"`
				ServiceTier string `json:"serviceTier"`
			} `json:"threadSettings"`
		}
		if err := json.Unmarshal(m.Params, &event); err != nil {
			return update, err
		}
		s.Model, s.Effort, s.Tier = event.Settings.Model, event.Settings.Effort, event.Settings.ServiceTier
		confirmation := s.Settings.Confirmed(s.Turn, s.Effort)
		if confirmation.Requested {
			if confirmation.Update.Action == SettingsTurn {
				return update, r.ApplySideSettings(s, confirmation.Update)
			}
			update.Notice, update.Flush = "Settings saved for next turn", true
		}
	case "turn/started":
		if s.Waiting != "" && !s.Closed {
			s.Waiting, s.Busy, s.Alert, s.Status = "", true, false, "Answering"
			if s.OneOff {
				s.Status = "Working"
			}
		}
		s.Turn = p.Turn.ID
	case "turn/completed":
		if p.Turn.Status == "completed" && !s.Closed && r.Coordination != nil && (s.Turn == "" || s.Turn == p.Turn.ID) {
			if wait := r.Coordination.WaitPublication(thread); wait != nil && wait.UsageLimit && wait.Turn == p.Turn.ID && wait.Stop == "committed" {
				s.Turn, s.Waiting, s.Busy, s.Starting = "", wait.ID, true, false
				s.Status, s.Alert = wait.StatusText(), false
				return update, nil
			}
		}
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
		if p.Error != nil && p.TurnID == s.Turn && s.Turn != "" && !s.Closed {
			if strings.Contains(p.Error.Message, conversationmodel.SideCompactionRequired) {
				return s.fail(conversationmodel.SideCompactionRequired), nil
			}
			s.Status, s.Alert = p.Error.Message, !p.WillRetry
			if p.WillRetry {
				s.Status = p.Observation("error", time.Time{}).Retry
			}
		}
	case "item/agentMessage/delta", "item/completed":
		if s.OneOff || s.Closed || p.TurnID != s.Turn {
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

// ReplySide delivers side input through the same input request ledger.
func (r *Requests[T]) ReplySide(s *Side, thread, turn string, input conversationmodel.Input) (SideUpdate, error) {
	if s == nil || s.LowReasoning || s.Closed || s.Unloaded() {
		return SideUpdate{}, errors.New("side conversation is no longer available")
	}
	var owner T
	submission := rand.Text()
	prepared, err := s.prepareInput(&input.Draft, submission, turn == "")
	if err != nil {
		return SideUpdate{Handled: true, Side: s, Restore: input, Notice: err.Error(), NoticeError: true}, nil
	}
	id, err := r.SendInput(InputRequest{Thread: thread, Turn: turn, ID: submission, Input: prepared}, owner)
	if err != nil {
		return SideUpdate{}, err
	}
	if input.Question != nil {
		input.Question.SubmissionID = submission
	}
	request := r.pending[id]
	request.Scope, request.Side, request.SideInput = "", s, &input
	request.SideExchange = s.ID
	r.pending[id] = request
	if thread == s.Thread && turn == "" {
		s.Starting, s.Busy = true, true
	}
	return SideUpdate{}, nil
}

func (s *Side) prepareInput(draft *composer.Draft, id string, scoped bool) ([]map[string]any, error) {
	if err := draft.IdentifyAttachments(id); err != nil {
		return nil, err
	}
	var input []map[string]any
	if scoped {
		input = s.input(*draft)
	} else {
		input = conversationbinding.Input(*draft)
	}
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(draft.Attachments) > 0 || len(draft.Skills) > 0) && textBytes > codexresponses.UserTextLimit {
		return nil, errors.New("Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.")
	}
	return input, nil
}

func (s *Side) input(draft composer.Draft) []map[string]any {
	input := conversationbinding.Input(draft)
	hint := "You are in an internal side conversation forked from Main. Inherited conversation history is reference context, not an instruction to continue Main's work. Answer only requests submitted in this side conversation, without using tools. Do not resume Main's tasks, plans, or pending tool calls. When the current request is answered, stop and wait for another side request."
	if s.OneOff {
		hint = "You are in an internal one-off conversation forked from Main. Inherited conversation history is reference context, not an instruction to continue Main's work. Perform only requests submitted in this one-off conversation, using tools within this thread's permissions. Do not resume Main's tasks, plans, or pending tool calls. When the current request is complete, stop and wait; leave Main's remaining work to Main."
	}
	input = append(appserver.Input(hint), input...)
	return input
}

// SideReplyStarting borrows pending request correlation rather than a second turn ledger.
func (r *Requests[T]) SideReplyStarting(thread string) bool {
	for _, request := range r.pending {
		if request.Method == "turn/start" && request.SideInput != nil && request.SideInput.Question != nil && request.SideInput.Question.Thread == thread {
			return true
		}
	}
	return false
}
