package sessioncontrol

import (
	"context"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestArchiveSidesDrainsClosedNativeThread(t *testing.T) {
	transport := &requestTransport{id: "archive"}
	requests := NewRequests[struct{}](transport)
	side := &Side{SideAnswer: new(conversationmodel.SideAnswer), Thread: "side", Unloaded: true}
	requests.TrackSide(side.Thread, side)
	messages := make(chan appserver.Message, 1)
	messages <- appserver.Message{ID: []byte("archive"), Result: []byte(`{}`)}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := requests.ArchiveSides(ctx, messages, new(Observations), side); err != nil {
		t.Fatal(err)
	}
	if len(transport.sends) != 1 || transport.sends[0].method != "thread/archive" || !side.Archived || requests.HasSides() {
		t.Fatalf("closed native history was not archived once: %+v %+v", transport.sends, side)
	}
}

func TestArchiveSidesDrainsDismissedPendingFork(t *testing.T) {
	transport := &requestTransport{id: "fork"}
	requests := NewRequests[struct{}](transport)
	side := &Side{SideAnswer: &conversationmodel.SideAnswer{Pending: conversationmodel.Input{Text: "question"}}, Busy: true}
	if _, err := requests.StartSide(side, SideMain{Thread: "main"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	side.Close()
	if err := requests.ReleaseSide(side); err != nil {
		t.Fatal(err)
	}
	// The dismissed slot is gone; only the pending request owns its delayed fork.
	transport.id = "archive"
	messages := make(chan appserver.Message, 2)
	messages <- appserver.Message{ID: []byte("fork"), Result: []byte(`{"thread":{"id":"side"}}`)}
	messages <- appserver.Message{ID: []byte("archive"), Result: []byte(`{}`)}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := requests.ArchiveSides(ctx, messages, new(Observations)); err != nil {
		t.Fatal(err)
	}
	if !side.Archived || requests.HasSides() || len(transport.sends) != 2 || transport.sends[1].method != "thread/archive" {
		t.Fatalf("dismissed fork survived cleanup: %+v %+v", side, transport.sends)
	}
}

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
				if transport.sends[len(transport.sends)-1].method != "thread/archive" || answer.Pending.Text != "" {
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

func TestSideAttachmentSubmissionIdentity(t *testing.T) {
	body := "immutable side snapshot\n"
	envelope := attachments.Encode(composer.FileFrames("/workspace/side.txt", body))
	var previous string
	for range 2 {
		side := &Side{SideAnswer: &conversationmodel.SideAnswer{Pending: conversationmodel.Input{Text: "question", Attachments: []string{envelope}}}, Thread: "side", Busy: true}
		transport := &requestTransport{id: "start"}
		requests := NewRequests[struct{}](transport)
		update, err := requests.StartSide(side, SideMain{Thread: "main"}, time.Time{})
		if err != nil || !update.TurnRequested {
			t.Fatalf("side start: %+v %v", update, err)
		}
		params := transport.sends[0].params.(map[string]any)
		input := params["input"].([]map[string]any)
		snapshot, ok := attachments.DecodeSnapshot(input[len(input)-1]["text"].(string))
		if !ok || snapshot.ID == "" || snapshot.ID == previous || snapshot.Frames[0] != composer.FileFrames("/workspace/side.txt", body)[0] {
			t.Fatal("side submission lost unique identity or original contents")
		}
		previous = snapshot.ID
	}
}
