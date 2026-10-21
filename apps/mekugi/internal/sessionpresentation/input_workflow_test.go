package sessionpresentation_test

import (
	"encoding/json/jsontext"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

type inputTransport struct {
	method   string
	params   map[string]any
	sequence int
	err      error
}

func (t *inputTransport) Initialize() (string, error)       { return "initialize", nil }
func (t *inputTransport) Respond(jsontext.Value, any) error { return nil }
func (t *inputTransport) Send(method string, params any, _ bool) (string, error) {
	t.sequence++
	t.method, t.params = method, params.(map[string]any)
	return strconv.Itoa(t.sequence), t.err
}

func TestInputWorkflowWithoutRenderer(t *testing.T) {
	service := moduletest.New(t, conversation.Module, conversation.Deps{}, module.Slots{}, new(moduletest.Scripted))
	var store scope.Store
	c := service.Session(&store)
	var presentation sessionpresentation.Service
	m := presentation.Session(&store)
	m.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, false)
	control := sessioncontrol.Live{Thread: "main"}
	transport := new(inputTransport)
	requests := sessioncontrol.NewRequests[string](transport)
	dispatch := func(id string) string {
		t.Helper()
		next := m.NextInput(&control, c, false)
		if len(next.Batch.Parts) == 0 || next.Interrupt {
			t.Fatal("eligible input was not selected")
		}
		submission := conversationmodel.Submission{Draft: conversationmodel.Join(next.Batch.Parts...).Draft, Parts: next.Batch.Parts}
		m.StageInput(&control, c, id, submission, next.Steer)
		turn := ""
		if next.Steer {
			turn = control.Turn
		}
		requestID, err := requests.SendInput(sessioncontrol.InputRequest{Thread: control.Thread, Turn: turn, ID: id, Tier: "priority", Input: conversationbinding.Input(submission.Draft)}, "input")
		if err != nil {
			t.Fatal(err)
		}
		return requestID
	}
	c.Editor.Replace(composer.Draft{Text: "first"})
	draft, plan, err := m.TakeInput(sessionpresentation.SubmitIntent{Version: m.View().Version, Revision: c.Editor.Revision(), Mode: sessioncontrol.SendInput}, &control, c, false)
	if err != nil {
		t.Fatal(err)
	}
	c.QueueInput(conversationmodel.Input{Draft: draft}, plan.Queued)
	requestID := dispatch("first-input")
	if transport.method != "turn/start" || transport.params["serviceTier"] != "priority" || transport.params["clientUserMessageId"] != "first-input" {
		t.Fatalf("start: %s %+v", transport.method, transport.params)
	}
	if next := m.NextInput(&control, c, false); len(next.Batch.Parts) != 0 {
		t.Fatal("pending acknowledgement admitted another dispatch")
	}
	control.Started("turn-1")
	m.CommitInput(&control, c, "first-input", "")
	if len(c.Submitted) != 1 {
		t.Fatal("commitment discarded payload before acknowledgement")
	}
	response, ok := requests.Take(requestID)
	if !ok || response.Owner != "input" {
		t.Fatal("input bypassed the request ledger")
	}
	m.AcknowledgeInput(&control, c, response.Method, "", false)
	if len(c.Submitted) != 0 {
		t.Fatal("committed and acknowledged payload remains pending")
	}

	c.QueueInput(conversationmodel.Input{Text: "steer"}, false)
	requestID = dispatch("steer-input")
	if transport.method != "turn/steer" || transport.params["expectedTurnId"] != "turn-1" {
		t.Fatalf("steer: %s %+v", transport.method, transport.params)
	}
	if _, exists := transport.params["serviceTier"]; exists {
		t.Fatal("steer changed the selected tier")
	}
	c.QueueInput(conversationmodel.Input{Text: "next turn"}, true)
	c.Editor.Replace(composer.Draft{Text: "new draft"})
	if effects := m.SettleInput(&control, c, "turn-1", true); len(effects) != 0 {
		t.Fatal("turn completion guessed an unacknowledged steer outcome")
	}
	control.Completed(false)
	response, _ = requests.Take(requestID)
	effects := m.AcknowledgeInput(&control, c, response.Method, "", false)
	if !effects.Restored.Changed || c.Editor.Text != "steer\nnext turn\nnew draft" || len(c.Submitted) != 0 || len(c.Queued) != 0 {
		t.Fatalf("late acknowledgement lost restoration: %+v draft=%q", effects, c.Editor.Text)
	}
	if _, ok := requests.Take(requestID); ok {
		t.Fatal("duplicate response can settle input twice")
	}
}

