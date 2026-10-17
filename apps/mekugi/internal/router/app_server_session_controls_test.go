package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerCompactIdleAndLifecycle(t *testing.T) {
	for _, beforeAck := range []bool{false, true} {
		t.Run(fmt.Sprint("turnBeforeAck=", beforeAck), func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestKeys(t, u, "/compact\r")
			r := appServerOneRequest(t, w, "thread/compact/start", "")
			if !u.Compaction.AcknowledgementPending() || !u.Starting() || u.conversation().Submitted[u.Submission().ID].Text != "" || len(u.source.Entries) != 0 {
				t.Fatalf("compact treated as conversation input: pending=%v starting=%v entries=%+v", u.Compaction.AcknowledgementPending(), u.Starting(), u.source.Entries)
			}
			if beforeAck {
				appServerTestTurn(t, u, "compact-turn")
				appServerTestTurnEnd(t, u, "compact-turn", "completed")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
			if !beforeAck {
				appServerTestTurn(t, u, "compact-turn")
				appServerTestTurnEnd(t, u, "compact-turn", "completed")
			}
			if u.Compaction.AcknowledgementPending() || u.Starting() || u.Text != "" || len(appServerTurnRequests(t, w)) != 0 {
				t.Fatalf("compact lifecycle not settled: pending=%v starting=%v draft=%q", u.Compaction.AcknowledgementPending(), u.Starting(), u.Text)
			}
		})
	}
}

