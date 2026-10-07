package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/session"
)

type runtimeControlsClient struct {
	*runtimeTestClient
	lists       []session.SessionListRequest
	changes     []session.SessionChange
	changeError error
}

func (c *runtimeControlsClient) ListSessions(_ context.Context, r session.SessionListRequest) error {
	c.lists = append(c.lists, r)
	return nil
}
func (c *runtimeControlsClient) ChangeSession(_ context.Context, r session.SessionChange) error {
	c.changes = append(c.changes, r)
	return c.changeError
}
func runtimeControlsUI(t *testing.T) (*appServerUI, *runtimeControlsClient) {
	t.Helper()
	u, base := runtimeTestUI(t)
	c := &runtimeControlsClient{runtimeTestClient: base}
	u.runtime.client = c
	return u, c
}
func runtimeControlsEvent(t *testing.T, u *appServerUI, e session.Event) {
	t.Helper()
	if err := u.runtimeEvent(e); err != nil {
		t.Fatal(err)
	}
}
func runtimeControlsPage(t *testing.T, u *appServerUI, id, cursor string, rows ...session.SavedSession) {
	t.Helper()
	runtimeControlsEvent(t, u, session.Event{Kind: "sessions", Sessions: &session.SessionPage{ID: id, Cursor: cursor, Sessions: rows}})
}

func TestNativeRuntimeControlsPickerPagesSearchFilterRetry(t *testing.T) {
	u, c := runtimeControlsUI(t)
	runtimeKeys(t, u, "/resume\r")
	if len(c.lists) != 1 || c.lists[0].Cwd != "/work" || c.lists[0].Limit != 25 {
		t.Fatalf("initial listing: %+v", c.lists)
	}
	var rows []session.SavedSession
	for i := range 25 {
		rows = append(rows, session.SavedSession{ID: fmt.Sprint(i), Title: fmt.Sprintf("Session %d", i), Cwd: "/work"})
	}
	runtimeControlsPage(t, u, c.lists[0].ID, "page-two", rows...)
	if len(c.lists) != 1 {
		t.Fatal("unrequested prefetch")
	}
	resumeTestKeys(t, u, "\x1b[F")
	if len(c.lists) != 2 || c.lists[1].Cursor != "page-two" {
		t.Fatalf("navigation page: %+v", c.lists)
	}
	resumeTestKeys(t, u, "\t")
	if len(c.lists) != 3 || c.lists[2].Cwd != "" || c.lists[2].Cursor != "" {
		t.Fatalf("all filter: %+v", c.lists)
	}
	runtimeControlsPage(t, u, c.lists[1].ID, "", session.SavedSession{ID: "stale", Title: "Stale"})
	if len(u.resumePicker.rows) != 0 || !u.resumePicker.loading {
		t.Fatal("stale receipt changed current listing")
	}
	runtimeControlsPage(t, u, c.lists[2].ID, "", session.SavedSession{ID: "here", Title: "Here", Cwd: "/work"}, session.SavedSession{ID: "else", Title: "Elsewhere", Cwd: "/else", Branch: "feature"})
	resumeTestKeys(t, u, "feat")
	if got := u.resumePicker.matches(); len(got) != 1 || got[0].id != "else" {
		t.Fatalf("branch search: %+v", got)
	}
	resumeTestKeys(t, u, "\x1b")
	if u.resumePicker == nil || u.resumePicker.query != "" {
		t.Fatal("Escape failed to clear search")
	}
	resumeTestKeys(t, u, "\t")
	request := c.lists[len(c.lists)-1]
	if request.Cwd != "/work" {
		t.Fatal("cwd filter not restored")
	}
	runtimeControlsEvent(t, u, session.Event{Kind: "sessions", Failed: true, Text: "offline", Sessions: &session.SessionPage{ID: request.ID}})
	before := len(c.lists)
	if err := u.fillResumePicker(); err != nil {
		t.Fatal(err)
	}
	if len(c.lists) != before || u.resumePicker.problem == "" {
		t.Fatal("failed page automatically retried")
	}
	resumeTestKeys(t, u, "\t")
	if len(c.lists) != before+1 || !u.resumePicker.loading {
		t.Fatal("explicit retry failed")
	}
	resumeTestKeys(t, u, "\x1b")
	runtimeControlsPage(t, u, c.lists[len(c.lists)-1].ID, "", session.SavedSession{ID: "late"})
	if u.resumePicker != nil || len(c.changes) != 0 {
		t.Fatal("closed picker revived")
	}
}

