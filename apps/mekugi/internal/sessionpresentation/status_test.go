package sessionpresentation_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
)

// statusConsumer consumes the same detached publication a nonterminal frontend
// receives, with operation and prompt state supplied by their existing owners.
type statusConsumer struct {
	service      sessionpresentation.Service
	store        scope.Store
	control      sessioncontrol.Live
	feedback     noticepresentation.Model
	conversation conversationmodel.Session
	now          time.Time
}

func newStatusConsumer() *statusConsumer {
	c := &statusConsumer{now: time.Unix(100, 0)}
	c.control.Thread = "main"
	c.conversation.Editor.Text = "!command"
	c.conversation.PromptEditor.Parked.Snapshot.Text = "draft"
	m := c.model()
	m.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, false)
	m.Status.Set("Working", false)
	m.Status.Started = c.now
	c.control.Started("turn")
	return c
}

func (c *statusConsumer) model() *sessionpresentation.Model { return c.service.Session(&c.store) }
func (c *statusConsumer) publish(caption sessionpresentation.Caption) sessionpresentation.View {
	return c.model().Publish(sessionpresentation.MainInputs{
		Control: &c.control, Feedback: &c.feedback, Conversation: &c.conversation, Processes: 2,
	}, caption, c.now)
}

func TestNonTerminalStatusTimingAndDetachedDetails(t *testing.T) {
	c := newStatusConsumer()
	c.now = c.now.Add(7 * time.Second)
	running := c.publish(sessionpresentation.Caption{Model: "model", Effort: "high", ContextKnown: true, ContextTokens: 123, ContextWindow: 1000})
	if s := running.Status.Session; !s.Running || !s.ElapsedKnown || s.Elapsed != 7*time.Second || s.Animation != sessionpresentation.Working {
		t.Fatalf("running timing: %+v", s)
	}
	c.model().Status.Complete("completed", nil, c.now)
	c.control.Completed(true)
	c.now = c.now.Add(time.Hour)
	done := c.publish(sessionpresentation.Caption{})
	if s := done.Status.Session; s.Running || !s.CompletionKnown || s.Completion != 7*time.Second || s.Text != "Completed" {
		t.Fatalf("completion changed after finish: %+v", s)
	}
	failure := "provider failed\n" + strings.Repeat("full detail ", 30)
	c.model().Status.Complete("failed", &failure, c.now)
	c.feedback.Set(failure, true, c.now)
	failed := c.publish(sessionpresentation.Caption{})
	if !failed.Status.Session.Alert || failed.Status.Session.Failure != failure || failed.Status.Session.CompletionKnown || failed.Status.Feedback.Text != failure || !failed.Status.Feedback.Alert {
		t.Fatalf("failure detail lost: %+v", failed.Status)
	}
	c.model().Status.Set("Ready", false)
	c.feedback.Set("changed", false, c.now)
	if running.Status.Session.Text != "Working" || running.Caption.Model != "model" || running.Caption.ContextTokens != 123 || failed.Status.Feedback.Text != failure || done.Status.Session.Text != "Completed" {
		t.Fatal("later owner changes mutated an earlier publication")
	}
}

func TestNonTerminalStatusPromptsAndDisplayedCaption(t *testing.T) {
	c := newStatusConsumer()
	call := &prompt.Call{ID: "question", Thread: "main", Turn: "turn", Sync: true, Questions: []prompt.Question{{ID: "q", Question: "Keep all details?", Note: true}}}
	if !c.conversation.Questions.Add(call, false) || !c.conversation.Questions.Open(call) {
		t.Fatal("question admission failed")
	}
	c.conversation.Approvals.Add(&prompt.Approval{ID: "approval", Details: []string{"full approval details"}})
	if !c.conversation.Approvals.Show() {
		t.Fatal("approval admission failed")
	}
	v := c.publish(sessionpresentation.Caption{Model: "main-model"})
	if v.Status.Composer.Text != "approving · turn waiting" || v.Status.Session.Text != "Working" {
		t.Fatalf("approval precedence: %+v", v.Status)
	}
	c.conversation.Approvals.Hide()
	if s := c.publish(sessionpresentation.Caption{}).Status; s.Composer.Text != "answering · note · turn waiting · draft kept" || s.ShellMode || s.Processes != 2 {
		t.Fatalf("question status: %+v", s)
	}
	m := c.model()
	child := sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}
	if err := m.Display(sessionpresentation.DisplayIntent{Version: m.View().Version, Thread: child}); err != nil {
		t.Fatal(err)
	}
	v = c.publish(sessionpresentation.Caption{})
	if v.ReadOnly != sessionpresentation.ChildDisplayed || v.Status.Composer.Text != "Read-only" || !reflect.DeepEqual(v.Caption, sessionpresentation.Caption{}) || v.Input.ID != "main" {
		t.Fatalf("child inherited Main caption or input status: %+v", v)
	}
	if err := m.Display(sessionpresentation.DisplayIntent{Version: m.View().Version, Thread: m.View().Main}); err != nil {
		t.Fatal(err)
	}
	v = c.publish(sessionpresentation.Caption{Model: "main-model"})
	if v.ReadOnly != sessionpresentation.Editable || v.Caption.Model != "main-model" || v.Status.Composer.Text != "answering · note · turn waiting · draft kept" || c.conversation.Questions.Current().Question != "Keep all details?" || c.conversation.Approvals.View().Approval.Details[0] != "full approval details" {
		t.Fatalf("return to Main lost owner state: %+v", v)
	}
	c.conversation.Questions.Current().Question = "changed"
	c.conversation.Approvals.Pending[0].Details[0] = "changed"
	if v.Questions.Call.Questions[0].Text != "Keep all details?" || v.Approvals.Approval.Details[0] != "full approval details" {
		t.Fatal("prompt publication aliases its owners")
	}
}

