package terminalsession

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	notice "github.com/yusing/mekugi/modules/notifications/presentation"
)

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