func TestAppServerCompactBusyQueuesAheadOfLaterText(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\t")
	appServerTestKeys(t, u, "after compact\r")
	if got := appServerTurnRequests(t, w); len(got) != 0 || len(u.conversation().Unsent) != 2 || u.Turn != "work" {
		t.Fatalf("busy compact replaced turn or sent text: requests=%+v unsent=%+v", got, u.conversation().Unsent)
	}
	appServerTestTurnEnd(t, u, "work", "completed")
	r := appServerOneRequest(t, w, "thread/compact/start", "")
	if len(u.conversation().Unsent) != 1 || u.conversation().Unsent[0].Text != "after compact" {
		t.Fatalf("input behind compact lost: %+v", u.conversation().Unsent)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	appServerTestTurn(t, u, "compact-turn")
	if got := appServerTurnRequests(t, w); len(got) != 0 {
		t.Fatalf("text overtook compaction: %+v", got)
	}
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerOneRequest(t, w, "turn/start", "after compact")
}

func TestAppServerCompactFailureRestoresCommand(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/compact\r")
	r := appServerOneRequest(t, w, "thread/compact/start", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
	if u.Text != "/compact" || u.Compaction.AcknowledgementPending() || u.Starting() || !strings.Contains(u.feedback.Text, "rejected") || len(u.source.Entries) != 0 {
		t.Fatalf("failed compact not restored: draft=%q notice=%q entries=%+v", u.Text, u.feedback.Text, u.source.Entries)
	}
}

func TestAppServerClearBusyRejected(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "busy")
	appServerTestKeys(t, u, "/clear\r")
	if w.Len() != 0 || u.Replacement.Pending() || u.Turn != "busy" || !strings.Contains(u.feedback.Text, "disabled") {
		t.Fatalf("busy clear was accepted: request=%q notice=%q", w.String(), u.feedback.Text)
	}
}

func TestAppServerClearWaitsForCompactAcknowledgement(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/compact\r")
	r := appServerOneRequest(t, w, "thread/compact/start", "")
	appServerTestTurn(t, u, "compact-turn")
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerTestKeys(t, u, "/clear\r")
	if w.Len() != 0 || u.Replacement.Pending() || !u.Compaction.AcknowledgementPending() || strings.TrimSpace(u.Text) != "/clear" {
		t.Fatalf("clear overtook compact acknowledgement: wire=%q clearing=%v compact=%v draft=%q", w.String(), u.Replacement.Pending(), u.Compaction.AcknowledgementPending(), u.Text)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	appServerTestKeys(t, u, "\r")
	clear := appServerOneRequest(t, w, "thread/start", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/tmp"}}}`, clear.ID))
	appServerTurnRequests(t, w)
	appServerTestKeys(t, u, "fresh prompt\r")
	appServerOneRequest(t, w, "turn/start", "fresh prompt")
}

func TestAppServerClearStartsFreshThreadAndRetiresOldEvents(t *testing.T) {
	u, w := newAppServerTestUI()
	u.model, u.reasoningEffort, u.serviceTier = "chosen", "high", "priority"
	appServerTestUserMessage(t, u, "old-user", "", "old transcript")
	appServerTestKeys(t, u, "/clear\r")
	var req struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			SessionStartSource string `json:"sessionStartSource"`
			Model              string `json:"model"`
			ServiceTier        string `json:"serviceTier"`
			Config             struct {
				Effort string `json:"model_reasoning_effort"`
			} `json:"config"`
		} `json:"params"`
	}
	if err := json.Unmarshal(w.Bytes(), &req); err != nil {
		t.Fatal(err)
	}
	w.Reset()
	if req.Method != "thread/start" || req.Params.SessionStartSource != "clear" || req.Params.Model != "chosen" || req.Params.ServiceTier != "priority" || req.Params.Config.Effort != "high" || !u.Replacement.Pending() || len(u.source.Entries) == 0 {
		t.Fatalf("clear request/state: %+v clearing=%v entries=%+v", req, u.Replacement.Pending(), u.source.Entries)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/tmp"},"model":"chosen","reasoningEffort":"high","serviceTier":"priority"}}`, req.ID))
	requests := appServerTurnRequests(t, w)
	if len(requests) != 2 || requests[0].Method != "thread/unsubscribe" || requests[1].Method != "model/list" {
		t.Fatalf("clear follow-up requests: %+v", requests)
	}
	if u.Thread != "fresh" || u.Replacement.Pending() || len(u.source.Entries) != 0 {
		t.Fatalf("old presentation retained: thread=%q entries=%+v", u.Thread, u.source.Entries)
	}
	appServerTestUserMessage(t, u, "late-old", "", "late old transcript")
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"late-turn"}}}`)
	if len(u.source.Entries) != 0 || u.Turn != "" {
		t.Fatalf("retired events repopulated session: %+v turn=%q", u.source.Entries, u.Turn)
	}
	var frame bytes.Buffer
	if err := u.testPaint(&frame, 80, 20); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ansi.Strip(frame.String()), "old transcript") {
		t.Fatal("retired transcript remains rendered")
	}
	appServerTestKeys(t, u, "fresh prompt\r")
	appServerOneRequest(t, w, "turn/start", "fresh prompt")
}

func TestAppServerClearFailureKeepsTranscript(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestUserMessage(t, u, "old-user", "", "keep this")
	appServerTestKeys(t, u, "/clear\r")
	r := appServerOneRequest(t, w, "thread/start", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, r.ID))
	if u.Thread != "main" || u.Replacement.Pending() || len(u.source.Entries) != 1 || u.source.Entries[0].Source.Text != "keep this" || !strings.Contains(u.feedback.Text, "unavailable") {
		t.Fatalf("failed clear discarded state: thread=%q entries=%+v notice=%q", u.Thread, u.source.Entries, u.feedback.Text)
	}
}

func TestAppServerClearFailureRestoresNewInputInsteadOfSendingToOldThread(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/clear\r")
	r := appServerOneRequest(t, w, "thread/start", "")
	appServerTestKeys(t, u, "new session only\r")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, r.ID))
	if u.Text != "new session only" || len(appServerTurnRequests(t, w)) != 0 {
		t.Fatalf("new input escaped to old thread: %q", u.Text)
	}
}

func TestAppServerClearKeepsThreadCommandsUntilNewSessionReady(t *testing.T) {
	for _, command := range []string{"/model other", "/model", "/tier priority", "/effort high", "!echo must-not-run", "/btw side question"} {
		t.Run(command, func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestKeys(t, u, "/clear\r")
			r := appServerOneRequest(t, w, "thread/start", "")
			appServerTestKeys(t, u, command+"\r")
			if strings.TrimSpace(u.Text) != command || u.Settings.Pending() || u.conversation().Shell.Text != "" || u.side != nil || len(appServerTurnRequests(t, w)) != 0 {
				t.Fatalf("command escaped while clearing: draft=%q settings=%v shell=%q btw=%+v", u.Text, u.Settings.Pending(), u.conversation().Shell.Text, u.side)
			}
			if accepted, err := u.updateSettings(map[string]any{"effort": "high"}); err != nil || accepted || w.Len() != 0 {
				t.Fatalf("shortcut bypassed clear gate: accepted=%v err=%v wire=%s", accepted, err, w.String())
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/tmp"}}}`, r.ID))
			appServerTurnRequests(t, w)
			appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"stale"}}}`)
			appServerTestTurn(t, u, "old-shell-turn")
			if strings.TrimSpace(u.Text) != command || u.Turn != "" || u.Settings.Pending() || u.conversation().Shell.Text != "" {
				t.Fatalf("old lifecycle affected fresh session: draft=%q turn=%q", u.Text, u.Turn)
			}
			appServerTestKeys(t, u, "\x03fresh prompt\r")
			appServerOneRequest(t, w, "turn/start", "fresh prompt")
		})
	}
}

func TestAppServerClearTransfersWaitingConversationScope(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/clear\r")
	request := appServerOneRequest(t, wire, "thread/start", "")
	appServerTestKeys(t, u, "first\rnext turn\tdraft")
	previous := u.conversation()
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/tmp"}}}`, request.ID))
	requests := appServerTurnRequests(t, wire)
	found := false
	for _, request := range requests {
		if request.Method == "turn/start" {
			found = request.Params.ThreadID == "fresh" && request.text() == "first\nnext turn"
		}
	}
	current := u.conversation()
	if !found || current == previous || u.Text != "draft" || len(current.Unsent)+len(current.Queued) != 0 || current.Submitted[u.Submission().ID].Text != "first\nnext turn" {
		t.Fatalf("session replacement lost waiting input: requests=%+v draft=%q queued=%+v", requests, u.Text, current.Queued)
	}
}
