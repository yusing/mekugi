package router

import (
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

type resumeTestRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		ThreadID    string   `json:"threadId"`
		Cwd         *string  `json:"cwd"`
		Cursor      string   `json:"cursor"`
		Ancestor    string   `json:"ancestorThreadId"`
		SourceKinds []string `json:"sourceKinds"`
		Limit       int      `json:"limit"`
	} `json:"params"`
}

func resumeTestRequests(t *testing.T, w *appServerTestInput) []resumeTestRequest {
	t.Helper()
	return appServerDrainRequests[resumeTestRequest](t, w)
}

func resumeTestOne(t *testing.T, w *appServerTestInput, method string) resumeTestRequest {
	t.Helper()
	requests := resumeTestRequests(t, w)
	if len(requests) != 1 || requests[0].Method != method {
		t.Fatalf("requests = %+v, want one %s", requests, method)
	}
	return requests[0]
}

// resumeTestSettle answers restoration and setup requests with empty results
// and returns every request method it saw.
func resumeTestSettle(t *testing.T, u *appServerUI, w *appServerTestInput) []string {
	t.Helper()
	var methods []string
	for range 16 {
		requests := resumeTestRequests(t, w)
		if len(requests) == 0 {
			return methods
		}
		for _, r := range requests {
			methods = append(methods, r.Method)
			if r.ID != 0 || r.Method != "" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[],"nextCursor":null}}`, r.ID))
			}
		}
	}
	t.Fatal("restoration did not settle")
	return nil
}

func resumeTestFrame(t *testing.T, u *appServerUI, width, height int) string {
	t.Helper()
	u.ensureShell()
	rows, _ := u.mainFrame(width, height, 0)
	return ansi.Strip(strings.Join(rows, "\n"))
}

func resumeTestKeys(t *testing.T, u *appServerUI, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if err := u.resumePickerKey(key); err != nil {
			t.Fatalf("picker key %q: %v", key, err)
		}
	}
}

func newResumeSessionTestUI(t *testing.T) (*appServerUI, *appServerTestInput) {
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", "/work")
	return u, w
}

func TestAppServerResumePickerStartup(t *testing.T) {
	u, w := newAppServerTestUI()
	u.thread, u.resumeThread, u.resumeCwd = "", resumePickerStartup, "/work"
	u.agents = newLiveActivityView()
	u.requests["0"] = "initialize"
	appServerTestMessage(t, u, `{"id":0,"result":{}}`)
	requests := resumeTestRequests(t, w)
	if len(requests) != 2 || requests[1].Method != "thread/list" {
		t.Fatalf("startup requests: %+v", requests)
	}
	list := requests[1]
	if list.Params.Cwd == nil || *list.Params.Cwd != "/work" || list.Params.Limit != resumePickerPage || !slices.Equal(list.Params.SourceKinds, []string{"cli", "vscode", "appServer"}) {
		t.Fatalf("picker listing: %+v", list.Params)
	}
	// Notifications before any thread exists are not buffered as resume events.
	appServerTestNotify(t, u, "skills/changed", map[string]any{})
	if len(u.resumePending) != 0 {
		t.Fatal("picker buffered notifications before choosing a thread")
	}
	if frame := resumeTestFrame(t, u, 120, 20); !strings.Contains(frame, "Resume a previous session") || !strings.Contains(frame, "Loading sessions…") {
		t.Fatalf("loading picker frame:\n%s", frame)
	}
	now := time.Now().Unix()
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[
		{"id":"recent","preview":"Fix the parser\nsecond line","cwd":"/work","updatedAt":%d,"gitInfo":{"branch":"main"}},
		{"id":"named","name":"Release notes","preview":"ignored preview","cwd":"/work","updatedAt":%d,"gitInfo":{"branch":"release-branch"}},
		{"id":"","preview":"no identity"}],"nextCursor":null}}`, list.ID, now-120, now-7200))
	frame := resumeTestFrame(t, u, 120, 20)
	for _, want := range []string{"Filter: Cwd All · /work", "Updated", "Branch", "Conversation", "› 2m ago", "Fix the parser second line", "2h ago", "release-branch", "Release notes", "esc start new", "ctrl+c quit"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("picker frame lacks %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "ignored preview") || strings.Contains(frame, "no identity") || strings.Contains(frame, "Directory") {
		t.Fatalf("picker frame shows wrong rows or columns:\n%s", frame)
	}
	resumeTestKeys(t, u, "\x1b[B", "\r")
	resume := resumeTestOne(t, w, "thread/resume")
	if resume.Params.ThreadID != "named" || u.resumePicker != nil || u.resumeThread != "named" {
		t.Fatalf("picker resumed %+v; state resume=%q", resume.Params, u.resumeThread)
	}
	// Once a thread is chosen, early events wait for its history as usual.
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"named","turnId":"old","item":{"type":"agentMessage","id":"answer","text":"Saved answer"}}}`)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","thread":{"id":"named","cwd":"/work","turns":[{"id":"old","status":"completed","items":[{"type":"userMessage","id":"question","content":[{"type":"text","text":"Saved question"}]},{"type":"agentMessage","id":"answer","text":"Saved answer"}]}]}}}`, resume.ID))
	resumeTestSettle(t, u, w)
	if u.thread != "named" || u.status != "Ready" || len(u.view.entries) != 2 || u.view.entries[1].Text != "Saved answer" {
		t.Fatalf("resumed state: thread=%q status=%q entries=%+v", u.thread, u.status, u.view.entries)
	}
}