func TestNativeRuntimeControlsBusyGuards(t *testing.T) {
	for _, command := range []string{"/resume saved", "/clear"} {
		states := []string{"turn", "permission", "settings", "bash", "child", "transition"}
		if command == "/clear" {
			states = states[:1]
		}
		for _, state := range states {
			t.Run(command+"/"+state, func(t *testing.T) {
				u, c := runtimeControlsUI(t)
				switch state {
				case "turn":
					u.runtime.busy = true
				case "permission":
					runtimeControlsEvent(t, u, session.Event{Kind: "prompt", Prompt: &session.Prompt{ID: "ask", Tool: "Bash", Description: "Needs permission"}})
				case "settings":
					u.runtime.settings = &session.Settings{ID: "pending", Field: "model", Value: "opus"}
				case "bash", "child":
					kind := "local_bash"
					if state == "child" {
						kind = "local_agent"
					}
					runtimeControlsEvent(t, u, session.Event{Kind: "task", Role: "task_started", Task: &session.Task{ID: "live", Kind: kind, Status: "running"}})
				case "transition":
					u.runtime.changeRequest = "pending"
					u.runtime.ready = false
				}
				if command == "/clear" {
					if err := u.sessionCommand(command); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := u.resumeCommand(command); err != nil {
						t.Fatal(err)
					}
				}
				if len(c.changes) != 0 || len(c.sent) != 0 || u.thread != "native-session" {
					t.Fatal("busy session stranded native work")
				}
			})
		}
	}
}

func TestNativeRuntimeControlsResumeCommitHistoryReady(t *testing.T) {
	u, c := runtimeControlsUI(t)
	runtimeControlsEvent(t, u, session.Event{Kind: "commands", CommandInfo: []session.Command{{Name: "workspace-a"}}})
	runtimeControlsEvent(t, u, session.Event{Kind: "message", ID: "old", Text: "Old transcript"})
	runtimeKeys(t, u, "/resume saved\r")
	if len(c.changes) != 1 || c.changes[0].SessionID != "saved" || u.thread != "native-session" || !u.replacement.pending() {
		t.Fatalf("resume intent: %+v", c.changes)
	}
	runtimeKeys(t, u, "Next input\r")
	if len(c.sent) != 0 || u.draft != "Next input" {
		t.Fatal("transition sent input to old session")
	}
	request := c.changes[0]
	runtimeControlsEvent(t, u, session.Event{Kind: "session_ready", Change: &session.SessionChange{ID: "stale"}})
	if u.runtime.ready {
		t.Fatal("stale ready admitted input")
	}
	runtimeControlsEvent(t, u, session.Event{Kind: "session_change", Change: &request})
	if u.thread != "saved" || u.runtime.ready || len(u.view.entries) != 0 {
		t.Fatal("commit did not isolate presentation")
	}
	runtimeControlsEvent(t, u, session.Event{Kind: "message", ID: "question", Role: "You", Text: "Saved question", Historical: true})
	runtimeControlsEvent(t, u, session.Event{Kind: "message", ID: "answer", Text: "Saved answer", Historical: true})
	if len(u.view.entries) != 2 || u.view.entries[0].Text != "Saved question" || u.view.entries[1].Text != "Saved answer" {
		t.Fatalf("history order: %+v", u.view.entries)
	}
	runtimeKeys(t, u, "\r")
	if len(c.sent) != 0 {
		t.Fatal("history receipt admitted input before ready")
	}
	runtimeControlsEvent(t, u, session.Event{Kind: "commands", CommandInfo: []session.Command{{Name: "workspace-b"}}})
	if len(u.runtime.commandInfo) != 1 || u.runtime.commandInfo[0].Name != "workspace-b" {
		t.Fatal("selected session retained the departing command catalog")
	}
	runtimeControlsEvent(t, u, session.Event{Kind: "session_ready", Change: &request})
	runtimeKeys(t, u, "\r")
	if !slices.Equal(c.sent, []string{"Next input"}) || u.replacement.pending() {
		t.Fatalf("ready input: %q", c.sent)
	}
	before := len(u.view.entries)
	runtimeControlsEvent(t, u, session.Event{Kind: "session_change", Change: &request})
	if len(u.view.entries) != before || u.thread != "saved" {
		t.Fatal("late change reset new conversation")
	}
}

