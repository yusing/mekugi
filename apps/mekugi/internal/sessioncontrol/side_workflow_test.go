package sessioncontrol

import (
	"context"
	"errors"
	"fmt"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
)

func TestAnswerOnlyClassificationUsesLiveForkLedger(t *testing.T) {
	for _, oneoff := range []bool{false, true} {
		for _, btw := range []bool{false, true} {
			t.Run(fmt.Sprintf("oneoff=%t/btw=%t", oneoff, btw), func(t *testing.T) {
				transport := &requestTransport{id: "fork"}
				r := NewRequests[struct{}](transport)
				side := &Side{SideAnswer: &conversationmodel.SideAnswer{OneOff: oneoff}, ForkPending: true, LowReasoning: btw}
				if _, err := r.StartSide(side, SideMain{Thread: "main"}, time.Time{}); err != nil {
					t.Fatal(err)
				}
				request, _ := r.Take("fork")
				if _, err := r.ObserveSide(appserver.Message{Result: []byte(`{"thread":{"id":"side"}}`)}, request, "main"); err != nil {
					t.Fatal(err)
				}
				r.AdmitSideChild("child", "side")
				if r.IsSideThread("side") != !oneoff || r.IsBTWThread("side") != btw || r.IsSideThread("child") || r.IsBTWThread("child") {
					t.Fatal("classification confused root kind or descendant admission")
				}
				var workers sync.WaitGroup
				workers.Go(func() {
					for range 1000 {
						r.IsSideThread("side")
						r.IsBTWThread("side")
					}
				})
				for range 100 {
					side.Begin("follow-up", conversationmodel.Input{Text: "follow-up"})
				}
				if _, err := r.ObserveSide(appserver.Message{Method: "thread/archived", Params: []byte(`{"threadId":"side"}`)}, Request[struct{}]{}, "main"); err != nil {
					t.Fatal(err)
				}
				workers.Wait()
				if r.IsSideThread("side") || r.IsBTWThread("side") || r.Side("side") != side {
					t.Fatal("archived open dock retained classification or lost correlation")
				}
			})
		}
	}
}

func TestSideNativeRetryIsProgress(t *testing.T) {
	for _, oneoff := range []bool{false, true} {
		for _, recovery := range []string{"item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/started"} {
			r := NewRequests[struct{}](&requestTransport{})
			side := &Side{SideAnswer: &conversationmodel.SideAnswer{OneOff: oneoff}, Thread: "side", Turn: "turn", Busy: true}
			r.TrackSide(side.Thread, side)
			notify := func(method, params string) {
				t.Helper()
				if _, err := r.ObserveSide(appserver.Message{Method: method, Params: []byte(params)}, Request[struct{}]{}, "main"); err != nil {
					t.Fatal(err)
				}
			}
			notify("error", `{"threadId":"side","turnId":"turn","willRetry":true,"error":{"message":"Reconnecting... 2/5","additionalDetails":"Model at capacity"}}`)
			if side.Alert || !side.Busy || !strings.Contains(side.Status, "2/5") || !strings.Contains(side.Status, "Model at capacity") {
				t.Fatalf("retry became failure: %+v", side)
			}
			notify("error", `{"threadId":"side","turnId":"old","willRetry":true,"error":{"message":"stale"}}`)
			if !strings.Contains(side.Status, "2/5") {
				t.Fatal("stale retry replaced current progress")
			}
			notify(recovery, `{"threadId":"side","turnId":"turn","itemId":"answer","delta":"Recovered","item":{"id":"tool","type":"commandExecution"}}`)
			if side.Alert || strings.Contains(side.Status, "Reconnecting") {
				t.Fatalf("retry did not clear: %+v", side)
			}
		}
	}
}

