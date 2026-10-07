package router

import (
	"context"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

type runtimeTitleControlsClient struct {
	*runtimeControlsClient
	renamed []session.SessionTitle
}

func (c *runtimeTitleControlsClient) RenameSession(_ context.Context, title session.SessionTitle) error {
	c.renamed = append(c.renamed, title)
	return nil
}

func TestNativeRuntimePendingTitleStaysWithDepartingSession(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		failed       bool
	}{
		{name: "clear"},
		{name: "resume", target: "saved"},
		{name: "failed preflight", target: "saved", failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, base := runtimeControlsUI(t)
			c := &runtimeTitleControlsClient{runtimeControlsClient: base}
			u.runtime.client, u.thread = c, ""
			runtimeKeys(t, u, "/title Departing draft title\r")
			command := "/clear"
			if tc.target != "" {
				command = "/resume " + tc.target
			}
			runtimeKeys(t, u, command+"\r")
			if len(c.changes) != 1 || u.pendingTitle != "Departing draft title" {
				t.Fatal("transition lost pending title before confirmation")
			}
			receipt := c.changes[0]
			receipt.Title = "Saved session title"
			if tc.failed {
				runtimeControlsEvent(t, u, session.Event{Kind: "session_ready", Change: &receipt, Failed: true, Text: "preflight failed"})
				if u.pendingTitle != "Departing draft title" {
					t.Fatal("failed transition lost source title intent")
				}
				runtimeControlsEvent(t, u, session.Event{Kind: "session", SessionID: "source"})
				if len(c.renamed) != 1 || c.renamed[0].SessionID != "source" {
					t.Fatal("source title intent not kept after failure")
				}
				return
			}
			runtimeControlsEvent(t, u, session.Event{Kind: "session_change", Change: &receipt})
			if u.pendingTitle != "" || len(c.renamed) != 0 {
				t.Fatal("transition renamed the selected session with source intent")
			}
			runtimeControlsEvent(t, u, session.Event{Kind: "session_ready", Change: &receipt})
			if tc.target == "" {
				runtimeControlsEvent(t, u, session.Event{Kind: "session", SessionID: "cleared-session"})
			}
			if len(c.renamed) != 0 {
				t.Fatal("late new identity revived source title intent")
			}
		})
	}
}
