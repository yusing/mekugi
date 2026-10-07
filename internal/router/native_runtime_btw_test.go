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

type runtimeBTWTestClient struct {
	*runtimeTestClient
	inputs []session.SideInput
	closed []string
	err    error
}

func (c *runtimeBTWTestClient) SendSide(_ context.Context, input session.SideInput) error {
	c.inputs = append(c.inputs, input)
	return c.err
}
func (c *runtimeBTWTestClient) CloseSide(_ context.Context, id string) error {
	c.closed = append(c.closed, id)
	return nil
}
func runtimeBTWTestUI(t *testing.T) (*appServerUI, *runtimeBTWTestClient) {
	u, base := runtimeTestUI(t)
	c := &runtimeBTWTestClient{runtimeTestClient: base}
	u.runtime.client = c
	return u, c
}

func TestNativeRuntimeBTWIsolationFollowUpAndRetiredEvents(t *testing.T) {
	u, c := runtimeBTWTestUI(t)
	u.runtime.busy = true
	runtimeKeys(t, u, "/btw explain context\r")
	b := u.btw
	if b == nil || len(c.inputs) != 1 || c.inputs[0].Source != u.thread || len(c.sent) != 0 || u.draft != "" {
		t.Fatal("side question did not use shared dock and native fork")
	}
	event := func(e session.Event) {
		t.Helper()
		e.SideID = b.nativeID
		if err := u.runtimeEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	event(session.Event{Kind: "session", SessionID: "native-side"})
	event(session.Event{Kind: "message", ID: "answer", Role: "Claude", Text: "Side **answer**"})
	event(session.Event{Kind: "done"})
	if !u.runtime.busy || u.thread != "native-session" || len(u.view.entries) != 0 || b.busy || b.answer[0].text != "Side **answer**" {
		t.Fatal("side events changed Main or failed to settle the dock")
	}
	runtimeKeys(t, u, "/btw follow-up\r")
	if len(c.inputs) != 2 || c.inputs[1].ID != c.inputs[0].ID || len(b.answer) != 0 {
		t.Fatal("follow-up did not keep native conversation and replace displayed exchange")
	}
	runtimeKeys(t, u, "/btw still busy\r")
	if len(c.inputs) != 2 || u.draft != "/btw still busy" {
		t.Fatal("busy side question lost draft or sent again")
	}
	oldID := b.nativeID
	if err := u.closeBTW(); err != nil {
		t.Fatal(err)
	}
	if len(c.closed) != 1 || c.closed[0] != oldID || c.interrupts != 0 || u.draft != "/btw still busy" {
		t.Fatal("closing side affected Main or its draft")
	}
	u.loadDraft(composerDraft{text: "/btw new snapshot"})
	runtimeKeys(t, u, "\r")
	newDock := u.btw
	if newDock.nativeID == oldID {
		t.Fatal("closed side identity reused")
	}
	event(session.Event{Kind: "message", ID: "late", Role: "Claude", Text: "old answer"})
	event(session.Event{Kind: "side_closed"})
	if u.btw != newDock || len(newDock.answer) != 0 || !newDock.busy {
		t.Fatal("retired event changed replacement dock")
	}
}

func TestNativeRuntimeBTWAttachmentFailureKeepsDraft(t *testing.T) {
	u, c := runtimeBTWTestUI(t)
	u.session.cwd = t.TempDir()
	image := runtimeInputImage(t, u.session.cwd, "image.png")
	runtimeInputFile(t, u.session.cwd, "notes.txt", "EXPLICIT_FILE_CONTENT")
	u.draft = "/btw explain "
	bindComposerFile(u, "@notes.txt", "notes.txt")
	u.insertDraft(" ")
	u.insertImage(image)
	c.err = errors.New("bridge unavailable")
	runtimeKeys(t, u, "\r")
	if len(c.inputs) != 1 || len(c.inputs[0].Input) != 4 || c.inputs[0].Input[2].ImagePath != image || !strings.Contains(c.inputs[0].Input[1].Text, "@notes.txt") || !strings.Contains(c.inputs[0].Input[3].Text, "EXPLICIT_FILE_CONTENT") {
		t.Fatalf("ordered native attachments: %+v", c.inputs)
	}
	if !strings.HasPrefix(u.draft, "/btw\n") || len(u.images) != 1 || u.draft[u.images[0].start:u.images[0].end] != "[Image 1]" || u.btw.busy {
		t.Fatalf("side send failure lost editable attachment draft: %q %+v", u.draft, u.images)
	}
}

func TestUISnapshotNativeRuntimeBTW(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := runtimeBTWTestUI(t)
			runtimeKeys(t, u, "/btw explain context\r")
			u.runtimeEvent(session.Event{Kind: "session", SessionID: "native-side", SideID: u.btw.nativeID})
			u.runtimeEvent(session.Event{Kind: "message", ID: "side", Role: "Claude", Text: "The side answer uses **the native snapshot**.\n\n- Main continues.\n- Follow-ups stay isolated.", SideID: u.btw.nativeID})
			runtimeKeys(t, u, "Main draft kept")
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-btw-%d.txt", width)), runtimeFrame(t, u, width, 28))
		})
	}
}
