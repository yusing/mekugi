package launch

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// Deliver metadata and full-item pages through the picker acquisition path.
func rewindTestHistory(t *testing.T, u *appServerUI, w *appServerTestInput, response string) {
	t.Helper()
	var message struct {
		ID     int `json:"id"`
		Result struct {
			Thread map[string]any `json:"thread"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(response), &message); err != nil {
		t.Fatal(err)
	}
	turns := message.Result.Thread["turns"]
	delete(message.Result.Thread, "turns")
	message.Result.Thread["historyMode"] = "paginated"
	metadata, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	appServerTestMessage(t, u, string(metadata))
	page := resumeTestOne(t, w, "thread/turns/list")
	data, err := json.Marshal(map[string]any{"id": page.ID, "result": map[string]any{"data": turns}})
	if err != nil {
		t.Fatal(err)
	}
	appServerTestMessage(t, u, string(data))
}

func rewindTestUser(t *testing.T, u *appServerUI) {
	t.Helper()
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"first","item":{"type":"userMessage","id":"user","content":[{"type":"text","text":"Implement the parser"}]}}}`)
}

func rewindTestTurns(t *testing.T, u *appServerUI, w *appServerTestInput) {
	t.Helper()
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	if read.Params.ThreadID != "main" || read.Params.IncludeTurns != nil && *read.Params.IncludeTurns {
		t.Fatalf("turn read = %+v", read)
	}
	rewindTestHistory(t, u, w, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","turns":[
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
			u.settings.Model, u.settings.Effort, u.settings.Tier = "model", "high", "priority"
			rewindTestTurns(t, u, w)
			rows, _ := u.testMainFrame(width, 18, 0)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-picker-%d.txt", width), rows, width)
			appServerTestKeys(t, u, "\x1b[B\r")
			rows, _ = u.testMainFrame(width, 18, 0)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-edits-unavailable-%d.txt", width), rows, width)
			appServerTestKeys(t, u, "\r")
			rewind := resumeTestOne(t, w, "thread/revert")
			if rewind.Params.ThreadID != "main" || rewind.Params.BeforeTurnID != "second" {
				t.Fatalf("rewind: %+v", rewind)
			}
			appServerTestKeys(t, u, "preserved draft")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","cwd":"/work","turns":[{"id":"first","status":"completed","items":[{"type":"agentMessage","id":"answer","text":"Parser implemented"}]}]}}}`, rewind.ID))
			resumeTestSettle(t, u, w)
			if u.Thread != "main" || u.Text != "preserved draft" || u.Replacement.Pending() || u.picker.Kind != "" || u.HasUserTurn || u.settings.Model != "model" || u.settings.Effort != "high" || u.settings.Tier != "priority" {
				t.Fatalf("rewind lost session or draft: %s %q %+v", u.Thread, u.Text, u.Live)
			}
			rows, _ = u.testMainFrame(width, 18, 0)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-ready-%d.txt", width), rows, width)
			appServerTestKeys(t, u, "\r")
			if start := resumeTestOne(t, w, "turn/start"); start.Params.ThreadID != "main" {
				t.Fatal("rewound session unusable")
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
	if u.Thread != "main" || w.Len() != 0 {
		t.Fatal("picker cancellation forked or archived")
	}
}

func TestAppServerRewindFailure(t *testing.T) {
	for _, response := range []string{`"error":{"code":-1,"message":"invalid turn"}`, `"result":{"thread":{"id":"foreign"}}`, `"result":{"thread":{}}`} {
		t.Run(response, func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			u.controlObservations = new(sessioncontrol.Observations)
			u.controlObservations.RefreshContext("main")
			rewindTestTurns(t, u, w)
			appServerTestKeys(t, u, "\r\r")
			rewind := resumeTestOne(t, w, "thread/revert")
			appServerTestKeys(t, u, "waiting")
			revision := u.controlObservations.ContextRevision("main")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, rewind.ID, response))
			if u.controlObservations.ContextRevision("main") != revision {
				t.Fatal("rejected rewind refreshed context")
			}
			if u.Thread != "main" || u.Text != "waiting" || u.Replacement.Pending() || u.picker.Kind != "" || w.Len() != 0 {
				t.Fatal("rejected rewind changed session or input")
			}
			if u.attached.presentation.Status.Text != "Ready" {
				t.Fatal("rejected rewind retained a pending status")
			}
		})
	}
}

func TestAppServerRewindReloadFailureKeepsSession(t *testing.T) {
	for _, failure := range []string{`"error":{"code":-1,"message":"history unavailable"}`, `"result":{"data":"invalid"}`} {
		t.Run(failure, func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			rewindTestTurns(t, u, w)
			appServerTestKeys(t, u, "\r\r")
			rewind := resumeTestOne(t, w, "thread/revert")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","cwd":"/work","historyMode":"paginated"}}}`, rewind.ID))
			appServerTestKeys(t, u, "preserved draft")
			page := resumeTestOne(t, w, "thread/turns/list")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, page.ID, failure))
			resumeTestSettle(t, u, w)
			if u.Thread != "main" || u.Replacement.Pending() || u.Text != "preserved draft" || !strings.Contains(u.attached.feedback.Text, "Conversation history incomplete") {
				t.Fatal("history failure discarded the accepted thread or hid its recovery")
			}
			appServerTestKeys(t, u, "\r")
			if next := resumeTestOne(t, w, "turn/start"); next.Params.ThreadID != "main" {
				t.Fatal("history failure left the runtime unusable")
			}
		})
	}
}