func TestNativeRuntimeControlsFailuresAndSameTarget(t *testing.T) {
	t.Run("same target", func(t *testing.T) {
		u, c := runtimeControlsUI(t)
		runtimeKeys(t, u, "/resume native-session\r")
		if len(c.changes) != 0 || u.replacement.pending() {
			t.Fatal("current session switched")
		}
	})
	t.Run("failed ready", func(t *testing.T) {
		u, c := runtimeControlsUI(t)
		runtimeControlsEvent(t, u, session.Event{Kind: "message", ID: "old", Text: "Keep transcript"})
		runtimeKeys(t, u, "/resume saved\rRetry input\r")
		runtimeControlsEvent(t, u, session.Event{Kind: "session_ready", Failed: true, Text: "missing", Change: &c.changes[0]})
		if u.thread != "native-session" || len(u.view.entries) != 1 || u.view.entries[0].Text != "Keep transcript" || u.draft != "Retry input" || !u.runtime.ready || len(c.sent) != 0 {
			t.Fatal("failed switch lost old conversation or draft")
		}
	})
	for _, command := range []string{"/resume saved", "/clear"} {
		t.Run("transport "+command, func(t *testing.T) {
			u, c := runtimeControlsUI(t)
			c.changeError = errors.New("offline")
			runtimeKeys(t, u, command+"\r")
			if strings.TrimSpace(u.draft) != command || u.thread != "native-session" || u.replacement.pending() || !u.runtime.ready || len(c.sent) != 0 {
				t.Fatalf("transport failure lost draft %q or changed session", u.draft)
			}
		})
	}
}

func TestNativeRuntimeControlsClearResetsPresentation(t *testing.T) {
	u, c := runtimeControlsUI(t)
	runtimeControlsEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: `{"command":"echo old"}`})
	runtimeControlsEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "old output"})
	u.journal = &nativeJournalSink{thread: "native-session"}
	u.runtime.usage = &session.Usage{}
	u.runtime.limits = map[string]session.RateLimit{"old": {Status: "allowed"}}
	u.runtime.tasks = map[string]session.Task{"finished": {ID: "finished", Status: "completed"}}
	u.runtime.taskOrder = []string{"finished"}
	runtimeKeys(t, u, "/clear\r")
	u.shell.output = &outputDialog{}
	if len(c.changes) != 1 || c.changes[0].SessionID != "" || len(u.view.entries) == 0 {
		t.Fatal("clear must await native commit")
	}
	runtimeControlsEvent(t, u, session.Event{Kind: "session_change", Change: &c.changes[0]})
	if u.thread != "" || len(u.view.entries) != 0 || len(u.agents.entries) != 0 || u.shell.output != nil || u.journal != nil || u.runtime.usage != nil || len(u.runtime.limits) != 0 || len(u.runtime.tasks) != 0 {
		t.Fatal("clear retained old session presentation")
	}
	runtimeControlsEvent(t, u, session.Event{Kind: "session_ready", Change: &c.changes[0]})
	runtimeKeys(t, u, "Fresh input\r")
	if !slices.Equal(c.sent, []string{"Fresh input"}) {
		t.Fatalf("fresh input: %q", c.sent)
	}
}

func TestUISnapshotNativeRuntimeResumePicker(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, c := runtimeControlsUI(t)
			u.view.painter.Theme = livediff.DarkTheme
			runtimeKeys(t, u, "/resume\r")
			resumeTestKeys(t, u, "\t")
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local)
			runtimeControlsPage(t, u, c.lists[len(c.lists)-1].ID, "",
				session.SavedSession{ID: "native-session", Title: "Current Claude conversation", Cwd: "/work", Branch: "main", Updated: now.Add(-2 * time.Minute).Unix()},
				session.SavedSession{ID: "saved", Title: "Fix session restore and history ordering", Cwd: "/other/project", Branch: "feat/native-resume", Updated: now.Add(-2 * time.Hour).Unix()})
			u.resumePicker.loadedAt = now
			resumeTestKeys(t, u, "\x1b[B")
			assertNativeUISnapshot(t, fmt.Sprintf("native-runtime-resume-%d", width), u.resumePickerFrame(width, 12))
		})
	}
}

func TestUISnapshotNativeRuntimeCrossWorkspaceHistory(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, c := runtimeControlsUI(t)
			u.view.painter.Theme = livediff.DarkTheme
			runtimeKeys(t, u, "/resume saved-b\r")
			change := c.changes[0]
			change.Cwd, change.Title = "/other/project B", "Workspace B session"
			runtimeControlsEvent(t, u, session.Event{Kind: "session_change", Change: &change})
			runtimeControlsEvent(t, u, session.Event{Kind: "message", ID: "question", Role: "You", Text: "Inspect workspace B", Historical: true})
			runtimeControlsEvent(t, u, session.Event{Kind: "tool", ID: "read-b", Role: "Read", Text: `{"file_path":"/other/project B/src/main.go"}`, Historical: true})
			runtimeControlsEvent(t, u, session.Event{Kind: "tool_result", ID: "read-b", Text: "package main", Historical: true})
			runtimeControlsEvent(t, u, session.Event{Kind: "session_ready", Change: &change})
			assertNativeUISnapshot(t, fmt.Sprintf("native-runtime-cross-workspace-history-%d", width), strings.Split(runtimeFrame(t, u, width, 18), "\n"))
			runtimeKeys(t, u, "/resume\r")
			if c.lists[len(c.lists)-1].Cwd != change.Cwd {
				t.Fatal("resumed workspace did not own the picker filter")
			}
		})
	}
}
