package sessionpresentation_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

func TestInterruptResolvesOriginalWorkThroughLifecycle(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit-before-ack", true: "ack-before-start"}[ackFirst], func(t *testing.T) {
			c := newStatusConsumer()
			c.control.Completed(true)
			m := c.model()
			var attached sessionpresentation.Attachment
			attached.Start(c.control.Thread)
			var text transcript.State
			var waits activity.Waits
			var agent activity.AgentLifecycle
			observe := func(kind observation.Kind, turn string) {
				t.Helper()
				result := attached.ObserveTurn(sessionpresentation.TurnModels{
					Transcript: &text, Waits: &waits, Agent: &agent,
					Selected: c.control.Turn, Start: c.control.InterruptTarget().Start,
				}, sessionpresentation.TurnObservation{Kind: kind, Thread: c.control.Thread, Turn: turn, Status: "completed", At: c.now})
				if !result.Admitted {
					t.Fatal("turn observation was rejected")
				}
			}
			capture := func() sessionpresentation.InterruptIntent {
				v := c.publish(sessionpresentation.Caption{})
				return sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt}
			}
			part := conversationmodel.Input{Text: "original"}
			m.StageInput(&c.control, &c.conversation, "original-input", conversationmodel.Submission{Draft: part.Draft, Parts: []conversationmodel.Input{part}}, false)
			pending := capture()
			if ackFirst {
				m.AcknowledgeInput(&c.control, &c.conversation, "turn/start", "", false)
			}
			observe(observation.TurnStarted, "original-turn")
			c.control.Started("original-turn")
			started := capture()
			m.CommitInput(&c.control, &c.conversation, "original-input", "")
			if !ackFirst {
				m.AcknowledgeInput(&c.control, &c.conversation, "turn/start", "", false)
			}
			if c.control.PendingStart() != "" || len(c.conversation.Submitted) != 0 {
				t.Fatal("settled input retained the commitment correlation")
			}
			committed := capture()
			if pending.Target.Start != "original-input" || committed.Target.Start != pending.Target.Start {
				t.Fatal("commitment replaced the original interrupt identity")
			}
			if err := m.Display(sessionpresentation.DisplayIntent{Version: m.View().Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}}); err != nil {
				t.Fatal(err)
			}
			m.Status.SetText("progress")
			c.publish(sessionpresentation.Caption{})
			intents := []sessionpresentation.InterruptIntent{pending, started, committed}
			for _, intent := range intents {
				if active, err := m.ResolveInterrupt(intent, &c.control, &attached); !active || err != nil {
					t.Fatalf("original active work rejected: %+v, %v, %v", intent.Target, active, err)
				}
			}
			request := c.control.RequestInterrupt(pending.Mode, false)
			if request.Turn != "original-turn" {
				t.Fatalf("admitted action did not interrupt its real turn: %+v", request)
			}
			observe(observation.TurnCompleted, "original-turn")
			if active, err := m.ResolveInterrupt(pending, &c.control, &attached); active || err != nil {
				t.Fatalf("observed completion did not settle original work: %v, %v", active, err)
			}
			c.control.Completed(true)
			m.SettleInput(&c.control, &c.conversation, "original-turn", false)
			m.StageInput(&c.control, &c.conversation, "successor-input", conversationmodel.Submission{}, false)
			observe(observation.TurnStarted, "successor-turn")
			c.control.Started("successor-turn")
			c.conversation.Unsent = []conversationmodel.Input{{Text: "successor waiting input"}}
			c.publish(sessionpresentation.Caption{})
			before := c.control
			for _, intent := range intents {
				if active, err := m.ResolveInterrupt(intent, &c.control, &attached); active || err != nil {
					t.Fatalf("closed original work did not settle: %+v, %v, %v", intent.Target, active, err)
				}
			}
			forged := committed
			forged.Target.Start = "different-start"
			if active, err := m.ResolveInterrupt(forged, &c.control, &attached); active || !errors.Is(err, sessionpresentation.ErrInterruptUnavailable) {
				t.Fatalf("closed turn accepted a foreign start: %v, %v", active, err)
			}
			if !reflect.DeepEqual(before, c.control) || len(c.conversation.Unsent) != 1 {
				t.Fatal("settlement mutated successor work or waiting input")
			}
		})
	}
}

