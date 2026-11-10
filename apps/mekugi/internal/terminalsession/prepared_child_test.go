package terminalsession

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	notice "github.com/yusing/mekugi/modules/notifications/presentation"
)

func TestFrontendHeaderDisplayedFacts(t *testing.T) {
	root := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	child := sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}
	side := sessionpresentation.Thread{ID: "side", Caller: "/root/side"}
	main := Main{session: sessionpresentation.View{Main: root, Displayed: root}}
	main.session.Info.Title = "A long\n session title"
	main.session.Info.Skills = []string{"commit", "plane"}
	main.session.Questions.Count = 2
	transcript, agents := activityui.NewView(), activityui.NewView()
	transcript.Offset, transcript.FeedLines, transcript.FeedRows, transcript.Unseen = 10, 100, 20, 3
	transcript.Following = false
	agents.Offset, agents.FeedLines, agents.FeedRows = 4, 40, 10
	agents.Following = true
	in := FrameInput{Main: &main, Transcript: transcript, Agents: agents, Shell: &Shell{Focus: 2}}
	want := sessionpresentation.FrontendHeader{Displayed: root, Name: "Main", Title: "A long session title", Skills: []string{"commit", "plane"}, Questions: 2, Above: 10, Below: 70, Unseen: 3}
	if got := in.Header(); !reflect.DeepEqual(got, want) {
		t.Fatalf("main header: got %+v, want %+v", got, want)
	}
	// Published skill slices must not borrow mutable renderer input.
	got := in.Header()
	got.Skills[0] = "changed"
	if main.session.Info.Skills[0] != "commit" {
		t.Fatal("header retained the renderer skill slice")
	}
	main.session.Displayed = child
	got = in.Header()
	if got.Displayed != child || got.Name != "Thread" || got.Detail == "" || got.Title != "" || len(got.Skills) != 0 || got.Questions != 0 || got.Above != 4 || got.Below != 0 || got.Unseen != 0 {
		t.Fatalf("child header: %+v", got)
	}
	main.session.SideThread, main.session.Displayed = side, side
	main.session.Side = &sessionpresentation.SideToken{Label: "One-off"}
	got = in.Header()
	if got.Name != "One-off" || got.Detail != "" || got.Displayed != side {
		t.Fatalf("side header: %+v", got)
	}
	main.session.Main, main.session.Displayed, main.session.Info.Caller = child, child, child.Caller
	got = in.Header()
	if got.Name != "Thread" || got.Detail == "" || len(got.Skills) != 0 || got.Title != "A long session title" {
		t.Fatalf("prepared child header: %+v", got)
	}
}

func TestPreparedChildHumanControls(t *testing.T) {
	var model sessionpresentation.Model
	model.Attach(sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}, false)
	model.RestrictChildInput(true)
	var conv conversation.Session
	call := &prompt.Call{ID: "ask", Thread: "child", Sync: true, Questions: []prompt.Question{{ID: "q", Question: "VISIBLE_CHILD_QUESTION"}}}
	conv.Questions.Add(call, false)
	conv.Questions.Open(call)
	var live sessioncontrol.Live
	live.Thread = "child"
	live.Started("turn")
	v := model.Publish(sessionpresentation.MainInputs{Control: &live, Feedback: &notice.Model{}, Conversation: &conv}, sessionpresentation.Caption{})
	var main Main
	main.Update(v)
	transcript := activityui.NewView()
	prompts := &Prompts{}
	t.Run("render", func(t *testing.T) {
		frame := main.Render(transcript, &transcript.Painter, prompts, MainOptions{Focused: true}, 100, 24, 0)
		if !strings.Contains(strings.Join(frame.Rows, "\n"), "VISIBLE_CHILD_QUESTION") {
			t.Fatal("prepared child published an open question but renderer hid its dock")
		}
	})
	t.Run("escape", func(t *testing.T) {
		if !v.InterruptAvailable {
			t.Fatal("fixture did not publish interrupt availability")
		}
		in := FrameInput{Main: &main, Transcript: transcript, Shell: &Shell{}, Prompts: prompts}
		var state Interaction
		for effect := range in.Dispatch(&state, Input{Kind: KeyInput, Text: "\x1b"}) {
			if effect.Interrupt != nil {
				return
			}
		}
		t.Fatal("prepared child swallowed Escape before issuing available interrupt")
	})
	t.Run("ordinary input", func(t *testing.T) {
		v.Questions.Call, v.Draft.Available = nil, true
		main.Update(v)
		if main.PrepareInput(true).Editable {
			t.Fatal("prepared child exposed ordinary editing")
		}
		v.Admission.ReadOnly = false
		main.Update(v)
		if !main.PrepareInput(true).Editable {
			t.Fatal("transient prompt admission blocked an ordinary root draft")
		}
	})
}
