package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func rewindTestUser(t *testing.T, u *appServerUI) {
	t.Helper()
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"first","item":{"type":"userMessage","id":"user","content":[{"type":"text","text":"Implement the parser"}]}}}`)
}

func rewindTestTurns(t *testing.T, u *appServerUI, w *appServerTestInput) {
	t.Helper()
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	if read.Params.ThreadID != "main" || read.Params.IncludeTurns == nil || !*read.Params.IncludeTurns {
		t.Fatalf("turn read = %+v", read)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","turns":[
		{"id":"first","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"Implement the parser"}]}]},
		{"id":"second","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"Change the error format"}]}]}
	]}}}`, read.ID))
}

func TestUISnapshotNativeRewind(t *testing.T) {
	for _, width := range []int{48, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local) }
			u.model, u.reasoningEffort, u.serviceTier = "model", "high", "priority"
			rewindTestTurns(t, u, w)
			rows, _ := u.testMainFrame(width, 18, 0)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-picker-%d.txt", width), rows, width)
			appServerTestKeys(t, u, "\x1b[B\r")
			rows, _ = u.testMainFrame(width, 18, 0)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-edits-unavailable-%d.txt", width), rows, width)
			appServerTestKeys(t, u, "\r")
			requests := appServerDrainRequests[struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}](t, w)
			if len(requests) != 1 || requests[0].Method != "thread/fork" || requests[0].Params["beforeTurnId"] != "second" || requests[0].Params["threadId"] != "main" || requests[0].Params["model"] != "model" || requests[0].Params["serviceTier"] != "priority" {
				t.Fatalf("rewind fork = %+v", requests)
			}
			appServerTestKeys(t, u, "preserved draft")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","serviceTier":"priority","thread":{"id":"branch","cwd":"/work","turns":[{"id":"first","status":"completed","items":[{"type":"agentMessage","id":"answer","text":"Parser implemented"}]}]}}}`, requests[0].ID))
			for _, method := range resumeTestSettle(t, u, w) {
				if method == "turn/start" || method == "thread/archive" {
					t.Fatalf("rewind unexpectedly dispatched %s", method)
				}
			}
			if u.Thread != "branch" || u.Text != "preserved draft" || u.Replacement.Pending() || u.picker.Kind != "archive" || u.picker.archiveTarget != "main" || u.shell.Main.Menu.Selected != 0 {
				t.Fatalf("rewind state = %s %q %+v", u.Thread, u.Text, u.picker)
			}
			if u.HasUserTurn {
				t.Fatal("branch without user messages retained original rewind eligibility")
			}
			appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"second","item":{"type":"agentMessage","id":"late","text":"Excluded original response"}}}`)
			rows, _ = u.testMainFrame(width, 18, 0)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-archive-%d.txt", width), rows, width)
			appServerTestKeys(t, u, "\x1b[B\r")
			archive := resumeTestOne(t, w, "thread/archive")
			if archive.Params.ThreadID != "main" {
				t.Fatal("archive targeted the branch")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"archive unavailable"}}`, archive.ID))
			if u.quitRequested || u.archived || u.Thread != "branch" || !strings.Contains(u.attached.feedback.Text, "main still exists") || u.Text != "preserved draft" {
				t.Fatal("archive failure lost the branch or original identity")
			}
			appServerTestKeys(t, u, "\r")
			if start := resumeTestOne(t, w, "turn/start"); start.Params.ThreadID != "branch" {
				t.Fatal("new branch unusable after archive failure")
			}
		})
	}
}

func TestAppServerRewindCancellationAndLateRead(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	appServerTestKeys(t, u, "\x1bnew draft")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","turns":[]}}}`, read.ID))
	if u.Thread != "main" || u.Text != "new draft" || u.picker.Kind != "" || u.Replacement.Pending() || w.Len() != 0 {
		t.Fatal("cancelled read changed session or input")
	}
	u.Editor.Take()
	rewindTestTurns(t, u, w)
	appServerTestKeys(t, u, "\x1b")
	if u.Thread != "main" || w.Len() != 0 || u.Startup.RewindOriginal != "" {
		t.Fatal("picker cancellation forked or archived")
	}
}

