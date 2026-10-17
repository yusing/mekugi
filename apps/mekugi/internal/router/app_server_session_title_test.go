package router

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
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
	if u.title != "Generated title" || u.Requests.Pending() || !u.dirty {
		t.Fatalf("confirmation state: title=%q pending=%v requests=%v dirty=%v", u.title, u.Requests, u.Requests, u.dirty)
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
			u.title, u.sessionPresentation().Status.Text, u.Turn, u.Text = "Confirmed", "Working", "active-turn", "unsent input"
			u.dirty = false
			if immediate {
				u.Host.Input = new(sessionTitleFailInput)
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
			if u.title != "Confirmed" || u.sessionPresentation().Status.Text != "Working" || u.Turn != "active-turn" || u.Text != "unsent input" || u.feedback.Text == "" || u.feedback.Alert || !u.dirty || u.Requests.Pending() {
				t.Fatalf("save failure disturbed session: title=%q status=%q turn=%q draft=%q notice=%q alert=%v dirty=%v", u.title, u.sessionPresentation().Status.Text, u.Turn, u.Text, u.feedback.Text, u.feedback.Alert, u.dirty)
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
			u.sessionPresentation().Status.Text, u.dirty = "Ready", false
			if immediate {
				u.Host.Input = new(sessionTitleFailInput)
			}
			u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Candidate"})
			if !immediate {
				request := resumeTestOne(t, wire, "thread/name/set")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, request.ID))
			}
			if !u.dirty || u.feedback.Text == "" || u.feedback.Alert || u.Turn != "" || u.sessionPresentation().Status.Text != "Ready" {
				t.Fatalf("idle failure will not repaint: dirty=%v notice=%q status=%q", u.dirty, u.feedback.Text, u.sessionPresentation().Status.Text)
			}
		})
	}
}

func TestAppServerSessionTitleOldThreadIsolation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Old generated title"})
	request := resumeTestOne(t, wire, "thread/name/set")
	u.Thread, u.title, u.dirty = "replacement", "Replacement title", false
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Old host title"}}`)
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"","threadName":"Unscoped title"}}`)
	if u.title != "Replacement title" || u.dirty || u.Requests.Pending() {
		t.Fatalf("old thread renamed Main: title=%q dirty=%v pending=%v", u.title, u.dirty, u.Requests)
	}
}

func TestAppServerSessionTitleManualRenameWinsGeneration(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.titleGenerator = newTestSessionNaming(t.Context(), nil)
	u.titleGenerator.register(binding.ThreadInfo{ID: "main"})
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Manual host title"}}`)
	u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Late generated title"})
	if u.title != "Manual host title" || wire.Len() != 0 || u.Requests.Pending() {
		t.Fatalf("late generation overwrote manual rename: title=%q wire=%q pending=%v", u.title, wire.String(), u.Requests)
	}
}

func TestAppServerSessionTitleSkippedNamingPreservesHostMetadata(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.titleGenerator = newTestSessionNaming(t.Context(), nil)
	u.titleGenerator.register(binding.ThreadInfo{ID: "main"})
	u.titleUpdates = u.titleGenerator.updates
	u.sessionPresentation().Status.Text, u.Turn, u.Text = "Working", "active-turn", "unsent input"
	u.setNotice("Existing host notice", false)
	u.dirty = false
	u.titleGenerator.ObserveInput("main", responses.MustMarshalJSON("First user message"))
	u.titleGenerator.Completed("main", nil)
	select {
	case update := <-u.titleUpdates:
		t.Fatalf("unavailable naming emitted update: %+v", update)
	default:
	}
	if u.dirty || u.feedback.Text != "Existing host notice" || wire.Len() != 0 {
		t.Fatalf("skipped naming changed UI: dirty=%v notice=%q wire=%q", u.dirty, u.feedback.Text, wire.String())
	}
	appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Saved host title"}}`)
	if u.title != "Saved host title" || !u.dirty || u.feedback.Text != "Existing host notice" || u.sessionPresentation().Status.Text != "Working" || u.Turn != "active-turn" || u.Text != "unsent input" || wire.Len() != 0 || u.Requests.Pending() {
		t.Fatalf("host metadata after skipped naming: title=%q dirty=%v notice=%q status=%q turn=%q draft=%q wire=%q", u.title, u.dirty, u.feedback.Text, u.sessionPresentation().Status.Text, u.Turn, u.Text, wire.String())
	}
}

func TestAppServerSessionTitleStartResumeMetadata(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume"} {
		for _, name := range []string{"Saved host title", ""} {
			t.Run(method+"/"+name, func(t *testing.T) {
				u, _ := newAppServerTestUI()
				u.title, u.Startup.Thread = "Previous thread title", "saved"
				appServerTestRequest(t, u, "1", method)
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":1,"result":{"thread":{"id":"saved","name":%q}}}`, name))
				if u.Thread != "saved" || u.title != name {
					t.Fatalf("metadata title: thread=%q title=%q want=%q", u.Thread, u.title, name)
				}
			})
		}
	}
}