func TestAppServerResumePickerStartupEscapeAndQuit(t *testing.T) {
	u, w := newAppServerTestUI()
	u.thread, u.resumeThread, u.resumeCwd = "", resumePickerStartup, "/work"
	if err := u.openResumePicker(true); err != nil {
		t.Fatal(err)
	}
	resumeTestOne(t, w, "thread/list")
	resumeTestKeys(t, u, "x", "\x1b")
	if u.resumePicker == nil || u.resumePicker.query != "" || len(resumeTestRequests(t, w)) != 0 {
		t.Fatal("first Escape must only clear the search")
	}
	resumeTestKeys(t, u, "\x1b")
	if u.resumePicker != nil || u.resumeThread != "" {
		t.Fatal("Escape did not leave the picker")
	}
	resumeTestOne(t, w, "thread/start")

	u, w = newAppServerTestUI()
	u.thread, u.resumeThread = "", resumePickerStartup
	if err := u.openResumePicker(true); err != nil {
		t.Fatal(err)
	}
	resumeTestRequests(t, w)
	resumeTestKeys(t, u, "\x03")
	if !u.quitRequested || len(resumeTestRequests(t, w)) != 0 {
		t.Fatal("Ctrl-C at startup must quit without creating a thread")
	}
}

func TestAppServerResumePickerSearchFilterAndPages(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.start("main", "/work")
	if err := u.openResumePicker(false); err != nil {
		t.Fatal(err)
	}
	first := resumeTestOne(t, w, "thread/list")
	var rows []string
	for i := range resumePickerPage {
		branch := "main"
		if i == 3 {
			branch = "feature-x"
		}
		rows = append(rows, fmt.Sprintf(`{"id":"t%d","preview":"session %d","cwd":"/work","updatedAt":1,"gitInfo":{"branch":%q}}`, i, i, branch))
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[%s],"nextCursor":"page-2"}}`, first.ID, strings.Join(rows, ",")))
	if requests := resumeTestRequests(t, w); len(requests) != 0 {
		t.Fatalf("picker preloaded pages without scrolling: %+v", requests)
	}
	resumeTestFrame(t, u, 120, 20)
	resumeTestKeys(t, u, "\x1b[F")
	next := resumeTestOne(t, w, "thread/list")
	if next.Params.Cursor != "page-2" {
		t.Fatalf("next page cursor: %+v", next.Params)
	}
	// Tab restarts the listing for every workspace; the old page is stale.
	resumeTestKeys(t, u, "\t")
	all := resumeTestOne(t, w, "thread/list")
	if all.Params.Cwd != nil || all.Params.Cursor != "" || len(u.resumePicker.rows) != 0 {
		t.Fatalf("all-workspace listing: %+v rows=%d", all.Params, len(u.resumePicker.rows))
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"stale","preview":"stale"}],"nextCursor":null}}`, next.ID))
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"here","preview":"Here","cwd":"/work/sub","updatedAt":1},{"id":"there","preview":"There","cwd":"/elsewhere","updatedAt":1,"gitInfo":{"branch":"feature-y"}}],"nextCursor":null}}`, all.ID))
	frame := resumeTestFrame(t, u, 130, 20)
	if strings.Contains(frame, "stale") || !strings.Contains(frame, "Directory") || !strings.Contains(frame, "sub") || !strings.Contains(frame, "/elsewhere") || !strings.Contains(frame, "esc close") {
		t.Fatalf("all-workspace frame:\n%s", frame)
	}
	resumeTestKeys(t, u, "f", "e", "a", "t")
	if got := u.resumePicker.matches(); len(got) != 1 || got[0].id != "there" {
		t.Fatalf("search matched %+v", got)
	}
	resumeTestKeys(t, u, "\x7f", "\x7f", "\x7f", "\x7f", "z", "z")
	if frame := resumeTestFrame(t, u, 130, 20); !strings.Contains(frame, "No results for your search") {
		t.Fatalf("empty search frame:\n%s", frame)
	}
}

func TestAppServerResumeSwitchesSession(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	appServerTestUserMessage(t, u, "old-user", "", "old transcript")
	appServerTestKeys(t, u, "/resume\r")
	list := resumeTestOne(t, w, "thread/list")
	if list.Params.Cwd == nil || *list.Params.Cwd != "/work" || u.draft != "" {
		t.Fatalf("in-session listing: %+v draft=%q", list.Params, u.draft)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"main","preview":"old transcript","cwd":"/work","updatedAt":2},{"id":"saved","preview":"Saved question","cwd":"/work","updatedAt":1}],"nextCursor":null}}`, list.ID))
	if frame := resumeTestFrame(t, u, 120, 20); !strings.Contains(frame, "old transcript · current") || !strings.Contains(frame, "esc close") {
		t.Fatalf("in-session picker frame:\n%s", frame)
	}
	resumeTestKeys(t, u, "\r")
	if u.resumePicker != nil || u.notice != "Already viewing this session" || len(resumeTestRequests(t, w)) != 0 {
		t.Fatal("choosing the current session must not resume it again")
	}
	appServerTestKeys(t, u, "/resume\r")
	list = resumeTestOne(t, w, "thread/list")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"main","preview":"old transcript"},{"id":"saved","preview":"Saved question"}],"nextCursor":null}}`, list.ID))
	resumeTestKeys(t, u, "\x1b[B", "\r")
	resume := resumeTestOne(t, w, "thread/resume")
	if resume.Params.ThreadID != "saved" || !u.clearing || u.thread != "main" || len(u.view.entries) != 1 {
		t.Fatalf("switch request/state: %+v thread=%q entries=%+v", resume.Params, u.thread, u.view.entries)
	}
	// The current session stays live until Codex confirms the switch.
	appServerTestUserMessage(t, u, "late-old", "", "late old transcript")
	if len(resumeTestRequests(t, w)) != 0 || len(u.view.entries) != 2 || len(u.resumePending) != 0 {
		t.Fatalf("current session not live while switching: entries=%+v pending=%d", u.view.entries, len(u.resumePending))
	}
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"saved","turnId":"old","item":{"type":"agentMessage","id":"answer","text":"Saved answer"}}}`)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","thread":{"id":"saved","cwd":"/work","turns":[{"id":"old","status":"completed","items":[{"type":"userMessage","id":"question","content":[{"type":"text","text":"Saved question"}]},{"type":"agentMessage","id":"answer","text":"Saved answer"}]}]}}}`, resume.ID))
	methods := resumeTestSettle(t, u, w)
	if len(methods) == 0 || methods[0] != "thread/unsubscribe" || !slices.Contains(methods, "model/list") {
		t.Fatalf("switch follow-up requests: %v", methods)
	}
	if u.thread != "saved" || u.clearing || u.switching != "" || u.status != "Ready" || len(u.view.entries) != 2 || u.view.entries[0].Text != "Saved question" {
		t.Fatalf("switched state: thread=%q status=%q entries=%+v", u.thread, u.status, u.view.entries)
	}
	if frame := resumeTestFrame(t, u, 120, 20); strings.Contains(frame, "old transcript") || strings.Contains(frame, "late old") {
		t.Fatalf("old transcript remains rendered:\n%s", frame)
	}
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"late-turn"}}}`)
	if u.turn != "" {
		t.Fatal("retired thread started a turn in the resumed session")
	}
	appServerTestKeys(t, u, "next\r")
	start := resumeTestOne(t, w, "turn/start")
	if start.Params.ThreadID != "saved" {
		t.Fatalf("input targeted %q", start.Params.ThreadID)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"t2"}}}`, start.ID))
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"saved","turn":{"id":"t2"}}}`)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"saved","turn":{"id":"t2","status":"completed"}}}`)

	// Returning readmits the first session's events.
	appServerTestKeys(t, u, "/resume main\r")
	back := resumeTestOne(t, w, "thread/resume")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","cwd":"/work","turns":[]}}}`, back.ID))
	resumeTestSettle(t, u, w)
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"again"}}}`)
	if u.thread != "main" || u.turn != "again" {
		t.Fatalf("returned session still retired: thread=%q turn=%q", u.thread, u.turn)
	}
}

func TestAppServerResumeSwitchFailureKeepsSession(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	appServerTestUserMessage(t, u, "old-user", "", "keep this")
	appServerTestKeys(t, u, "/resume missing\r")
	resume := resumeTestOne(t, w, "thread/resume")
	appServerTestKeys(t, u, "held\r")
	appServerTestUserMessage(t, u, "during", "", "arrived while switching")
	if len(resumeTestRequests(t, w)) != 0 {
		t.Fatal("input escaped to the old thread while switching")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"no rollout"}}`, resume.ID))
	if u.thread != "main" || u.clearing || u.switching != "" || !strings.Contains(u.notice, "no rollout") || u.draft != "held" || len(u.view.entries) != 2 || u.view.entries[1].Text != "arrived while switching" {
		t.Fatalf("failed switch lost state: thread=%q notice=%q entries=%+v", u.thread, u.notice, u.view.entries)
	}
	appServerTestKeys(t, u, "\r")
	if start := resumeTestOne(t, w, "turn/start"); start.Params.ThreadID != "main" {
		t.Fatalf("input after failed switch targeted %q", start.Params.ThreadID)
	}
}

