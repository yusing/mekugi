package headless

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

func headlessTestMessage(t *testing.T, h *client, method, params string) error {
	t.Helper()
	return h.message(appserver.Message{Method: method, Params: jsontext.Value(params)})
}

func TestHeadlessAppServerRejectsInvalidPromptBeforeLaunch(t *testing.T) {
	for _, tc := range []struct{ name, prompt, want string }{
		{"empty", "", "nonempty"}, {"whitespace", " \n\t", "nonempty"},
		{"oversized", strings.Repeat("x", (16<<20)+1), "16 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("headless-must-not-start")
			run, err := Start(t.Context(), cmd, strings.NewReader(tc.prompt), io.Discard, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || run != nil || cmd.Process != nil {
				t.Fatalf("run nonnil=%v, err=%v, process=%v", run != nil, err, cmd.Process)
			}
		})
	}
}

type headlessFailedIO struct{ err error }

func (f headlessFailedIO) Read([]byte) (int, error)  { return 0, f.err }
func (f headlessFailedIO) Write([]byte) (int, error) { return 0, f.err }

func TestHeadlessAppServerPromptReadFailure(t *testing.T) {
	want := errors.New("stdin failed")
	run, err := Start(t.Context(), exec.Command("headless-must-not-start"), headlessFailedIO{want}, io.Discard, nil)
	if run != nil || !errors.Is(err, want) {
		t.Fatalf("run nonnil=%v err=%v", run != nil, err)
	}
}

type testInput struct{ bytes.Buffer }

func (*testInput) Close() error { return nil }

func testClient() (*client, *testInput) {
	wire := new(testInput)
	host := &appserver.Client{Input: wire}
	return &client{host: host, requests: sessioncontrol.NewRequests[struct{}](host)}, wire
}