func TestClosedSideHostCloseBeforeStartAcknowledgement(t *testing.T) {
	transport := &requestTransport{id: "archive"}
	r := NewRequests[struct{}](transport)
	side := &Side{SideAnswer: new(conversationmodel.SideAnswer), Thread: "side", Starting: true, Busy: true}
	r.TrackSide(side.Thread, side)
	side.Close()
	if _, err := r.ObserveSide(appserver.Message{Method: "thread/closed", Params: []byte(`{"threadId":"side"}`)}, Request[struct{}]{}, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ObserveSide(appserver.Message{Result: []byte(`{"turn":{"id":"late"}}`)}, Request[struct{}]{Side: side, Method: "turn/start"}, "main"); err != nil {
		t.Fatal(err)
	}
	if side.Starting || side.Turn != "" || len(transport.sends) != 1 || transport.sends[0].method != "thread/archive" {
		t.Fatal("late start revived execution or blocked cleanup")
	}
}

func TestSideArchiveFailureCanRetryAfterHostClose(t *testing.T) {
	for _, failure := range []string{"send", "acknowledgement"} {
		t.Run(failure, func(t *testing.T) {
			transport := &requestTransport{id: "archive"}
			r := NewRequests[struct{}](transport)
			side := &Side{SideAnswer: new(conversationmodel.SideAnswer), Thread: "side"}
			r.TrackSide(side.Thread, side)
			if failure == "send" {
				transport.err = errors.New("write failed")
			}
			if err := r.ReleaseSide(side); (err != nil) != (failure == "send") {
				t.Fatal(err)
			}
			if _, err := r.ObserveSide(appserver.Message{Method: "thread/closed", Params: []byte(`{"threadId":"side"}`)}, Request[struct{}]{}, "main"); err != nil {
				t.Fatal(err)
			}
			if failure == "acknowledgement" {
				if err := r.ReleaseSide(side); err != nil || len(transport.sends) != 1 {
					t.Fatal("host close duplicated an in-flight archive")
				}
				request, _ := r.Take("archive")
				update, err := r.ObserveSide(appserver.Message{Error: &appserver.Error{Message: "archive rejected"}}, request, "main")
				if err != nil || !update.NoticeError {
					t.Fatal("archive rejection was hidden")
				}
			}
			if !side.Unloaded() || side.Archived() || r.Side("side") != side {
				t.Fatal("host close or retry correlation lost")
			}
			transport.err = nil
			side.Close()
			if err := r.ReleaseSide(side); err != nil || transport.sends[len(transport.sends)-1].method != "thread/archive" {
				t.Fatal("failed archive cannot retry")
			}
			request, _ := r.Take("archive")
			if _, err := r.ObserveSide(appserver.Message{}, request, "main"); err != nil || !side.Archived() || r.HasSides() {
				t.Fatal("successful retry retained the dismissed fork")
			}
		})
	}
}

func TestSideForkInheritedEffort(t *testing.T) {
	driver := new(threadbinding.Driver)
	driver.CaptureCatalog([]byte(`{"models":[{"slug":"model","multi_agent_reasoning_effort":"high","supported_reasoning_levels":[{"effort":"high"},{"effort":"max"},{"effort":"ultra"}]}]}`))
	for _, oneoff := range []bool{false, true} {
		for _, effort := range []string{"ultra", "max"} {
			t.Run(fmt.Sprintf("oneoff=%t/%s", oneoff, effort), func(t *testing.T) {
				transport := &requestTransport{id: "fork"}
				requests := NewRequests[struct{}](transport)
				requests.InheritedEffort = driver.InheritedEffort
				side := &Side{SideAnswer: &conversationmodel.SideAnswer{OneOff: oneoff, Pending: conversationmodel.Input{Text: "task"}}, Busy: true}
				if _, err := requests.StartSide(side, SideMain{Thread: "main", Model: "model", Effort: effort}, time.Time{}); err != nil {
					t.Fatal(err)
				}
				want := effort
				if effort == "ultra" {
					want = "high"
				}
				params := transport.sends[0].params.(map[string]any)
				if got := params["config"].(map[string]any)["model_reasoning_effort"]; got != want || side.Effort != want {
					t.Fatalf("fork effort=%v side=%s, want %s", got, side.Effort, want)
				}
			})
		}
	}
}

func TestSideForkWaitsForCatalogAndRestoresOnFailure(t *testing.T) {
	transport := &requestTransport{id: "fork"}
	requests := NewRequests[struct{}](transport)
	requests.CatalogPending = true
	side := &Side{SideAnswer: &conversationmodel.SideAnswer{OneOff: true, Pending: conversationmodel.Input{Text: "task"}}, Busy: true}
	main := SideMain{Thread: "main", Model: "model", Effort: "ultra"}
	if _, err := requests.StartSide(side, main, time.Time{}); err != nil || len(transport.sends) != 0 {
		t.Fatalf("fork did not wait for config: %v", err)
	}
	requests.CatalogPending = false
	update, err := requests.StartSide(side, main, time.Time{})
	if err != nil || !update.NoticeError || update.Restore.Text != "task" || len(transport.sends) != 0 {
		t.Fatalf("unavailable catalog did not restore input: %+v %v", update, err)
	}
}

func TestArchiveSidesDrainsClosedNativeThread(t *testing.T) {
	transport := &requestTransport{id: "archive"}
	requests := NewRequests[struct{}](transport)
	side := &Side{SideAnswer: new(conversationmodel.SideAnswer), Thread: "side"}
	side.release.HostClosed()
	requests.TrackSide(side.Thread, side)
	messages := make(chan appserver.Message, 1)
	messages <- appserver.Message{ID: []byte("archive"), Result: []byte(`{}`)}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := requests.ArchiveSides(ctx, messages, side); err != nil {
		t.Fatal(err)
	}
	if len(transport.sends) != 1 || transport.sends[0].method != "thread/archive" || !side.Archived() || requests.HasSides() {
		t.Fatalf("closed native history was not archived once: %+v %+v", transport.sends, side)
	}
}

func TestArchiveSidesDrainsDismissedPendingFork(t *testing.T) {
	transport := &requestTransport{id: "fork"}
	requests := NewRequests[struct{}](transport)
	side := &Side{SideAnswer: &conversationmodel.SideAnswer{Pending: conversationmodel.Input{Text: "question"}}, Busy: true}
	if _, err := requests.StartSide(side, SideMain{Thread: "main"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	side.Close()
	if err := requests.ReleaseSide(side); err != nil {
		t.Fatal(err)
	}
	// The dismissed slot is gone; only the pending request owns its delayed fork.
	transport.id = "archive"
	messages := make(chan appserver.Message, 2)
	messages <- appserver.Message{ID: []byte("fork"), Result: []byte(`{"thread":{"id":"side"}}`)}
	messages <- appserver.Message{ID: []byte("archive"), Result: []byte(`{}`)}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := requests.ArchiveSides(ctx, messages); err != nil {
		t.Fatal(err)
	}
	if !side.Archived() || requests.HasSides() || len(transport.sends) != 2 || transport.sends[1].method != "thread/archive" {
		t.Fatalf("dismissed fork survived cleanup: %+v %+v", side, transport.sends)
	}
}

func TestSideReplyHistoryAdmission(t *testing.T) {
	for _, result := range []string{`{"turn":{"id":"turn"}}`, `{}`} {
		for _, replies := range []int{0, 1} {
			t.Run(fmt.Sprintf("result=%s/replies=%d", result, replies), func(t *testing.T) {
				requests := NewRequests[struct{}](&requestTransport{id: "reply"})
				side := &Side{SideAnswer: new(conversationmodel.SideAnswer), Thread: "side"}
				input := conversationmodel.Input{Text: "input", ReplyCount: replies}
				if _, err := requests.ReplySide(side, "side", "", input); err != nil {
					t.Fatal(err)
				}
				response, _ := requests.Take("reply")
				if _, err := requests.ObserveSide(appserver.Message{Result: []byte(result)}, response, "main"); err != nil {
					t.Fatal(err)
				}
				want := 0
				if result != `{}` && replies == 0 {
					want = 1
				}
				if len(side.Drafts.Editor.InputHistory) != want {
					t.Fatalf("history = %+v", side.Drafts.Editor.InputHistory)
				}
			})
		}
	}
}

func TestSideWorkflowWithoutRenderer(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "closed during fork"}[closed], func(t *testing.T) {
			answer := &conversationmodel.SideAnswer{Pending: conversationmodel.Input{Text: "question"}}
			side := &Side{SideAnswer: answer, Busy: true}
			transport := &requestTransport{id: "fork"}
			requests := NewRequests[struct{}](transport)
			main := SideMain{Thread: "main", Blocked: true}
			if _, err := requests.StartSide(side, main, time.Time{}); err != nil || len(transport.sends) != 0 {
				t.Fatal("fork escaped Main's pending submission")
			}
			main.Blocked = false
			if _, err := requests.StartSide(side, main, time.Time{}); err != nil {
				t.Fatal(err)
			}
			requests.Retire("main")
			if closed {
				side.Close()
			}
			fork, found := requests.Take("fork")
			if !found || fork.Side != side || side.SideAnswer != answer {
				t.Fatal("side correlation or Conversation owner changed")
			}
			transport.id = "next"
			update, err := requests.ObserveSide(appserver.Message{Result: []byte(`{"thread":{"id":"side"}}`)}, fork, "replacement-main")
			if err != nil || !update.Handled || !requests.IsSideThread("side") || update.Flush == closed {
				t.Fatalf("fork result: update=%+v err=%v", update, err)
			}
			if closed {
				if transport.sends[len(transport.sends)-1].method != "thread/archive" || answer.Pending.Text != "" {
					t.Fatal("closed fork sampled or retained its visible draft")
				}
				response, _ := requests.Take("next")
				if _, err := requests.ObserveSide(appserver.Message{}, response, "replacement-main"); err != nil || requests.IsSideThread("side") {
					t.Fatal("unsubscribe failed to retire side classification")
				}
				return
			}
			update, err = requests.StartSide(side, main, time.Time{})
			if err != nil || !update.TurnRequested || transport.sends[len(transport.sends)-1].method != "turn/start" {
				t.Fatal("classified side did not start through the existing ledger")
			}
			if _, err := requests.ObserveSide(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"side","turn":{"id":"turn","status":"completed"}}`)}, Request[struct{}]{}, "main"); err != nil {
				t.Fatal(err)
			}
			response, _ := requests.Take("next")
			if _, err := requests.ObserveSide(appserver.Message{Result: []byte(`{"turn":{"id":"turn"}}`)}, response, "main"); err != nil || side.Turn != "" || side.Busy {
				t.Fatal("late start acknowledgement revived completed side work")
			}
		})
	}
}

func TestSideAttachmentSubmissionIdentity(t *testing.T) {
	body := "immutable side snapshot\n"
	envelope := attachments.Encode(composer.FileFrames("/workspace/side.txt", body))
	var previous string
	for range 2 {
		side := &Side{SideAnswer: &conversationmodel.SideAnswer{Pending: conversationmodel.Input{Text: "question", Attachments: []string{envelope}}}, Thread: "side", Busy: true}
		transport := &requestTransport{id: "start"}
		requests := NewRequests[struct{}](transport)
		update, err := requests.StartSide(side, SideMain{Thread: "main"}, time.Time{})
		if err != nil || !update.TurnRequested {
			t.Fatalf("side start: %+v %v", update, err)
		}
		params := transport.sends[0].params.(map[string]any)
		input := params["input"].([]map[string]any)
		snapshot, ok := attachments.DecodeSnapshot(input[len(input)-1]["text"].(string))
		if !ok || snapshot.ID == "" || snapshot.ID == previous || snapshot.Frames[0] != composer.FileFrames("/workspace/side.txt", body)[0] {
			t.Fatal("side submission lost unique identity or original contents")
		}
		previous = snapshot.ID
	}
}

func TestSideSteeringAttachmentPreparation(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		t.Run(fmt.Sprint(oversize), func(t *testing.T) {
			side := &Side{SideAnswer: &conversationmodel.SideAnswer{ID: 1}, Thread: "side", Turn: "turn", Busy: true}
			transport := &requestTransport{id: "steer"}
			requests := NewRequests[struct{}](transport)
			input := conversationmodel.Input{Text: "read attached", Attachments: []string{attachments.Encode(composer.FileFrames("/workspace/file.txt", "same bytes"))}}
			if oversize {
				input.Text = strings.Repeat("x", codexresponses.UserTextLimit)
			}
			update, err := requests.ReplySide(side, "side", "turn", input)
			if err != nil {
				t.Fatal(err)
			}
			if oversize {
				if !update.Handled || !update.NoticeError || update.Restore.Text != input.Text || len(update.Restore.Attachments) != 1 || len(transport.sends) != 0 || !side.Busy {
					t.Fatal("invalid steering was dispatched or lost instead of restored")
				}
				return
			}
			params := transport.sends[0].params.(map[string]any)
			parts := params["input"].([]map[string]any)
			snapshot, ok := attachments.DecodeSnapshot(parts[len(parts)-1]["text"].(string))
			if !ok || snapshot.ID == "" || !strings.HasPrefix(snapshot.ID, params["clientUserMessageId"].(string)) {
				t.Fatalf("steering attachment lost submission identity: %+v", snapshot)
			}
		})
	}
}