func TestAppServerResumeCommandRequiresIdleSession(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/resume\r")
	if u.resumePicker != nil || len(resumeTestRequests(t, w)) != 0 || !strings.Contains(u.notice, "disabled while a task is in progress") || strings.TrimSpace(u.draft) != "/resume" {
		t.Fatalf("busy /resume: picker=%v notice=%q draft=%q", u.resumePicker != nil, u.notice, u.draft)
	}
	u, w = newAppServerTestUI()
	appServerTestKeys(t, u, "/resume a b\r")
	if len(resumeTestRequests(t, w)) != 0 || !strings.Contains(u.notice, "Use /resume") {
		t.Fatalf("extra /resume arguments: %q", u.notice)
	}
}

// Switching root threads replaces the saved-Diff scope. Events already queued
// for the previous subscription must not reach the reset view, and the old
// session's children must not rejoin it.
func TestLiveDiffResetScopeResynchronizes(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffScopeCapture(t, store, workspace, "old", "old-call", filepath.Join(workspace, "old.go"), "a", "b")
	liveDiffScopeCapture(t, store, workspace, "new", "new-call", filepath.Join(workspace, "new.go"), "a", "b")
	liveDiffScopeCapture(t, store, workspace, "old-child", "old-child-call", filepath.Join(workspace, "old-child.go"), "a", "b")
	liveDiffScopeCapture(t, store, workspace, "new-child", "new-child-call", filepath.Join(workspace, "new-child.go"), "a", "b")
	auto, stop := newAutoLiveDiff(t.Context(), "")
	defer stop()
	auto.enable()
	auto.includeThread(workspace, "old", true)
	c := newLiveDiffTerminalController(store, "", os.Stdout)
	defer c.close()
	sub := auto.events.subscribe()
	apply := func(event liveDiffEvent) {
		t.Helper()
		if _, err := c.applyEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	paths := func() []string {
		var paths []string
		for _, file := range c.view.Files {
			paths = append(paths, filepath.Base(file.Path))
		}
		slices.Sort(paths)
		return paths
	}
	apply(<-sub.events)
	if got := paths(); !slices.Equal(got, []string{"old.go"}) {
		t.Fatalf("initial scope files: %v", got)
	}
	child := func(thread, parent string) codexTurnMetadata {
		return codexTurnMetadata{ThreadID: thread, ParentThreadID: parent, SubagentKind: "thread_spawn", RequestKind: "turn"}
	}
	auto.observe(workspace, "old-child", child("old-child", "old"))
	stale := <-sub.events // Queued before the switch; the UI loop may still read it.
	// Production order: reset, include the resumed root, then resubscribe on gap.
	c.resetScope()
	auto.resetScope()
	auto.includeThread(workspace, "new", true)
	select {
	case <-sub.gap:
	default:
		t.Fatal("reset did not detach the previous subscriber")
	}
	apply(stale)
	if len(c.view.Files) != 0 {
		t.Fatalf("stale scope reached the reset view: %v", paths())
	}
	auto.observe(workspace, "old-child", child("old-child", "old"))
	auto.observe(workspace, "new-child", child("new-child", "new"))
	sub = auto.events.subscribe()
	apply(<-sub.events)
	if got := paths(); !slices.Equal(got, []string{"new-child.go", "new.go"}) || auto.workspace != workspace {
		t.Fatalf("resynchronized scope files: %v", got)
	}
}

func TestAppServerResumeSwitchKeepsCurrentSessionLive(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	u.session.registerThread(appServerThreadInfo{ID: "child", ParentThreadID: "main", AgentNickname: "worker", AgentRole: "worker",
		Source: jsontext.Value(`{"subAgent":{"thread_spawn":{"agent_path":"/root/worker","agent_role":"worker"}}}`)})
	appServerTestKeys(t, u, "/resume saved\r")
	resume := resumeTestOne(t, w, "thread/resume")
	for range 300 {
		appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"child","turnId":"c","itemId":"m","delta":"x"}}`)
	}
	appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"saved-child","turnId":"c","itemId":"m","delta":"x"}}`)
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"saved","turnId":"old","item":{"type":"agentMessage","id":"answer","text":"Saved answer"}}}`)
	if len(u.resumePending) != 1 {
		t.Fatalf("switch buffered %d events; want only the chosen thread's item", len(u.resumePending))
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"saved","cwd":"/work","turns":[{"id":"old","status":"completed","items":[{"type":"agentMessage","id":"answer","text":"Saved answer"}]}]}}}`, resume.ID))
	resumeTestSettle(t, u, w)
	if u.thread != "saved" || u.status != "Ready" {
		t.Fatalf("switch state: thread=%q status=%q", u.thread, u.status)
	}
}

