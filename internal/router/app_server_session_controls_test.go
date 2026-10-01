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
			if !u.compaction.ackPending || !u.starting() || u.submission.text != "" || len(u.view.entries) != 0 {
				t.Fatalf("compact treated as conversation input: pending=%v starting=%v entries=%+v", u.compaction.ackPending, u.starting(), u.view.entries)
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
			if u.compaction.ackPending || u.starting() || u.draft != "" || len(appServerTurnRequests(t, w)) != 0 {
				t.Fatalf("compact lifecycle not settled: pending=%v starting=%v draft=%q", u.compaction.ackPending, u.starting(), u.draft)
			}
		})
	}
}

func TestAppServerCompactBusyQueuesAheadOfLaterText(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\r")
	appServerTestKeys(t, u, "after compact\r")
	if got := appServerTurnRequests(t, w); len(got) != 0 || len(u.unsent) != 2 || u.turn != "work" {
		t.Fatalf("busy compact replaced turn or sent text: requests=%+v unsent=%+v", got, u.unsent)
	}
	appServerTestTurnEnd(t, u, "work", "completed")
	r := appServerOneRequest(t, w, "thread/compact/start", "")
	if len(u.unsent) != 1 || u.unsent[0].text != "after compact" {
		t.Fatalf("input behind compact lost: %+v", u.unsent)
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
	if u.draft != "/compact" || u.compaction.ackPending || u.starting() || !strings.Contains(u.notice, "rejected") || len(u.view.entries) != 0 {
		t.Fatalf("failed compact not restored: draft=%q notice=%q entries=%+v", u.draft, u.notice, u.view.entries)
	}
}

func TestAppServerClearBusyRejected(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "busy")
	appServerTestKeys(t, u, "/clear\r")
	if w.Len() != 0 || u.replacement.pending() || u.turn != "busy" || !strings.Contains(u.notice, "disabled") {
		t.Fatalf("busy clear was accepted: request=%q notice=%q", w.String(), u.notice)
	}
}

func TestAppServerClearWaitsForCompactAcknowledgement(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/compact\r")
	r := appServerOneRequest(t, w, "thread/compact/start", "")
	appServerTestTurn(t, u, "compact-turn")
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerTestKeys(t, u, "/clear\r")
	if w.Len() != 0 || u.replacement.pending() || !u.compaction.ackPending || strings.TrimSpace(u.draft) != "/clear" {
		t.Fatalf("clear overtook compact acknowledgement: wire=%q clearing=%v compact=%v draft=%q", w.String(), u.replacement.pending(), u.compaction.ackPending, u.draft)
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
	if req.Method != "thread/start" || req.Params.SessionStartSource != "clear" || req.Params.Model != "chosen" || req.Params.ServiceTier != "priority" || req.Params.Config.Effort != "high" || !u.replacement.pending() || len(u.view.entries) == 0 {
		t.Fatalf("clear request/state: %+v clearing=%v entries=%+v", req, u.replacement.pending(), u.view.entries)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/tmp"},"model":"chosen","reasoningEffort":"high","serviceTier":"priority"}}`, req.ID))
	requests := appServerTurnRequests(t, w)
	if len(requests) != 2 || requests[0].Method != "thread/unsubscribe" || requests[1].Method != "model/list" {
		t.Fatalf("clear follow-up requests: %+v", requests)
	}
	if u.thread != "fresh" || u.replacement.pending() || len(u.view.entries) != 0 {
		t.Fatalf("old presentation retained: thread=%q entries=%+v", u.thread, u.view.entries)
	}
	appServerTestUserMessage(t, u, "late-old", "", "late old transcript")
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"late-turn"}}}`)
	if len(u.view.entries) != 0 || u.turn != "" {
		t.Fatalf("retired events repopulated session: %+v turn=%q", u.view.entries, u.turn)
	}
	var frame bytes.Buffer
	if err := u.paint(&frame, 80, 20); err != nil {
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
	if u.thread != "main" || u.replacement.pending() || len(u.view.entries) != 1 || u.view.entries[0].Text != "keep this" || !strings.Contains(u.notice, "unavailable") {
		t.Fatalf("failed clear discarded state: thread=%q entries=%+v notice=%q", u.thread, u.view.entries, u.notice)
	}
}

func TestAppServerClearFailureRestoresNewInputInsteadOfSendingToOldThread(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/clear\r")
	r := appServerOneRequest(t, w, "thread/start", "")
	appServerTestKeys(t, u, "new session only\r")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, r.ID))
	if u.draft != "new session only" || len(appServerTurnRequests(t, w)) != 0 {
		t.Fatalf("new input escaped to old thread: %q", u.draft)
	}
}

func TestAppServerClearKeepsThreadCommandsUntilNewSessionReady(t *testing.T) {
	for _, command := range []string{"/model other", "/model", "/tier priority", "/effort high", "!echo must-not-run", "/btw side question"} {
		t.Run(command, func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestKeys(t, u, "/clear\r")
			r := appServerOneRequest(t, w, "thread/start", "")
			appServerTestKeys(t, u, command+"\r")
			if strings.TrimSpace(u.draft) != command || u.settings.pending() || u.shellCommand.pending.text != "" || u.btw != nil || len(appServerTurnRequests(t, w)) != 0 {
				t.Fatalf("command escaped while clearing: draft=%q settings=%v shell=%q btw=%+v", u.draft, u.settings.pending(), u.shellCommand.pending.text, u.btw)
			}
			if accepted, err := u.updateSettings(map[string]any{"effort": "high"}); err != nil || accepted || w.Len() != 0 {
				t.Fatalf("shortcut bypassed clear gate: accepted=%v err=%v wire=%s", accepted, err, w.String())
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/tmp"}}}`, r.ID))
			appServerTurnRequests(t, w)
			appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"stale"}}}`)
			appServerTestTurn(t, u, "old-shell-turn")
			if strings.TrimSpace(u.draft) != command || u.turn != "" || u.settings.pending() || u.shellCommand.pending.text != "" {
				t.Fatalf("old lifecycle affected fresh session: draft=%q turn=%q", u.draft, u.turn)
			}
			appServerTestKeys(t, u, "\x03fresh prompt\r")
			appServerOneRequest(t, w, "turn/start", "fresh prompt")
		})
	}
}