func TestAppServerRewindFailureAndArchiveChoice(t *testing.T) {
	for _, outcome := range []string{"fork-failure", "settings-failure", "keep", "archive"} {
		t.Run(outcome, func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			u.reasoningEffort = "high"
			if outcome == "settings-failure" {
				u.reasoningEffort = ""
			}
			rewindTestTurns(t, u, w)
			appServerTestKeys(t, u, "\r\r")
			fork := resumeTestOne(t, w, "thread/fork")
			appServerTestKeys(t, u, "waiting")
			if outcome == "fork-failure" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unsupported boundary"}}`, fork.ID))
			} else {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","thread":{"id":"branch","cwd":"/work"}}}`, fork.ID))
				if outcome == "settings-failure" {
					resume := resumeTestOne(t, w, "thread/resume")
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"settings unavailable"}}`, resume.ID))
					if release := resumeTestOne(t, w, "thread/unsubscribe"); release.Params.ThreadID != "branch" {
						t.Fatal("failed switch released original")
					}
				} else {
					resumeTestSettle(t, u, w)
				}
			}
			if strings.HasSuffix(outcome, "failure") {
				if u.Thread != "main" || u.Text != "waiting" || u.Replacement.Pending() || u.Startup.RewindOriginal != "" || u.picker.Kind == "archive" || w.Len() != 0 {
					t.Fatal("failed rewind lost original or offered archive")
				}
				return
			}
			keys := "\r" // No is selected initially.
			if outcome == "archive" {
				keys = "\x1b[B\r"
			}
			appServerTestKeys(t, u, keys)
			if outcome == "archive" {
				archive := resumeTestOne(t, w, "thread/archive")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, archive.ID))
			}
			if u.Thread != "branch" || u.Text != "waiting" || u.quitRequested || u.archived || w.Len() != 0 || u.picker.Kind != "" {
				t.Fatal("archive choice changed new branch or input")
			}
		})
	}
}

func TestAppServerRewindUnavailableHistory(t *testing.T) {
	for _, response := range []string{
		`"error":{"code":-1,"message":"unavailable"}`,
		`"result":{"thread":{"id":"other"}}`,
		`"result":{"thread":{"id":"main","turns":[]}}`,
	} {
		t.Run(response, func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			rewindTestUser(t, u)
			appServerTestKeys(t, u, "/rewind\r")
			read := resumeTestOne(t, w, "thread/read")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, read.ID, response))
			appServerTestKeys(t, u, "\r")
			if u.picker.Loading || u.picker.Problem == "" || len(u.picker.Choices) != 0 || w.Len() != 0 || u.Thread != "main" {
				t.Fatal("missing history produced a rewind or no diagnostic")
			}
		})
	}
}

func TestAppServerRewindBeforeFirstUserTurn(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	appServerTestKeys(t, u, "/rewind\r")
	if w.Len() != 0 || u.picker.Kind == "rewind" || !strings.Contains(u.attached.feedback.Text, "disabled before the first user turn") {
		t.Fatalf("empty-session rewind: requests=%s picker=%+v feedback=%s", w.String(), u.picker, u.attached.feedback.Text)
	}
	u.Editor.Take()
	rewindTestTurns(t, u, w)
}

func TestAppServerRewindQuestionAnswer(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	text := lifecycleReply(t, "question", 0, "How?", "Autosquash, including descendant commits")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","turns":[{"id":"answer","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":%q}]}]}]}}}`, read.ID, text))
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Display != "Autosquash, including descendant commits" {
		t.Fatalf("answer choices = %+v", u.picker.Choices)
	}
	rows, _ := u.testMainFrame(100, 18, 0)
	frame := strings.Join(rows, "\n")
	if !strings.Contains(frame, "Autosquash, including descendant commits") || strings.Contains(frame, "send_user_message_question_reply") {
		t.Fatalf("answer dialog = %s", frame)
	}
	appServerTestKeys(t, u, "\r\r")
	if fork := resumeTestOne(t, w, "thread/fork"); fork.Params.BeforeTurnID != "answer" {
		t.Fatalf("answer fork = %+v", fork)
	}
}
