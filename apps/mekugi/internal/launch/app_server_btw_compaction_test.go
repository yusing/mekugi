package launch

import (
	"bytes"
	jsonv1 "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/libraries/diffrender"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestSideForkDuringMainCompaction(t *testing.T) {
	for _, oneoff := range []bool{false, true} {
		for _, gate := range []string{"starting", "running", "acknowledged", "completed"} {
			t.Run(fmt.Sprintf("oneoff=%v/%s", oneoff, gate), func(t *testing.T) {
				u, wire := newAppServerTestUI()
				u.startSession("main", t.TempDir())
				appServerTestKeys(t, u, "/compact\r")
				compact := btwTestRequest(t, wire, "thread/compact/start", "main")
				if gate == "acknowledged" {
					btwTestReply(t, u, compact, `{}`)
				}
				if gate == "running" || gate == "completed" {
					appServerTestTurn(t, u, "compact-turn")
				}
				if gate == "completed" {
					appServerTestTurnEnd(t, u, "compact-turn", "completed")
				}
				if oneoff {
					oneoffTestStart(t, u, wire)
				} else {
					btwTestStart(t, u, wire)
				}
				thread, turn := "side", "side-turn"
				if oneoff {
					thread, turn = "oneoff", "oneoff-turn"
				}
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn}})
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "independent answer", "phase": "final_answer"}})
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}})
				b := u.BTW
				if oneoff {
					b = u.side
					if !strings.Contains(strings.Join(u.sideFrame(100, 24), "\n"), "independent answer") {
						t.Fatal("one-off answer did not reach its view")
					}
					selectSideThread(t, u, u.attached.presentation.View().Main)
				} else if len(b.Answer) != 1 || b.Answer[0].Text != "independent answer" {
					t.Fatal("BTW answer did not reach its exchange")
				}
				if b.Busy || !u.Compaction.Pending() {
					t.Fatal("side completion lost its answer or settled Main compaction")
				}
				appServerTestKeys(t, u, "Main waits\r")
				if wire.Len() != 0 {
					t.Fatalf("Main input bypassed compaction: %s", wire.String())
				}
				if gate == "starting" || gate == "acknowledged" {
					appServerTestTurn(t, u, "compact-turn")
				}
				if gate != "completed" {
					appServerTestTurnEnd(t, u, "compact-turn", "completed")
				}
				if gate != "acknowledged" {
					if wire.Len() != 0 {
						t.Fatal("Main input bypassed compact acknowledgement")
					}
					btwTestReply(t, u, compact, `{}`)
				}
				btwTestRequest(t, wire, "turn/start", "main")
			})
		}
	}
}

func TestOneoffReplacementDuringMainCompaction(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
	selectSideThread(t, u, u.attached.presentation.View().Main)
	appServerTestKeys(t, u, "/compact\r")
	btwTestRequest(t, wire, "thread/compact/start", "main")
	appServerTestKeys(t, u, "/oneoff replacement\r\x1b[B\r")
	line, err := wire.ReadBytes('\n')
	var archive btwTestRPC
	if err != nil || jsonv1.Unmarshal(line, &archive) != nil || archive.Method != "thread/archive" || archive.Params.ThreadID != "oneoff" {
		t.Fatalf("missing old One-off cleanup: %s", line)
	}
	fork := btwTestRequest(t, wire, "thread/fork", "main")
	if !u.Compaction.Starting() || u.side == nil || strings.TrimSpace(u.side.Pending.Text) != "replacement" {
		t.Fatal("replacement lost its task or settled Main compaction")
	}
	btwTestReply(t, u, archive, `{}`)
	btwTestReply(t, u, fork, `{"thread":{"id":"replacement"}}`)
	btwTestRequest(t, wire, "turn/start", "replacement")
}

func TestAppServerBTWCompactionRejectsBeforeProvider(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	workspace := t.TempDir()
	thread := "side"
	requests := sessioncontrol.NewRequests[appServerRequest](nil)
	proxy.bindForkRequests(requests)
	requests.TrackSide(thread, &sessioncontrol.Side{Thread: thread, SideAnswer: new(conversationmodel.SideAnswer)})
	requestFor := func(thread string) (codexresponses.Request, http.Header) {
		request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
			"model": "gpt-test", "stream": true,
			"tool_choice": "auto", "parallel_tool_calls": false,
			"input": []any{map[string]any{"role": "user", "content": "Summarize."}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		metadata, ok := codexresponses.DecodeTurnMetadata(serverCompactionMetadataHeaders(t))
		if !ok {
			t.Fatal("invalid fixture metadata")
		}
		metadata.ThreadID = thread
		metadata.Directories = map[string]jsonv1.RawMessage{workspace: nil}
		headers := make(http.Header)
		headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
		headers.Set(codexresponses.ThreadIDHeader, thread)
		return request, headers
	}
	request, headers := requestFor(thread)
	provider := &serverFakeProvider{}
	var output bytes.Buffer
	err := executeRequest(t.Context(), t.Context(), request, headers, "side", provider, &output, nil, proxy)
	compatibility, ok := errors.AsType[*inferencegateway.CompatibilityError](err)
	if !ok || compatibility.Code != "btw_compaction_required" || compatibility.Message != conversationmodel.SideCompactionRequired {
		t.Fatalf("rejection = %v", err)
	}
	if len(provider.forwarded) != 0 || output.Len() != 0 {
		t.Fatalf("side request escaped: upstream=%d output=%q", len(provider.forwarded), output.String())
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, request.Fields)))
	httpRequest.Header = headers
	recorder := httptest.NewRecorder()
	testResponsesHandler(t.Context(), time.Second, provider, nil, proxy)(recorder, httpRequest)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), conversationmodel.SideCompactionRequired) {
		t.Fatalf("HTTP rejection: %d %s", recorder.Code, recorder.Body.String())
	}
	// Main compaction still goes to the provider while a side thread is registered.
	mainRequest, mainHeaders := requestFor("main")
	provider.results = []serverForwardResult{{response: serverHTTPResponse(`{"id":"main-compact","status":"completed","output":[]}`)}}
	if err := executeRequest(t.Context(), t.Context(), mainRequest, mainHeaders, "main", provider, io.Discard, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("Main compaction requests=%d want=1", len(provider.forwarded))
	}
}