func TestNonTerminalPendingInputOrderAndDetachment(t *testing.T) {
	c := newStatusConsumer()
	input := func(text string) conversationmodel.Input {
		return conversationmodel.Input{Draft: composer.Draft{Text: text}}
	}
	c.conversation.Stage("first", conversationmodel.Submission{Parts: []conversationmodel.Input{input("acknowledged steer")}})
	c.control.Submit("first", "turn")
	c.control.AcknowledgeInput(false, false, false)
	c.conversation.Stage("second", conversationmodel.Submission{Parts: []conversationmodel.Input{input("awaiting acknowledgement")}})
	c.control.Submit("second", "turn")
	c.conversation.Unsent = []conversationmodel.Input{input("unsent")}
	c.conversation.Queued = []conversationmodel.Input{input("next turn")}
	v := c.publish(sessionpresentation.Caption{})
	want := conversationmodel.PendingView{
		Reason:  conversationmodel.Steering,
		Waiting: []conversationmodel.PendingText{{Text: "acknowledged steer"}, {Text: "awaiting acknowledgement"}, {Text: "unsent"}},
		Queued:  []conversationmodel.PendingText{{Text: "next turn"}},
	}
	if !reflect.DeepEqual(v.Pending, want) || !v.Status.ShellMode {
		t.Fatalf("pending publication: %+v", v)
	}
	c.control.CommitInput("first")
	c.conversation.Submitted["second"].Parts[0].Text = "changed"
	c.conversation.Unsent[0].Text, c.conversation.Queued[0].Text = "changed", "changed"
	c.control.Shell.Begin("")
	if next := c.publish(sessionpresentation.Caption{}).Pending; len(next.Waiting) != 2 || next.Reason != conversationmodel.ShellRunning {
		t.Fatalf("control changes not reflected: %+v", next)
	}
	if !reflect.DeepEqual(v.Pending, want) {
		t.Fatal("pending publication aliases its owners")
	}
}

func TestNonTerminalStatusProgressCorrelationAndPrecedence(t *testing.T) {
	c := newStatusConsumer()
	m := c.model()
	assertText := func(want string) {
		t.Helper()
		if got := c.publish(sessionpresentation.Caption{}).Status.Session.Text; got != want {
			t.Fatalf("status = %q, want %q", got, want)
		}
	}
	progress := func(kind observation.Kind, thread, turn, item, itemType, process string, completed bool) {
		m.ObserveProgress(observation.Event{Kind: kind, ThreadID: thread, TurnID: turn, Item: observation.Item{ID: item, Type: itemType, ProcessID: process}}, c.control.Turn, completed)
	}
	m.TerminalInteraction("child", "turn", "turn", "process", "")
	m.TerminalInteraction("main", "old", "turn", "process", "")
	progress(observation.ItemStarted, "child", "turn", "compact", "contextCompaction", "", false)
	progress(observation.ItemStarted, "main", "old", "compact", "contextCompaction", "", false)
	assertText("Working")
	m.TerminalInteraction("main", "turn", "turn", "process", "")
	assertText("Still running")
	m.TerminalInteraction("main", "turn", "turn", "other", "input")
	progress(observation.ItemCompleted, "main", "turn", "command", "commandExecution", "other", false)
	assertText("Still running")
	progress(observation.ItemCompleted, "main", "turn", "command", "commandExecution", "process", false)
	assertText("Working")
	m.TerminalInteraction("main", "turn", "turn", "process", "")
	progress(observation.ItemStarted, "main", "turn", "compact", "contextCompaction", "", false)
	assertText("Compacting context")
	progress(observation.ItemCompleted, "main", "turn", "other", "contextCompaction", "", false)
	progress(observation.TurnCompleted, "main", "old", "", "", "", false)
	assertText("Compacting context")
	c.control.Shell.Begin("")
	assertText("Running shell")
	c.control.Shell.Identified()
	c.control.Completed(true)
	c.control.Started("turn")
	progress(observation.ItemCompleted, "main", "turn", "compact", "contextCompaction", "", false)
	progress(observation.ItemStarted, "main", "turn", "compact", "contextCompaction", "", true)
	assertText("Working")
	m.TerminalInteraction("main", "turn", "turn", "process", "")
	progress(observation.AgentDelta, "main", "old", "", "", "", false)
	assertText("Still running")
	progress(observation.ReasoningDelta, "main", "turn", "", "", "", false)
	assertText("Working")
}

func TestNonTerminalStatusReplacementRetiresProgress(t *testing.T) {
	c := newStatusConsumer()
	m := c.model()
	m.ObserveProgress(observation.Event{Kind: observation.ItemStarted, ThreadID: "main", TurnID: "turn", Item: observation.Item{ID: "compact", Type: "contextCompaction"}}, "turn", false)
	c.control.Replacement.Archive()
	if s := c.publish(sessionpresentation.Caption{}).Status.Session; s.Text != "Archiving session…" || s.Animation != sessionpresentation.Pulse {
		t.Fatalf("archive precedence: %+v", s)
	}
	m.Attach(sessionpresentation.Thread{ID: "replacement", Caller: "/root"}, false)
	c.control.LeaveThread()
	c.control.Thread = "replacement"
	c.control.Started("next")
	m.Status.Set("Working", false)
	if s := c.publish(sessionpresentation.Caption{}).Status.Session; s.Text != "Working" || s.ElapsedKnown || s.CompletionKnown || s.Failure != "" {
		t.Fatalf("replacement retained previous status: %+v", s)
	}
}
