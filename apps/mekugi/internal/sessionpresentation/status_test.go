package sessionpresentation_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	presentationcontract "github.com/yusing/mekugi/contracts/presentation"
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

func TestReplayPublicationWithoutControl(t *testing.T) {
	var service sessionpresentation.Service
	var store scope.Store
	var conversation conversationmodel.Session
	var feedback noticepresentation.Model
	m := service.Session(&store)
	m.Attach(sessionpresentation.Thread{ID: "recorded", Caller: "/root"}, true)
	now := time.Unix(100, 0)
	m.Status.Started = now.Add(-2 * time.Second)
	m.Status.Set("Working", false)
	conversation.Questions.Add(&prompt.Call{ID: "question", Thread: "recorded", Questions: []prompt.Question{{ID: "choice", Question: "Proceed?"}}}, true)
	in := sessionpresentation.MainInputs{RecordedTurn: "turn", Conversation: &conversation, Feedback: &feedback}
	v := m.Publish(in, sessionpresentation.Caption{}, now)
	if !v.Status.Session.Running || v.Status.Session.Elapsed != 2*time.Second || v.Questions.Count != 1 {
		t.Fatalf("recorded facts missing: %+v", v)
	}
	if v.Admission.Edit || v.Admission.Submit || v.InterruptAvailable || v.Interrupt != (sessioncontrol.InterruptTarget{}) || !reflect.DeepEqual(v.Pending, conversationmodel.PendingView{}) {
		t.Fatalf("playback published live admission: %+v", v)
	}
	in.RecordedTurn = ""
	m.Status.Complete("completed", nil, now)
	v = m.Publish(in, sessionpresentation.Caption{}, now.Add(time.Second))
	if v.Status.Session.Running || v.Status.Animating || v.InterruptAvailable {
		t.Fatalf("recorded completion retained live status: %+v", v.Status)
	}
}

func TestNonTerminalSurfaceScopeLifetime(t *testing.T) {
	c := newStatusConsumer()
	m := c.model()
	m.Menu().Begin(sessionpresentation.MenuCopy)
	m.OpenPanel("").Fields = []sessionpresentation.StatusField{{Value: "retained panel"}}
	m.OpenResume(false, "/work").Append([]sessionpresentation.ResumeRow{{ID: "saved"}}, "next")
	m.Attach(sessionpresentation.Thread{ID: "accepted", Caller: "/root"}, false)
	v := c.publish(sessionpresentation.Caption{})
	if v.Panel == nil || v.Panel.Fields[0].Value != "retained panel" || v.Resume == nil || v.Resume.Rows[0].ID != "saved" || m.Menu().Kind != sessionpresentation.MenuCopy {
		t.Fatal("host identity discovery discarded scoped presentation")
	}
	var replacement scope.Store
	fresh := c.service.Session(&replacement)
	fresh.Attach(sessionpresentation.Thread{ID: "replacement", Caller: "/root"}, false)
	v = fresh.Publish(sessionpresentation.MainInputs{Control: &c.control, Conversation: &c.conversation, Feedback: &c.feedback}, sessionpresentation.Caption{}, c.now)
	if v.Panel != nil || v.Resume != nil || fresh.Menu().Kind != "" || fresh.Menu().ID != 0 {
		t.Fatal("replacement inherited a retired surface")
	}
}

func TestNonTerminalPublicationRetainsDetachedCurrentValue(t *testing.T) {
	c := newStatusConsumer()
	call := &prompt.Call{ID: "call", Thread: "main", Questions: []prompt.Question{{
		ID: "question", Choices: []prompt.Option{{Label: "choice"}}, Answer: []string{"answer"}, Outcome: "answered",
	}}}
	c.conversation.Questions.Add(call, false)
	c.conversation.Questions.Open(call)
	c.conversation.Approvals.Add(&prompt.Approval{ID: "approval", Details: []string{"detail"}, Choices: []prompt.Choice{{Label: "allow"}}})
	c.conversation.Unsent = []conversationmodel.Input{{Text: "waiting"}}
	c.conversation.Queued = []conversationmodel.Input{{Text: "queued"}}
	c.conversation.Side = &conversationmodel.SideAnswer{Question: "side", Answer: []conversationmodel.Answer{{ID: "answer", Text: "original"}}}
	c.conversation.BTW = &conversationmodel.SideAnswer{Answer: []conversationmodel.Answer{{Text: "current"}}, History: []conversationmodel.SideExchange{{Question: "first", Answer: []conversationmodel.Answer{{Text: "earlier"}}}}}
	caption := sessionpresentation.Caption{Items: []presentationcontract.Item{{Text: "caption"}}}
	v := c.publish(caption)
	want := c.model().View()
	if !reflect.DeepEqual(v, want) || c.publish(caption).Version != want.Version {
		t.Fatal("publication was not retained or unchanged publication advanced its version")
	}
	v.Caption.Items[0].Text = "changed"
	v.Questions.Call.Questions[0].Choices[0].Label = "changed"
	v.Questions.Call.Questions[0].Answer[0] = "changed"
	v.Approvals.Approval.Details[0] = "changed"
	v.Approvals.Approval.Choices[0].Label = "changed"
	v.Pending.Waiting[0].Text = "changed"
	v.Pending.Queued[0].Text = "changed"
	v.Side.ID++
	v.BTW.Answer[0].Text = "changed"
	v.BTW.History[0].Answer[0].Text = "changed"
	c.conversation.BTW.Answer[0].Text = "changed by owner"
	c.conversation.BTW.History[0].Answer[0].Text = "changed by owner"
	c.conversation.Side.Answer[0].Text = "changed by owner"
	caption.Items[0].Text = "changed"
	c.model().Status.Set("new state", false)
	c.now = c.now.Add(time.Hour)
	for range 2 {
		if !reflect.DeepEqual(c.model().View(), want) {
			t.Fatal("a read, returned value, input value or unpublished owner change mutated the current publication")
		}
	}
	c.conversation.Questions.Hide()
	c.conversation.Editor.Files = []composer.Token{{Path: "source"}}
	history := []sessionpresentation.HistoryView{{Thread: "child", Status: sessionpresentation.Unloaded}}
	v = c.model().Publish(sessionpresentation.MainInputs{Control: &c.control, Feedback: &c.feedback, Conversation: &c.conversation, History: history}, caption, c.now)
	if v.Version == want.Version || v.Status.State.Text != "new state" {
		t.Fatal("changed owner facts were not published")
	}
	want = c.model().View()
	v.Draft.Draft.Files[0].Path = "changed"
	v.History.Entries[0].Thread = "changed"
	history[0].Thread = "changed again"
	if !reflect.DeepEqual(c.model().View(), want) {
		t.Fatal("draft or history publication aliases mutable storage")
	}
	c.conversation.Side = nil
	if c.publish(caption).Side != nil {
		t.Fatal("closed side remains in the current publication")
	}
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
