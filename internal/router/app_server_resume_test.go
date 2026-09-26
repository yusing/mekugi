package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestAppServerResumeStartup(t *testing.T) {
	u, w := newAppServerTestUI()
	u.thread, u.resumeThread = "", "saved"
	u.resumeConfig = map[string]any{"model": "override", "model_provider": "routed"}
	u.agents = newLiveActivityView()
	u.requests["0"] = "initialize"
	appServerTestMessage(t, u, `{"id":0,"result":{}}`)
	lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("startup requests: %s", w.Bytes())
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			ThreadID      string         `json:"threadId"`
			Approval      string         `json:"approvalPolicy"`
			Sandbox       string         `json:"sandbox"`
			Config        map[string]any `json:"config"`
			ModelProvider string         `json:"modelProvider"`
		} `json:"params"`
	}
	if err := json.Unmarshal(lines[1], &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "thread/resume" || request.Params.ThreadID != "saved" || request.Params.Approval != "never" || request.Params.Sandbox != "danger-full-access" || request.Params.Config["model"] != "override" || request.Params.ModelProvider != "routed" {
		t.Fatalf("wrong resume request: %+v", request)
	}
	w.Reset()
	appServerTestKeys(t, u, "next prompt\r")
	if w.Len() != 0 || u.draft != "next prompt" {
		t.Fatal("input submitted before resume succeeded")
	}
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"saved","turnId":"old","item":{"type":"agentMessage","id":"answer","text":"Saved answer"}}}`)
	appServerTestMessage(t, u, `{"id":1,"result":{"model":"model","reasoningEffort":"high","thread":{"id":"saved","cwd":"/workspace","turns":[{"id":"old","status":"completed","items":[{"type":"userMessage","id":"question","content":[{"type":"text","text":"Saved question"}]},{"type":"agentMessage","id":"answer","text":"Saved answer"}]}]}}}`)
	appServerTestMessage(t, u, `{"id":2,"result":{"data":[],"nextCursor":null}}`)
	appServerTestMessage(t, u, `{"id":3,"result":{"data":[],"nextCursor":null}}`)
	if u.thread != "saved" || u.status != "Ready" || u.model != "model" || u.reasoningEffort != "high" || len(u.view.entries) != 2 || u.view.entries[0].Text != "Saved question" || u.view.entries[1].Text != "Saved answer" {
		t.Fatalf("resume state: %+v, entries=%+v", u, u.view.entries)
	}
	if bytes.Contains(w.Bytes(), []byte("turn/start")) || bytes.Contains(w.Bytes(), []byte("thread/resume")) {
		t.Fatal("history replay issued execution requests")
	}
	w.Reset()
	appServerTestKeys(t, u, "\r")
	if err := json.Unmarshal(bytes.TrimSpace(w.Bytes()), &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "turn/start" || request.Params.ThreadID != "saved" {
		t.Fatalf("did not continue resumed thread: %+v", request)
	}
}

func TestAppServerResumeRejectsFailureAndWrongIdentity(t *testing.T) {
	for _, wire := range []string{`{"id":1,"error":{"code":-1,"message":"missing thread"}}`, `{"id":1,"result":{"thread":{"id":"other"}}}`, `{"id":1,"result":{"thread":{}}}`} {
		u, w := newAppServerTestUI()
		u.thread, u.resumeThread = "", "saved"
		u.requests["1"] = "thread/resume"
		var m appServerMessage
		if err := json.Unmarshal([]byte(wire), &m); err != nil {
			t.Fatal(err)
		}
		if err := u.message(m); err == nil || u.thread != "" || w.Len() != 0 {
			t.Fatalf("resume must fail without creating another thread: %v", err)
		}
	}
}

func TestAppServerResumeHistoryDoesNotReviveEffects(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{
		{ID: "cmd", Type: "commandExecution", Command: "false", ExitCode: new(2)},
		{ID: "edit", Type: "fileChange", Changes: []appServerFileChange{{Path: "a.go", Diff: "+changed"}}},
		{ID: "spawn", Type: "collabAgentToolCall", Tool: "spawnAgent", ReceiverThreadIDs: []string{"child"}},
	}}})
	if u.turn != "" || len(u.session.patches) != 0 || len(u.session.agents) != 1 || u.session.agents[0].Responding || len(u.view.entries) != 2 || u.view.blocks[0][0].exitCode != 2 {
		t.Fatalf("history revived lifecycle or lost tools: %+v", u.session)
	}
	u.restoreHistory([]appServerHistoryTurn{{ID: "active", Status: "inProgress", Items: []appServerItem{{ID: "partial", Type: "agentMessage", Text: "Partial"}}}})
	if u.turn != "active" || !u.session.agents[0].Responding {
		t.Fatal("active snapshot did not restore steer/interrupt target")
	}
	appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"main","turnId":"active","itemId":"partial","delta":" answer"}}`)
	if u.view.entries[len(u.view.entries)-1].Text != "Partial answer" {
		t.Fatal("snapshot swallowed subsequent delta")
	}
}

func TestAppServerResumePendingBound(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.thread, u.resumeThread = "", "saved"
	m := appServerMessage{Method: "thread/started", Params: jsontext.Value(`{}`)}
	for range 256 {
		if err := u.message(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.message(m); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("missing bounded recovery failure: %v", err)
	}
}

func TestAppServerResumeConfig(t *testing.T) {
	config := appServerResumeConfig([]string{"codex", "app-server", "-c", `model="old"`, "-c", `model = 'new'`, "-c", "model_provider=preview", "-c", `model_reasoning_effort="high"`, "-c", "other=true"})
	if len(config) != 3 || config["model"] != "new" || config["model_provider"] != "preview" || config["model_reasoning_effort"] != "high" {
		t.Fatalf("explicit resume settings: %+v", config)
	}
}
