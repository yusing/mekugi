package router

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestAppServerSessionTitleHostConfirmation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.title = "Existing title"
	u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Generated title"})
	requests := appServerDrainRequests[struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
			Name     string `json:"name"`
		} `json:"params"`
	}](t, wire)
	if len(requests) != 1 || requests[0].Method != "thread/name/set" || requests[0].Params.ThreadID != "main" || requests[0].Params.Name != "Generated title" {
		t.Fatalf("title persistence request = %+v", requests)
	}
	if u.title != "Existing title" {
		t.Fatalf("unconfirmed title displayed: %q", u.title)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, requests[0].ID))
	if u.title != "Generated title" || len(u.titleRequests) != 0 || len(u.requests) != 0 || !u.dirty {
		t.Fatalf("confirmation state: title=%q pending=%v requests=%v dirty=%v", u.title, u.titleRequests, u.requests, u.dirty)
	}
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Host rename"}}`)
	if u.title != "Host rename" {
		t.Fatalf("host rename not routed: %q", u.title)
	}
}

func TestAppServerSessionTitleSaveFailureNonfatal(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprint(immediate), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.title, u.status, u.turn, u.draft = "Confirmed", "Working", "active-turn", "unsent input"
			u.dirty = false
			if immediate {
				u.client.Input = new(sessionTitleFailInput)
			}
			u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Candidate"})
			if immediate {
				if wire.Len() != 0 {
					t.Fatal("failed transport wrote metadata")
				}
			} else {
				request := resumeTestOne(t, wire, "thread/name/set")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, request.ID))
			}
			if u.title != "Confirmed" || u.status != "Working" || u.turn != "active-turn" || u.draft != "unsent input" || u.notice == "" || u.noticeAlert || !u.dirty || len(u.titleRequests) != 0 || len(u.requests) != 0 {
				t.Fatalf("save failure disturbed session: title=%q status=%q turn=%q draft=%q notice=%q alert=%v dirty=%v", u.title, u.status, u.turn, u.draft, u.notice, u.noticeAlert, u.dirty)
			}
		})
	}
}

type sessionTitleFailInput struct{ appServerTestInput }

func (*sessionTitleFailInput) Write([]byte) (int, error) {
	return 0, errors.New("metadata transport unavailable")
}

func TestAppServerSessionTitleIdleSaveFailureRepaints(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprint(immediate), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.status, u.dirty = "Ready", false
			if immediate {
				u.client.Input = new(sessionTitleFailInput)
			}
			u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Candidate"})
			if !immediate {
				request := resumeTestOne(t, wire, "thread/name/set")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, request.ID))
			}
			if !u.dirty || u.notice == "" || u.noticeAlert || u.turn != "" || u.status != "Ready" {
				t.Fatalf("idle failure will not repaint: dirty=%v notice=%q status=%q", u.dirty, u.notice, u.status)
			}
		})
	}
}

func TestAppServerSessionTitleOldThreadIsolation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Old generated title"})
	request := resumeTestOne(t, wire, "thread/name/set")
	u.thread, u.title, u.dirty = "replacement", "Replacement title", false
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Old host title"}}`)
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"","threadName":"Unscoped title"}}`)
	if u.title != "Replacement title" || u.dirty || len(u.titleRequests) != 0 {
		t.Fatalf("old thread renamed Main: title=%q dirty=%v pending=%v", u.title, u.dirty, u.titleRequests)
	}
}

func TestAppServerSessionTitleManualRenameWinsGeneration(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.titleGenerator = newSessionTitleGenerator(t.Context(), nil, nil)
	u.titleGenerator.register(appServerThreadInfo{ID: "main"})
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Manual host title"}}`)
	u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Late generated title"})
	if u.title != "Manual host title" || wire.Len() != 0 || len(u.titleRequests) != 0 {
		t.Fatalf("late generation overwrote manual rename: title=%q wire=%q pending=%v", u.title, wire.String(), u.titleRequests)
	}
}

func TestAppServerSessionTitleSkippedNamingPreservesHostMetadata(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.titleGenerator = newSessionTitleGenerator(t.Context(), nil, nil)
	u.titleGenerator.register(appServerThreadInfo{ID: "main"})
	u.titleUpdates = u.titleGenerator.updates
	u.status, u.turn, u.draft = "Working", "active-turn", "unsent input"
	u.setNotice("Existing host notice", false)
	u.dirty = false
	u.titleGenerator.observe("main", "First user message", nil, true)
	select {
	case update := <-u.titleUpdates:
		t.Fatalf("unavailable naming emitted update: %+v", update)
	default:
	}
	if u.dirty || u.notice != "Existing host notice" || wire.Len() != 0 {
		t.Fatalf("skipped naming changed UI: dirty=%v notice=%q wire=%q", u.dirty, u.notice, wire.String())
	}
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Saved host title"}}`)
	if u.title != "Saved host title" || !u.dirty || u.notice != "Existing host notice" || u.status != "Working" || u.turn != "active-turn" || u.draft != "unsent input" || wire.Len() != 0 || len(u.titleRequests) != 0 {
		t.Fatalf("host metadata after skipped naming: title=%q dirty=%v notice=%q status=%q turn=%q draft=%q wire=%q", u.title, u.dirty, u.notice, u.status, u.turn, u.draft, wire.String())
	}
}

func TestAppServerSessionTitleStartResumeMetadata(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume"} {
		for _, name := range []string{"Saved host title", ""} {
			t.Run(method+"/"+name, func(t *testing.T) {
				u, _ := newAppServerTestUI()
				u.title, u.resumeThread = "Previous thread title", "saved"
				u.requests["1"] = method
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":1,"result":{"thread":{"id":"saved","name":%q}}}`, name))
				if u.thread != "saved" || u.title != name {
					t.Fatalf("metadata title: thread=%q title=%q want=%q", u.thread, u.title, name)
				}
			})
		}
	}
}

func TestUISnapshotNativeSessionTitle(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		width       int
		scroll      bool
	}{
		{"wide", "Review the native session title lifecycle", 160, false},
		{"narrow", "Review the native session title lifecycle", 36, false},
		{"unicode-controls", "  修復\t標題\n🙂\x1b[31m safely\x1b[0m\x07  ", 80, false},
		{"unnamed", "", 80, false},
		{"idle-save-failure", "", 120, false},
		{"wide-scrolled", "Review the native session title lifecycle", 160, true},
		{"narrow-scrolled", "Review the native session title lifecycle", 36, true},
		{"tiny-scrolled", "Review the native session title lifecycle", 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
			u.status, u.model, u.title = "Ready", "snapshot-model", tc.title
			if tc.name == "idle-save-failure" {
				u.client.Input = new(sessionTitleFailInput)
				u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Candidate"})
			}
			u.ensureShell()
			u.shell.side, u.shell.activityOpen, u.shell.journalOpen = false, false, false
			if tc.scroll {
				var entries []activityPaneEntry
				for i := range 30 {
					entries = append(entries, activityPaneEntry{Seq: uint64(i + 1), Agent: "Main", Kind: "text", Text: fmt.Sprintf("Earlier message %02d", i+1), Observed: u.now()})
				}
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: entries})
				u.view.following, u.view.offset, u.view.unseen = false, 0, 2
			}
			screen := vt.NewEmulator(tc.width, 16)
			defer screen.Close()
			if err := u.paint(screen, tc.width, 16); err != nil {
				t.Fatal(err)
			}
			uisnapshot.Assert(t, "testdata/snapshots/native-session-title-"+tc.name+".txt", strings.TrimSuffix(screen.String(), "\n")+"\n")
		})
	}
}