func TestHeadlessAppServerInitializeSequence(t *testing.T) {
	h, wire := testClient()
	h.prompt = "Keep this\nprompt."
	id, err := h.requests.Initialize(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{`"unrelated"`, `"` + id + `"`, ""} {
		if err := h.message(appserver.Message{ID: jsontext.Value(other), Result: jsontext.Value(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	requireHeadlessTestMethods(t, wire, "initialize")
	if err := h.message(appserver.Message{ID: jsontext.Value(id), Result: jsontext.Value(`{}`)}); err != nil {
		t.Fatal(err)
	}
	messages := requireHeadlessTestMethods(t, wire, "initialize", "initialized", "thread/start")
	threadRequest := messages[2]
	var threadParams struct {
		ApprovalPolicy string `json:"approvalPolicy"`
		Sandbox        string `json:"sandbox"`
	}
	if err := json.Unmarshal(threadRequest.Params, &threadParams); err != nil {
		t.Fatal(err)
	}
	if threadParams.ApprovalPolicy != "never" || threadParams.Sandbox != "danger-full-access" {
		t.Fatalf("thread params=%s", threadRequest.Params)
	}
	// Duplicate replies must not replay host effects or replace pending ownership.
	if err := h.message(appserver.Message{ID: jsontext.Value(id), Error: &appserver.Error{Message: "stale failure"}}); err != nil {
		t.Fatal(err)
	}
	result := jsontext.Value(`{"thread":{"id":"main","cwd":"/workspace","turns":"unconsumed","status":17}}`)
	if err := h.message(appserver.Message{ID: threadRequest.ID, Result: result}); err != nil {
		t.Fatal(err)
	}
	messages = requireHeadlessTestMethods(t, wire, "initialize", "initialized", "thread/start", "turn/start")
	if h.thread != "main" {
		t.Fatalf("thread=%q", h.thread)
	}
	var params struct {
		ThreadID string `json:"threadId"`
		Input    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	}
	if err := json.Unmarshal(messages[3].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "main" || len(params.Input) != 1 || params.Input[0].Text != h.prompt || params.Input[0].Type != "text" {
		t.Fatalf("turn params=%s", messages[3].Params)
	}
	if err := h.message(appserver.Message{ID: threadRequest.ID, Result: result}); err != nil {
		t.Fatal(err)
	}
	if err := h.message(appserver.Message{ID: messages[3].ID, Result: jsontext.Value(`{}`)}); err != nil {
		t.Fatal(err)
	}
	requireHeadlessTestMethods(t, wire, "initialize", "initialized", "thread/start", "turn/start")
	if h.requests.Pending() {
		t.Fatal("turn/start response left a pending request")
	}
}

func TestHeadlessAppServerRejectsMissingThreadIdentity(t *testing.T) {
	for _, result := range []string{`{}`, `{"thread":{"id":"main"}}`, `{"thread":{"cwd":"/tmp"}}`, `not json`} {
		h, _ := testClient()
		id, err := h.requests.Send("thread/start", nil, "", struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.message(appserver.Message{ID: jsontext.Value(id), Result: jsontext.Value(result)}); err == nil {
			t.Fatalf("accepted %s", result)
		}
	}
}

func TestHeadlessAppServerRejectsHostRequestsAndRPCFailures(t *testing.T) {
	h, _ := testClient()
	if err := h.message(appserver.Message{ID: jsontext.Value(`7`), Method: "item/commandExecution/requestApproval"}); err == nil {
		t.Fatal("accepted interactive host request")
	}
	id, err := h.requests.Send("turn/start", nil, "", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.message(appserver.Message{ID: jsontext.Value(id), Error: &appserver.Error{Code: -1, Message: "start rejected"}}); err == nil || !strings.Contains(err.Error(), "start rejected") {
		t.Fatalf("err=%v", err)
	}
}

func TestHeadlessAppServerIgnoresStaleCompletionAndRejectsFailedTurn(t *testing.T) {
	h := &client{ctx: t.Context(), notices: new(notice.Service), thread: "main", turn: "current", output: jsontext.NewEncoder(io.Discard)}
	for _, params := range []string{
		`{"threadId":"foreign","turn":{"id":"current","status":"completed"}}`,
		`{"threadId":"` + h.thread + `","turn":{"id":"stale","status":"completed"}}`,
	} {
		var event struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal([]byte(params), &event); err != nil {
			t.Fatal(err)
		}
		h.notices.Submit(notice.Input{Session: "session", Thread: event.ThreadID, Turn: event.Turn.ID, Text: "upstream restart", Request: true})
		if err := headlessTestMessage(t, h, "turn/completed", params); err != nil {
			t.Fatal(err)
		}
		if h.completed || h.turn != "current" {
			t.Fatal("unrelated completion changed current turn")
		}
		if len(h.notices.Pending()) != 0 {
			t.Fatal("headless recovery left a launcher failure notice")
		}
	}
	if err := headlessTestMessage(t, h, "turn/completed", `{"threadId":"`+h.thread+`","turn":{"id":"current","status":"failed"}}`); err == nil || h.completed {
		t.Fatalf("failed completion err=%v completed=%v", err, h.completed)
	}
}

func TestHeadlessAppServerIgnoresUnidentifiedCompletion(t *testing.T) {
	h := &client{thread: "main"}
	if err := headlessTestMessage(t, h, "turn/completed", `{"threadId":"main","turn":{"status":"completed"}}`); err != nil {
		t.Fatal(err)
	}
	if h.completed {
		t.Fatal("completion without a turn identity completed Main")
	}
}

func TestHeadlessAppServerEmitJSONLAndOutputFailure(t *testing.T) {
	var output bytes.Buffer
	h := &client{output: jsontext.NewEncoder(&output)}
	if err := h.emit("mekugi/headless/completed", map[string]string{"threadId": "main"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "\n") || strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("not one JSONL record: %q", output.String())
	}
	var m appserver.Message
	if err := json.Unmarshal(output.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Method != "mekugi/headless/completed" {
		t.Fatalf("method=%q", m.Method)
	}
	want := errors.New("output failed")
	h.output = jsontext.NewEncoder(headlessFailedIO{want})
	if err := h.emit("mekugi/headless/completed", map[string]string{"threadId": "main"}); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestHeadlessAppServerCancelledPromptRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	cancel()
	run, err := Start(ctx, exec.Command("headless-must-not-start"), reader, io.Discard, nil)
	if run != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("run nonnil=%v err=%v", run != nil, err)
	}
}

func requireHeadlessTestMethods(t *testing.T, wire *testInput, want ...string) []appserver.Message {
	t.Helper()
	var got []string
	var messages []appserver.Message
	for _, row := range strings.Split(strings.TrimSpace(wire.String()), "\n") {
		var message appserver.Message
		if err := json.Unmarshal([]byte(row), &message); err != nil {
			t.Fatal(err)
		}
		got = append(got, message.Method)
		messages = append(messages, message)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("RPCs = %v, want %v", got, want)
	}
	return messages
}