func TestAppServerResumeSwitchWaitsForDiffResync(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffScopeCapture(t, store, workspace, "saved", "saved-call", filepath.Join(workspace, "saved.go"), "a", "b")
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.agents = newLiveActivityView()
	auto, stop := newAutoLiveDiff(t.Context(), "")
	defer stop()
	auto.enable()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.shell.auto, u.shell.diff.store = auto, store
	u.session.start("main", workspace)
	auto.includeThread(workspace, "main", true)
	sub := auto.events.subscribe()
	u.shell.applyDiff(t.Context(), <-sub.events)
	appServerTestKeys(t, u, "/resume saved\r")
	resume := resumeTestOne(t, w, "thread/resume")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"saved","cwd":%q,"turns":[]}}}`, resume.ID, workspace))
	resumeTestSettle(t, u, w)
	if u.status != "Restoring saved Diff…" || u.restoring == nil {
		t.Fatalf("restoration finished before the Diff resynchronized: %q", u.status)
	}
	select {
	case <-sub.gap:
	default:
		t.Fatal("switch did not resynchronize the Diff subscription")
	}
	sub = auto.events.subscribe()
	for _, event := range auto.events.takePreviews(sub) {
		u.shell.applyDiff(t.Context(), event)
	}
	if err := u.finishRestoredContent(); err != nil {
		t.Fatal(err)
	}
	if u.status != "Ready" || u.shell.diffFailure != "" || len(u.shell.diff.view.Files) != 1 || filepath.Base(u.shell.diff.view.Files[0].Path) != "saved.go" {
		t.Fatalf("resumed Diff: status=%q failure=%q files=%+v", u.status, u.shell.diffFailure, u.shell.diff.view.Files)
	}
	// A later /clear session still joins the Diff, as do its children.
	appServerTestKeys(t, u, "/clear\r")
	start := resumeTestOne(t, w, "thread/start")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":%q}}}`, start.ID, workspace))
	auto.observe(workspace, "fresh-child", codexTurnMetadata{ThreadID: "fresh-child", ParentThreadID: "fresh", SubagentKind: "thread_spawn", RequestKind: "turn"})
	auto.mu.Lock()
	admitted := auto.scope.Workspaces[workspace]["fresh"] && auto.scope.Workspaces[workspace]["fresh-child"]
	auto.mu.Unlock()
	if !admitted {
		t.Fatal("session cleared after a switch never entered the Diff")
	}
}

func TestAppServerResumePickerFailedPageStopsPaging(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.start("main", "/work")
	if err := u.openResumePicker(false); err != nil {
		t.Fatal(err)
	}
	first := resumeTestOne(t, w, "thread/list")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"a","preview":"A"}],"nextCursor":"page-2"}}`, first.ID))
	next := resumeTestOne(t, w, "thread/list")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"store offline"}}`, next.ID))
	for range 3 {
		if err := u.fillResumePicker(); err != nil {
			t.Fatal(err)
		}
	}
	if requests := resumeTestRequests(t, w); len(requests) != 0 || !strings.Contains(u.resumePicker.problem, "store offline") {
		t.Fatalf("failed page retried: %+v problem=%q", requests, u.resumePicker.problem)
	}
	resumeTestKeys(t, u, "\t")
	resumeTestOne(t, w, "thread/list")
}