func TestInputStartExpeditePreservesWaiting(t *testing.T) {
	for _, empty := range []bool{true, false} {
		name := "ordinary"
		if empty {
			name = "empty-continuation"
		}
		t.Run(name, func(t *testing.T) {
			service := moduletest.New(t, conversation.Module, conversation.Deps{}, module.Slots{}, new(moduletest.Scripted))
			var store scope.Store
			c := service.Session(&store)
			var presentation sessionpresentation.Service
			m := presentation.Session(&store)
			m.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, false)
			control := sessioncontrol.Live{Thread: "main"}
			var start conversationmodel.Submission
			if !empty {
				part := conversationmodel.Input{Text: "work"}
				start = conversationmodel.Submission{Draft: part.Draft, Parts: []conversationmodel.Input{part}}
			}
			m.StageInput(&control, c, "start", start, false)
			control.Started("continued")
			if !empty {
				m.CommitInput(&control, c, "start", "work")
			}
			// Empty continuation has no host userMessage commitment.
			m.AcknowledgeInput(&control, c, "turn/start", "", false)
			part := conversationmodel.Input{Text: "steered"}
			m.StageInput(&control, c, "steer", conversationmodel.Submission{Draft: part.Draft, Parts: []conversationmodel.Input{part}}, true)
			c.Unsent = []conversationmodel.Input{{Text: "waiting"}}
			control.RequestInterrupt(sessioncontrol.ExpediteSteers, true)
			control.Completed(false)
			m.SettleInput(&control, c, "continued", true)
			m.AcknowledgeInput(&control, c, "turn/steer", "interrupted", true)
			if len(c.Unsent) != 2 || c.Unsent[0].Text != "steered" || c.Unsent[1].Text != "waiting" {
				t.Fatalf("expedited waiting input was restored instead: unsent=%+v draft=%q pendingStart=%q", c.Unsent, c.Editor.Text, control.PendingStart())
			}
		})
	}
}

func TestInputStartInterruptedBeforeAcknowledgement(t *testing.T) {
	for _, name := range []string{"empty-restore", "empty-expedite", "ordinary-restore", "ordinary-expedite"} {
		t.Run(name, func(t *testing.T) {
			service := moduletest.New(t, conversation.Module, conversation.Deps{}, module.Slots{}, new(moduletest.Scripted))
			var store scope.Store
			c := service.Session(&store)
			var presentation sessionpresentation.Service
			m := presentation.Session(&store)
			m.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, false)
			control := sessioncontrol.Live{Thread: "main"}
			empty := strings.HasPrefix(name, "empty-")
			var start conversationmodel.Submission
			if !empty {
				part := conversationmodel.Input{Text: "work"}
				start = conversationmodel.Submission{Draft: part.Draft, Parts: []conversationmodel.Input{part}}
			}
			m.StageInput(&control, c, "start", start, false)
			control.Started("continued")
			if !empty {
				m.CommitInput(&control, c, "start", "work")
			}
			c.Unsent = []conversationmodel.Input{{Text: "waiting"}}
			expedite := strings.HasSuffix(name, "-expedite")
			mode := sessioncontrol.RestoreWaiting
			if expedite {
				mode = sessioncontrol.ExpediteSteers
			}
			control.RequestInterrupt(mode, true)
			control.InterruptResponse("steer/interrupt", false)
			control.Completed(false)
			m.SettleInput(&control, c, "continued", true)
			m.AcknowledgeInput(&control, c, "turn/start", "", false)
			if !expedite {
				if len(c.Unsent) != 0 || c.Editor.Text != "waiting" {
					t.Fatalf("interrupted waiting input remains eligible: unsent=%+v draft=%q", c.Unsent, c.Editor.Text)
				}
				return
			}
			next := m.NextInput(&control, c, false)
			if len(next.Batch.Parts) != 1 || next.Batch.Parts[0].Text != "waiting" {
				t.Fatal("waiting input was not selected")
			}
			m.StageInput(&control, c, "next", conversationmodel.Submission{Draft: next.Batch.Parts[0].Draft, Parts: next.Batch.Parts}, false)
			control.Started("next-turn")
			m.CommitInput(&control, c, "next", "waiting")
			m.AcknowledgeInput(&control, c, "turn/start", "", false)
			if !control.PrepareEscape(true) {
				t.Fatal("completed continuation left expedite state blocking Escape in next turn")
			}
		})
	}
}
