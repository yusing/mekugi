package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

func headlessTestMessage(t *testing.T, h *headlessAppServer, method, params string) error {
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
			run, err := startHeadlessAppServer(t.Context(), cmd, strings.NewReader(tc.prompt), io.Discard, nil)
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
	run, err := startHeadlessAppServer(t.Context(), exec.Command("headless-must-not-start"), headlessFailedIO{want}, io.Discard, nil)
	if run != nil || !errors.Is(err, want) {
		t.Fatalf("run nonnil=%v err=%v", run != nil, err)
	}
}

func TestHeadlessAppServerInitializeSequence(t *testing.T) {
	wire := &appServerTestInput{}
	h := &headlessAppServer{ctx: t.Context(), client: &appserver.Client{Input: wire}, prompt: "Keep this\nprompt.", request: "initialize", requestID: `"initialize"`}
	if err := h.message(appserver.Message{ID: jsontext.Value(`"unrelated"`), Result: jsontext.Value(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || h.request != "initialize" {
		t.Fatal("foreign RPC advanced initialization")
	}
	if err := h.message(appserver.Message{ID: jsontext.Value(h.requestID), Result: jsontext.Value(`{}`)}); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "initialized", "thread/start")
	result, err := json.Marshal(map[string]any{"thread": map[string]string{"id": "main", "cwd": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.message(appserver.Message{ID: jsontext.Value(h.requestID), Result: result}); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "initialized", "thread/start", "turn/start")
	if h.thread != "main" || h.reset == nil || h.reset.delay != 0 {
		t.Fatalf("thread=%q reset=%+v", h.thread, h.reset)
	}
	lines := strings.Split(strings.TrimSpace(wire.String()), "\n")
	var sent appserver.Message
	if err := json.Unmarshal([]byte(lines[2]), &sent); err != nil {
		t.Fatal(err)
	}
	var params struct {
		ThreadID string `json:"threadId"`
		Input    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	}
	if err := json.Unmarshal(sent.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "main" || len(params.Input) != 1 || params.Input[0].Text != h.prompt || params.Input[0].Type != "text" {
		t.Fatalf("turn params=%s", sent.Params)
	}
}

func TestHeadlessAppServerRejectsMissingThreadIdentity(t *testing.T) {
	for _, result := range []string{`{}`, `{"thread":{"id":"main"}}`, `{"thread":{"cwd":"/tmp"}}`, `not json`} {
		h := &headlessAppServer{request: "thread/start", requestID: `1`}
		if err := h.message(appserver.Message{ID: jsontext.Value(`1`), Result: jsontext.Value(result)}); err == nil {
			t.Fatalf("accepted %s", result)
		}
	}
}

func TestHeadlessAppServerRejectsHostRequestsAndRPCFailures(t *testing.T) {
	h := &headlessAppServer{}
	if err := h.message(appserver.Message{ID: jsontext.Value(`7`), Method: "item/commandExecution/requestApproval"}); err == nil {
		t.Fatal("accepted interactive host request")
	}
	h.request, h.requestID = "turn/start", `8`
	if err := h.message(appserver.Message{ID: jsontext.Value(`8`), Error: &appserver.Error{Code: -1, Message: "start rejected"}}); err == nil || !strings.Contains(err.Error(), "start rejected") {
		t.Fatalf("err=%v", err)
	}
}

func TestHeadlessAppServerIgnoresStaleCompletionAndRejectsFailedTurn(t *testing.T) {
	d, _ := resetDriverFixture(t, "off")
	// Use its isolated durable owner, but no active slice reset.
	d.phase, d.requestID, d.intent = "", "", nil
	h := &headlessAppServer{ctx: t.Context(), proxy: d.proxy, client: d.client, reset: d, thread: d.thread, turn: "current", output: jsontext.NewEncoder(io.Discard)}
	for _, params := range []string{
		`{"threadId":"foreign","turn":{"id":"current","status":"completed"}}`,
		`{"threadId":"` + d.thread + `","turn":{"id":"stale","status":"completed"}}`,
	} {
		if err := headlessTestMessage(t, h, "turn/completed", params); err != nil {
			t.Fatal(err)
		}
		if h.completed || h.turn != "current" {
			t.Fatal("unrelated completion changed current turn")
		}
	}
	if err := headlessTestMessage(t, h, "turn/completed", `{"threadId":"`+d.thread+`","turn":{"id":"current","status":"failed"}}`); err == nil || h.completed {
		t.Fatalf("failed completion err=%v completed=%v", err, h.completed)
	}
}

func TestHeadlessAppServerContinuationStaysPendingUntilMatchingStart(t *testing.T) {
	d, wire := resetDriverFixture(t, "off")
	d.delay = 0
	h := &headlessAppServer{ctx: t.Context(), client: d.client, proxy: d.proxy, reset: d, thread: d.thread, turn: "first-turn", completed: true, output: jsontext.NewEncoder(io.Discard)}

	if err := d.tick(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "turn/start")
	if err := h.message(appserver.Message{ID: jsontext.Value(d.requestID), Result: jsontext.Value(`{"turn":{"id":"second-turn"}}`)}); err != nil {
		t.Fatal(err)
	}
	if !d.active() || !d.startPending {
		t.Fatal("headless became terminal before continuation start")
	}
	if err := headlessTestMessage(t, h, "turn/completed", `{"threadId":"`+d.thread+`","turn":{"id":"stale","status":"completed"}}`); err != nil {
		t.Fatal(err)
	}
	if !d.active() {
		t.Fatal("stale completion released pending continuation")
	}
	if err := headlessTestMessage(t, h, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"second-turn"}}`); err != nil {
		t.Fatal(err)
	}
	if h.completed || h.turn != "second-turn" || d.startPending {
		t.Fatalf("completed=%v turn=%q pending=%v", h.completed, h.turn, d.startPending)
	}
}

func TestHeadlessAppServerEmitJSONLAndOutputFailure(t *testing.T) {
	var output bytes.Buffer
	h := &headlessAppServer{output: jsontext.NewEncoder(&output)}
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
	run, err := startHeadlessAppServer(ctx, exec.Command("headless-must-not-start"), reader, io.Discard, nil)
	if run != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("run nonnil=%v err=%v", run != nil, err)
	}
}