func TestAppServerSessionTitleComposerRename(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.title = "Saved title"
			u.titleGenerator = newTestSessionNaming(t.Context(), nil)
			u.titleGenerator.register(binding.ThreadInfo{ID: "main"})
			if busy {
				u.Turn, u.sessionPresentation().Status.Text = "active-turn", "Working"
			}
			appServerTestKeys(t, u, "/title 修復 session title\r")
			request := sessionTitleTestOne(t, wire)
			if request.Params.ThreadID != "main" || request.Params.Name != "修復 session title" || u.Text != "" || u.title != "Saved title" || u.titleGenerator.acceptCandidate("main") {
				t.Fatalf("manual rename: request=%+v draft=%q saved=%q", request, u.Text, u.title)
			}
			if busy && (u.Turn != "active-turn" || u.sessionPresentation().Status.Text != "Working") {
				t.Fatal("rename disturbed the running task")
			}
			// The header updates before the metadata response, not merely after it.
			if !strings.Contains(u.mainHeaderRight(120, true), "修復 session title") {
				t.Fatal("manual title not immediately visible")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
			if u.title != "修復 session title" || u.Names.Pending("main") != "" {
				t.Fatalf("manual title not confirmed: title=%q pending=%v", u.title, u.Names.Pending("main"))
			}
			u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Late generated title"})
			if wire.Len() != 0 {
				t.Fatal("manual rename did not suppress automatic naming")
			}
		})
	}
}

type sessionTitleTestRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		ThreadID string `json:"threadId"`
		Name     string `json:"name"`
	} `json:"params"`
}

func sessionTitleTestOne(t *testing.T, wire *appServerTestInput) sessionTitleTestRequest {
	t.Helper()
	requests := appServerDrainRequests[sessionTitleTestRequest](t, wire)
	if len(requests) != 1 || requests[0].Method != "thread/name/set" {
		t.Fatalf("requests = %+v, want one thread/name/set", requests)
	}
	return requests[0]
}

func TestAppServerSessionTitleBeforeSessionStart(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume"} {
		t.Run(method, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Thread, u.Startup.Thread = "", "saved"
			u.titleGenerator = newTestSessionNaming(t.Context(), nil)
			appServerTestKeys(t, u, "/title First name\r/title Final name\r")
			if wire.Len() != 0 || u.Names.Pending("") != "Final name" || !strings.Contains(u.mainHeaderRight(120, true), "Final name") {
				t.Fatalf("pre-session title: pending=%q wire=%q", u.Names.Pending(""), wire.String())
			}
			appServerTestRequest(t, u, "100", method)
			appServerTestMessage(t, u, `{"id":100,"result":{"thread":{"id":"saved","name":""}}}`)
			requests := appServerDrainRequests[sessionTitleTestRequest](t, wire)
			wantRequests := 2
			if method == "thread/resume" {
				wantRequests = 3 // Resume also loads the agent roster.
			}
			if len(requests) != wantRequests || requests[0].Method != "thread/name/set" || requests[0].Params.ThreadID != "saved" || requests[0].Params.Name != "Final name" || requests[1].Method != "model/list" {
				t.Fatalf("start title persistence: %+v", requests)
			}
			if u.Names.Pending("") != "" || u.titleGenerator.acceptCandidate("saved") || !strings.Contains(u.mainHeaderRight(120, true), "Final name") {
				t.Fatal("thread creation lost the manual title or enabled generation")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, requests[0].ID))
			// Fresh process registration uses only the durable host name.
			fresh := newTestSessionNaming(t.Context(), nil)
			fresh.register(binding.ThreadInfo{ID: "saved", Name: u.title})
			if fresh.acceptCandidate("saved") {
				t.Fatal("fresh process would regenerate the saved manual title")
			}
		})
	}
}

func TestAppServerSessionTitleEmptyAndCommandBoundaries(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/title\r")
	if u.feedback.Text != "Usage: /title <title>" || u.Text != "/title " || wire.Len() != 0 {
		t.Fatalf("empty title: draft=%q notice=%q wire=%q", u.Text, u.feedback.Text, wire.String())
	}
	if u.titleCommand("/titles wrong") || u.titleCommand("prompt /title wrong") {
		t.Fatal("non-command interpreted as a rename")
	}
	appServerTestKeys(t, u, "\x03\x1b[200~/title\t  修復\n標題  \x1b[201~\r")
	request := sessionTitleTestOne(t, wire)
	if request.Params.Name != "修復\n標題" {
		t.Fatalf("whitespace title = %+v", request)
	}
}

