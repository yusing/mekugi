package sessioncontrol

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestSideWorkflowWithoutRenderer(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "closed during fork"}[closed], func(t *testing.T) {
			answer := &conversationmodel.SideAnswer{Pending: conversationmodel.Input{Text: "question"}}
			side := &Side{SideAnswer: answer, Busy: true}
			transport := &requestTransport{id: "fork"}
			requests := NewRequests[struct{}](transport)
			observations := new(Observations)
			main := SideMain{Thread: "main", Blocked: true}
			if _, err := requests.StartSide(side, main, time.Time{}); err != nil || len(transport.sends) != 0 {
				t.Fatal("fork escaped Main's pending submission")
			}
			main.Blocked = false
			if _, err := requests.StartSide(side, main, time.Time{}); err != nil {
				t.Fatal(err)
			}
			requests.Retire("main")
			if closed {
				side.Close()
			}
			fork, found := requests.Take("fork")
			if !found || fork.Side != side || side.SideAnswer != answer {
				t.Fatal("side correlation or Conversation owner changed")
			}
			transport.id = "next"
			update, err := requests.ObserveSide(appserver.Message{Result: []byte(`{"thread":{"id":"side"}}`)}, fork, "replacement-main", observations)
			if err != nil || !update.Handled || !observations.IsSideThread("side") || update.Flush == closed {
				t.Fatalf("fork result: update=%+v err=%v", update, err)
			}
			if closed {
				if transport.sends[len(transport.sends)-1].method != "thread/unsubscribe" || answer.Pending.Text != "" {
					t.Fatal("closed fork sampled or retained its visible draft")
				}
				response, _ := requests.Take("next")
				if _, err := requests.ObserveSide(appserver.Message{}, response, "replacement-main", observations); err != nil || observations.IsSideThread("side") {
					t.Fatal("unsubscribe failed to retire side classification")
				}
				return
			}
			update, err = requests.StartSide(side, main, time.Time{})
			if err != nil || !update.TurnRequested || transport.sends[len(transport.sends)-1].method != "turn/start" {
				t.Fatal("classified side did not start through the existing ledger")
			}
			if _, err := requests.ObserveSide(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"side","turn":{"id":"turn","status":"completed"}}`)}, Request[struct{}]{}, "main", observations); err != nil {
				t.Fatal(err)
			}
			response, _ := requests.Take("next")
			if _, err := requests.ObserveSide(appserver.Message{Result: []byte(`{"turn":{"id":"turn"}}`)}, response, "main", observations); err != nil || side.Turn != "" || side.Busy {
				t.Fatal("late start acknowledgement revived completed side work")
			}
		})
	}
}
