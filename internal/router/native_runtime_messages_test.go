package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeMessagesClient struct {
	*runtimeInputTestClient
	messages []session.AgentMessage
	err      error
}

func (c *runtimeMessagesClient) SendAgentMessage(_ context.Context, m session.AgentMessage) error {
	c.messages = append(c.messages, m)
	return c.err
}

func runtimeMessagesUI(t *testing.T) (*appServerUI, *runtimeMessagesClient) {
	t.Helper()
	u, base := runtimeTestUI(t)
	c := &runtimeMessagesClient{runtimeInputTestClient: &runtimeInputTestClient{runtimeTestClient: base}}
	u.runtime.client = c
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "review", ToolID: "review-tool", Kind: "local_agent", Role: "reviewer", Description: "Review implementation", Status: "running"})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", Kind: "local_bash", Description: "Native Bash job", Status: "running"})
	return u, c
}

func TestNativeRuntimeMessagesCompletionAndDelivery(t *testing.T) {
	u, c := runtimeMessagesUI(t)
	runtimeKeys(t, u, "/to\t")
	if u.draft != "/to " || len(u.picker.choices) != 1 || u.picker.choices[0].name != "/root/review" {
		t.Fatalf("agent completion included a shell task or missed native child: %q %+v", u.draft, u.picker.choices)
	}
	runtimeKeys(t, u, "\tCheck 世界\nPreserve original context.  ")
	before := u.draft
	u.runtime.busy = true // A direct message must not steer Main's active turn.
	runtimeKeys(t, u, "\r\r")
	if len(c.messages) != 1 || len(c.sent) != 0 || c.messages[0].AgentID != "review" || c.messages[0].SessionID != u.thread || c.messages[0].Text != "Check 世界\nPreserve original context.  " || u.draft != before {
		t.Fatalf("direct native intent or pending draft changed: %+v %q", c.messages, u.draft)
	}
	m := c.messages[0]
	stale := m
	stale.AgentID = "other"
	runtimeEvidenceEvent(t, u, session.Event{Kind: "agent_message", AgentMessage: &stale})
	if u.runtime.message == nil || u.draft != before {
		t.Fatal("another target's receipt consumed pending message")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "agent_message", AgentMessage: &m})
	if u.draft != "" || u.runtime.message != nil || !u.runtime.busy {
		t.Fatal("delivery receipt changed Main lifecycle or failed to clear matching draft")
	}
	for _, v := range []*liveActivityView{u.view, u.agents} {
		e := v.entries[len(v.entries)-1]
		if e.Kind != "reply" || e.Agent != "/root/review" || e.message == nil || e.message.from != "You via Mekugi" || e.Text != m.Text {
			t.Fatal("queued message lost directed lane or native plugin provenance")
		}
	}
}

func TestNativeRuntimeMessagesFailuresAndEditedDraft(t *testing.T) {
	u, c := runtimeMessagesUI(t)
	// Complete commands do not require a displayed target: native admission owns
	// rejection, including historical targets inherited from a different parent.
	runtimeKeys(t, u, "/to unknown Keep this draft")
	c.err = errors.New("bridge unavailable")
	runtimeKeys(t, u, "\r")
	if u.runtime.message != nil || u.draft != "/to unknown Keep this draft" {
		t.Fatal("transport failure consumed the message")
	}
	c.err = nil
	runtimeKeys(t, u, "\r")
	m := c.messages[len(c.messages)-1]
	m.Text = "Native child does not belong to this session"
	runtimeEvidenceEvent(t, u, session.Event{Kind: "agent_message", AgentMessage: &m, Failed: true})
	if u.draft != "/to unknown Keep this draft" || u.runtime.message != nil || len(c.sent) != 0 {
		t.Fatal("native rejection consumed draft or dispatched Main")
	}
	u.loadDraft(composerDraft{text: "/to review Continue"})
	runtimeKeys(t, u, "\r")
	m = c.messages[len(c.messages)-1]
	u.loadDraft(composerDraft{text: "New Main draft"})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "agent_message", AgentMessage: &m})
	if u.draft != "New Main draft" {
		t.Fatal("late queued receipt erased an edited composer")
	}
	count := len(c.messages)
	u.session.cwd = t.TempDir()
	u.loadDraft(composerDraft{text: "/to review "})
	image := runtimeInputImage(t, u.session.cwd, "message.png")
	runtimeKeys(t, u, "\x1b[200~"+image+"\x1b[201~")
	runtimeKeys(t, u, "\r")
	if len(c.messages) != count || len(u.images) != 1 || !strings.Contains(u.notice, "plain text") {
		t.Fatal("direct message dropped an unsupported image")
	}
	u.loadDraft(composerDraft{text: "/to review Read "})
	runtimeInputFile(t, u.session.cwd, "notes.txt", "Private file snapshot")
	runtimeKeys(t, u, "@notes")
	runtimeInputScan(t, u)
	runtimeKeys(t, u, "\t\r")
	if len(c.messages) != count || len(u.files) != 1 || !strings.Contains(u.notice, "plain text") {
		t.Fatal("direct message dropped an unsupported file-picker attachment")
	}
}

func TestUISnapshotNativeRuntimeMessages(t *testing.T) {
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, c := runtimeMessagesUI(t)
			runtimeEvidenceEvent(t, u, session.Event{Kind: "task", Historical: true, Task: &session.Task{ID: "saved-child", Kind: "local_agent", Status: "saved"}})
			runtimeKeys(t, u, "/to ")
			finishPacing(u.view, u.agents)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-messages-picker-%d.txt", width)), runtimeFrame(t, u, width, 36))
			runtimeKeys(t, u, "review\tPlease review the implementation.\r")
			m := c.messages[len(c.messages)-1]
			runtimeEvidenceEvent(t, u, session.Event{Kind: "agent_message", AgentMessage: &m})
			finishPacing(u.view, u.agents)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-messages-queued-%d.txt", width)), runtimeFrame(t, u, width, 36))
		})
	}
}

func TestNativeRuntimeMessageReceiptKeepsNewFileBinding(t *testing.T) {
	u, c := runtimeMessagesUI(t)
	u.session.cwd = t.TempDir()
	runtimeInputFile(t, u.session.cwd, "notes.txt", "Unsent file attachment")
	const draft = "/to review Read @notes.txt "
	runtimeKeys(t, u, draft+"\r\x1b[D")
	runtimeInputScan(t, u)
	runtimeKeys(t, u, "\t")
	if u.draft != draft || len(u.files) != 1 {
		t.Fatal("fixture did not attach the existing token without changing text")
	}
	undo := len(u.undoDrafts)
	m := c.messages[0]
	runtimeEvidenceEvent(t, u, session.Event{Kind: "agent_message", AgentMessage: &m})
	if u.draft != draft || len(u.files) != 1 || len(u.undoDrafts) != undo {
		t.Fatal("queue receipt discarded an unsent attachment or its undo history")
	}
}