func TestAppServerSessionTitleManualSaveFailure(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprint(immediate), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.title, u.Turn, u.sessionPresentation().Status.Text = "Saved title", "active-turn", "Working"
			if immediate {
				u.Host.Input = new(sessionTitleFailInput)
			}
			appServerTestKeys(t, u, "/title Unsaved title\r")
			if !immediate {
				request := sessionTitleTestOne(t, wire)
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, request.ID))
			}
			if u.title != "Saved title" || u.Names.Pending("main") != "" || u.Requests.Pending() || u.feedback.Text == "" || u.feedback.Alert || !u.dirty || u.Turn != "active-turn" || u.sessionPresentation().Status.Text != "Working" {
				t.Fatalf("manual save failure: title=%q pending=%v notice=%q turn=%q status=%q", u.title, u.Names.Pending("main"), u.feedback.Text, u.Turn, u.sessionPresentation().Status.Text)
			}
		})
	}
}

func TestAppServerSessionTitleSerializesManualRenames(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("automatic=%v/failed=%v", automatic, failed), func(t *testing.T) {
				u, wire := newAppServerTestUI()
				if automatic {
					u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Older name"})
				} else {
					appServerTestKeys(t, u, "/title Older name\r")
				}
				older := sessionTitleTestOne(t, wire)
				appServerTestKeys(t, u, "/title Latest name\r")
				if wire.Len() != 0 || !strings.Contains(u.mainHeaderRight(120, true), "Latest name") {
					t.Fatal("rename was not immediately displayed and serialized")
				}
				response := `"result":{}`
				if failed {
					response = `"error":{"code":-1,"message":"older failure"}`
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, older.ID, response))
				latest := sessionTitleTestOne(t, wire)
				if latest.Params.Name != "Latest name" || u.feedback.Text != "" {
					t.Fatalf("queued rename: request=%+v notice=%q", latest, u.feedback.Text)
				}
				if !failed {
					appServerTestMessage(t, u, `{"method":"thread/name/updated","params":{"threadId":"main","threadName":"Older name"}}`)
				}
				if !strings.Contains(u.mainHeaderRight(120, true), "Latest name") {
					t.Fatal("older confirmation overwrote pending manual display")
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, latest.ID))
				if u.title != "Latest name" || u.Names.Pending("main") != "" {
					t.Fatal("latest title not confirmed")
				}
			})
		}
	}
}

func TestAppServerSessionTitleManualThreadIsolation(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/title Old thread name\r")
	request := sessionTitleTestOne(t, wire)
	u.Thread, u.title, u.dirty = "replacement", "Replacement name", false
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	if u.title != "Replacement name" || u.dirty || u.Names.Pending("main") != "" {
		t.Fatal("old manual rename changed the replacement thread")
	}
	u.Replacement.Clear()
	appServerTestKeys(t, u, "/title During replacement\r")
	if u.Text != "/title During replacement" || wire.Len() != 0 || u.Names.Pending("main") != "" {
		t.Fatal("replacement rename targeted the previous thread or lost the command")
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
		{"manual-pending", "Previously saved title", 120, false},
		{"manual-before-start", "", 120, false},
		{"wide-scrolled", "Review the native session title lifecycle", 160, true},
		{"narrow-scrolled", "Review the native session title lifecycle", 36, true},
		{"tiny-scrolled", "Review the native session title lifecycle", 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
			u.sessionPresentation().Status.Text, u.model, u.title = "Ready", "snapshot-model", tc.title
			if tc.name == "idle-save-failure" {
				u.Host.Input = new(sessionTitleFailInput)
				u.persistSessionTitle(sessionTitleUpdate{thread: "main", name: "Candidate"})
			}
			if strings.HasPrefix(tc.name, "manual-") {
				if tc.name == "manual-before-start" {
					u.Thread = ""
					u.sessionPresentation().Status.SetText("Starting thread…")
				}
				appServerTestKeys(t, u, "/title My manual session name\r")
			}

			if tc.scroll {
				var entries []sessionpresentation.ActivityEntry
				for i := range 30 {
					entries = append(entries, sessionpresentation.ActivityEntry{Seq: uint64(i + 1), Agent: "Main", Kind: "text", Text: fmt.Sprintf("Earlier message %02d", i+1), Observed: u.now()})
				}
				u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: entries})
				u.shell.Transcript.Following, u.shell.Transcript.Offset, u.shell.Transcript.Unseen = false, 0, 2
			}
			screen := vt.NewEmulator(tc.width, 16)
			defer screen.Close()
			if err := u.testPaint(screen, tc.width, 16); err != nil {
				t.Fatal(err)
			}
			uisnapshot.Assert(t, "testdata/snapshots/native-session-title-"+tc.name+".txt", strings.TrimSuffix(screen.String(), "\n")+"\n")
		})
	}
}