func TestAppServerBTWCompactionRestoresAttachmentsOnce(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "error-first", true: "completion-first"}[completionFirst], func(t *testing.T) {
			u, w := newAppServerTestUI()
			setTestAppServerIntegration(u, newManagedMekugiProxy(t))
			u.Turn, u.attached.presentation.Status.Text = "main-turn", "Working"
			appServerTestKeys(t, u, "/btw explain ")
			image := filepath.Join(t.TempDir(), "side.png")
			attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, image)
			appServerTestKeys(t, u, "\r")
			fork := btwTestRequest(t, w, "thread/fork", "main")
			btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
			if !u.Requests.IsSideThread("side") {
				t.Fatal("side not registered before turn/start")
			}
			start := btwTestRequest(t, w, "turn/start", "side")
			btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
			if len(u.BTW.Pending.Images) != 1 || u.BTW.Pending.Text == "" {
				t.Fatal("start acknowledgement discarded retry input")
			}
			appServerTestKeys(t, u, "newer draft")
			errorEvent := func() {
				appServerTestNotify(t, u, "error", map[string]any{"threadId": "side", "error": map[string]any{"message": "HTTP 400: " + conversationmodel.SideCompactionRequired}})
			}
			completion := func() {
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "failed", "error": map[string]any{"message": conversationmodel.SideCompactionRequired}}})
			}
			if completionFirst {
				completion()
				errorEvent()
			} else {
				errorEvent()
				completion()
			}
			before := u.Text
			errorEvent()
			completion()
			if u.Text != before || strings.Count(before, "/btw") != 1 || !strings.HasSuffix(before, "newer draft") || len(u.Images) != 1 || u.Images[0].Path != image {
				t.Fatalf("restoration lost/duplicated input: %q images=%+v", u.Text, u.Images)
			}
			if u.BTW.Busy || u.BTW.Pending.Text != "" || u.BTW.Status != conversationmodel.SideCompactionRequired || u.Turn != "main-turn" || u.attached.presentation.Status.Text != "Working" {
				t.Fatalf("failure state = %+v Main=%s/%s", u.BTW, u.Turn, u.attached.presentation.Status.Text)
			}
			if err := submitBTW(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, nil); err != nil {
				t.Fatal(err)
			}
			if w.Len() != 0 || u.Text != before {
				t.Fatal("rejected side accepted follow-up")
			}
		})
	}
}

func TestAppServerBTWCompactionCloseRecreateIgnoresLateFailure(t *testing.T) {
	u, w := newAppServerTestUI()
	setTestAppServerIntegration(u, newManagedMekugiProxy(t))
	btwTestStart(t, u, w)
	old := u.BTW
	if err := closeBTWQuestion(&u.attached, &u.Client); err != nil {
		t.Fatal(err)
	}
	interrupt := btwTestRequest(t, w, "turn/interrupt", "side")
	appServerTestKeys(t, u, "/btw fresh\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"new-side"}}`)
	start := btwTestRequest(t, w, "turn/start", "new-side")
	btwTestReply(t, u, start, `{"turn":{"id":"new-turn"}}`)
	appServerTestNotify(t, u, "error", map[string]any{"threadId": "side", "error": map[string]any{"message": conversationmodel.SideCompactionRequired}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "failed"}})
	btwTestReply(t, u, interrupt, `{}`)
	unload := btwTestRequest(t, w, "thread/archive", "side")
	btwTestReply(t, u, unload, `{}`)
	if u.BTW == old || u.BTW.Thread != "new-side" || !u.BTW.Busy || u.Text != "" || u.Requests.IsSideThread("side") || !u.Requests.IsSideThread("new-side") {
		t.Fatalf("late events crossed lifecycle: %+v draft=%q", u.BTW, u.Text)
	}
}

func TestAppServerBTWCompactionBeforeStartAckDoesNotResend(t *testing.T) {
	u, w := newAppServerTestUI()
	setTestAppServerIntegration(u, newManagedMekugiProxy(t))
	appServerTestKeys(t, u, "/btw initial\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	start := btwTestRequest(t, w, "turn/start", "side")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "failed", "error": map[string]any{"message": conversationmodel.SideCompactionRequired}}})
	before := u.Text
	btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
	if w.Len() != 0 || u.BTW.Turn != "" || u.BTW.Busy || u.Text != before || !strings.Contains(before, "initial") {
		t.Fatalf("late start acknowledgement revived rejection: panel=%+v draft=%q sent=%q", u.BTW, u.Text, w.String())
	}
}

func TestUISnapshotBTWCompactionRequired(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	setBTWTest(u, &sessioncontrol.Side{SideAnswer: &conversationmodel.SideAnswer{Question: "Why did this fail?", Status: conversationmodel.SideCompactionRequired, Alert: true}})
	for _, test := range []struct {
		name  string
		width int
	}{{"btw-compaction-required", 110}, {"btw-compaction-required-narrow", 60}} {
		t.Run(test.name, func(t *testing.T) {
			uisnapshot.Assert(t, "testdata/snapshots/"+test.name+".txt", strings.Join(u.sideFrame(test.width, 24), "\n")+"\n")
		})
	}
}