func TestInterruptRejectsUnavailableOrForeignWork(t *testing.T) {
	c := newStatusConsumer()
	c.control.Completed(true)
	m := c.model()
	var attached sessionpresentation.Attachment
	attached.Start(c.control.Thread)
	c.control.Submit("rejected", "")
	v := c.publish(sessionpresentation.Caption{})
	intent := sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt}
	c.control.SubmissionFailed()
	c.control.Submit("successor", "")
	c.publish(sessionpresentation.Caption{})
	if active, err := m.ResolveInterrupt(intent, &c.control, &attached); active || !errors.Is(err, sessionpresentation.ErrInterruptUnavailable) {
		t.Fatalf("rejected start was confused with its successor or proven completion: %v, %v", active, err)
	}
	intent.Version, intent.Target = m.View().Version, c.control.InterruptTarget()
	for _, name := range []string{"thread", "attachment", "future", "mode", "unknown-start"} {
		t.Run(name, func(t *testing.T) {
			invalid := intent
			switch name {
			case "thread":
				invalid.Target.Thread = "foreign"
			case "attachment":
				invalid.Version.Attachment++
			case "future":
				invalid.Version.Revision++
			case "mode":
				invalid.Mode = sessioncontrol.InterruptMode(255)
			case "unknown-start":
				invalid.Target.Start = "unknown"
			}
			if active, err := m.ResolveInterrupt(invalid, &c.control, &attached); active || err == nil {
				t.Fatalf("invalid action admitted: %v, %v", active, err)
			}
		})
	}
	m.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, true)
	intent.Version = m.View().Version
	if active, err := m.ResolveInterrupt(intent, &c.control, &attached); active || !errors.Is(err, sessionpresentation.ErrReplayIntent) {
		t.Fatalf("replay action admitted: %v, %v", active, err)
	}
	if c.control.Interruption.BeforeStart || c.control.Interruption.Target != "" || c.control.SendSteersAfterInterrupt {
		t.Fatal("rejected action changed successor interruption state")
	}
}

func TestInterruptRetainsLogicalWorkAfterHostCompletion(t *testing.T) {
	for _, next := range []string{"same-wait", "successor-wait", "successor-stop", "successor-start", "successor-turn"} {
		t.Run(next, func(t *testing.T) {
			c := newStatusConsumer()
			c.control.Completed(true)
			c.control.Submit("original-input", "")
			c.control.Started("original-turn")
			c.control.Waiting, c.control.LocalStop = "original-wait", "original-turn"
			var attached sessionpresentation.Attachment
			attached.Start(c.control.Thread)
			var text transcript.State
			var waits activity.Waits
			var agent activity.AgentLifecycle
			models := sessionpresentation.TurnModels{Transcript: &text, Waits: &waits, Agent: &agent, Selected: c.control.Turn, Start: c.control.InterruptTarget().Start}
			attached.ObserveTurn(models, sessionpresentation.TurnObservation{Kind: observation.TurnStarted, Thread: c.control.Thread, Turn: c.control.Turn, At: c.now})
			v := c.publish(sessionpresentation.Caption{})
			intent := sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt}
			attached.ObserveTurn(models, sessionpresentation.TurnObservation{Kind: observation.TurnCompleted, Thread: c.control.Thread, Turn: c.control.Turn, Status: "completed", At: c.now})
			c.control.Completed(true)
			if next != "same-wait" {
				v = c.publish(sessionpresentation.Caption{})
				intent = sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt}
			}
			switch next {
			case "successor-wait":
				c.control.Waiting = "new-wait"
			case "successor-stop":
				c.control.LocalStop = "new-stop"
			case "successor-start":
				c.control.Submit("new-input", "")
			case "successor-turn":
				c.control.Started("new-turn")
			}
			active, err := c.model().ResolveInterrupt(intent, &c.control, &attached)
			if next == "same-wait" {
				if !active || err != nil {
					t.Fatalf("host completion settled logical work: active=%v err=%v", active, err)
				}
			} else if active || !errors.Is(err, sessionpresentation.ErrInterruptUnavailable) {
				t.Fatalf("original cancellation reached successor: active=%v err=%v", active, err)
			}
		})
	}
}