func TestAppServerRewindPreservesExecutionReaders(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	models, summaries := 0, 0
	hooks := u.attached.hooks
	u.attached.hooks.modelsReady = func([]appServerModel, preferences.Applied, *inferencegateway.Tiers, string) { models++ }
	u.attached.hooks.compaction = func(string, string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { summaries++; return "retained summary", nil }
	}
	side := new(sessioncontrol.Side)
	u.attached.sideInputs = map[*sessioncontrol.Side]bool{side: true}
	rewindTestTurns(t, u, w)
	appServerTestKeys(t, u, "\r\r")
	rewind := resumeTestOne(t, w, "thread/revert")
	mainInput := &threadInput{admitted: true}
	u.attached.threadInput = mainInput
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","cwd":"/work"}}}`, rewind.ID))
	resumeTestSettle(t, u, w)
	if u.attached.hooks != hooks || u.attached.threadInput == mainInput {
		t.Fatal("rewind copied hooks or retained Main input/interrupt state")
	}
	if models == 0 || u.attached.hooks.compaction == nil {
		t.Fatal("rewind detached model or compaction readers")
	}
	if !u.attached.sideInputs[side] {
		t.Fatal("Main rewind retired an independent Side input admission")
	}
	text, err := u.attached.hooks.compaction("main", "summary")(t.Context())
	if err != nil || text != "retained summary" || summaries != 1 {
		t.Fatal("retained summary reader lost its owner")
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

func TestAppServerRewindLegacyRefusesBeforeEdits(t *testing.T) {
	u, w := rewindEditsFixture(t)
	composerShellEscape(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","historyMode":"legacy"}}}`, read.ID))
	appServerTestKeys(t, u, "\r")
	if !strings.Contains(u.picker.Problem, "legacy") || len(u.picker.Choices) != 0 || w.Len() != 0 || u.Rewind != nil || u.Replacement.Pending() {
		t.Fatal("legacy history admitted rewind or edit reversal")
	}
}

func TestAppServerRewindTurnPages(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","historyMode":"paginated"}}}`, read.ID))
	page := resumeTestOne(t, w, "thread/turns/list")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"first","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"First"}]}]}],"nextCursor":"older"}}`, page.ID))
	page = resumeTestOne(t, w, "thread/turns/list")
	if page.Params.Cursor != "older" || !u.picker.Loading {
		t.Fatal("history paging stopped early")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"second","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"Second"}]}]}]}}`, page.ID))
	if u.picker.Loading || len(u.picker.Choices) != 2 || u.picker.Choices[1].Name != "second" {
		t.Fatal("picker lost a retained page")
	}
	appServerTestKeys(t, u, "\x1b[B\r\r")
	if rewind := resumeTestOne(t, w, "thread/revert"); rewind.Params.BeforeTurnID != "second" {
		t.Fatal("paged target changed")
	}
}

func TestAppServerRewindReloadPages(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	rewindTestTurns(t, u, w)
	appServerTestKeys(t, u, "\r\r")
	rewind := resumeTestOne(t, w, "thread/revert")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","cwd":"/work","historyMode":"paginated","turns":[]}}}`, rewind.ID))
	appServerTestKeys(t, u, "preserved draft")
	// A duplicate original response cannot finish replacement before its pages.
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","cwd":"/work","historyMode":"paginated","turns":[]}}}`, rewind.ID))

	for index := range 2 {
		page := resumeTestOne(t, w, "thread/turns/list")
		if !u.Replacement.Pending() {
			t.Fatal("input admitted before transcript reload")
		}
		cursor := `,"nextCursor":"next"`
		if index == 1 {
			cursor = ""
		}
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[{"id":"retained-%d","status":"completed","items":[{"type":"agentMessage","id":"answer-%d","text":"Retained answer %d"}]}]%s}}`, page.ID, index, index, index, cursor))
	}
	resumeTestSettle(t, u, w)
	rows, _ := u.testMainFrame(100, 20, 0)
	frame := strings.Join(rows, "\n")
	if !strings.Contains(frame, "Retained answer 0") || !strings.Contains(frame, "Retained answer 1") || strings.Contains(frame, "Implement the parser") || u.Replacement.Pending() || u.Text != "preserved draft" || u.Thread != "main" {
		t.Fatal("rewind did not replace the transcript with all retained pages")
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
	rewindTestHistory(t, u, w, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","turns":[{"id":"answer","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":%q}]}]}]}}}`, read.ID, text))
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Display != "Autosquash, including descendant commits" {
		t.Fatalf("answer choices = %+v", u.picker.Choices)
	}
	rows, _ := u.testMainFrame(100, 18, 0)
	frame := strings.Join(rows, "\n")
	if !strings.Contains(frame, "Autosquash, including descendant commits") || strings.Contains(frame, "send_user_message_question_reply") {
		t.Fatalf("answer dialog = %s", frame)
	}
	appServerTestKeys(t, u, "\r\r")
	if fork := resumeTestOne(t, w, "thread/revert"); fork.Params.BeforeTurnID != "answer" {
		t.Fatalf("answer fork = %+v", fork)
	}
}
