package launch

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func sideNativeWait(t *testing.T, u *appServerUI, thread string, deadline *int64) {
	t.Helper()
	b := u.Client.Requests.Side(thread)
	if b == nil {
		t.Fatal("missing native side root")
	}
	disposition := "waiting"
	if deadline == nil {
		disposition = "paused"
	}
	b.ObserveNativeWork(appserver.AgentObservation{ThreadID: thread, Work: &appserver.AgentWorkObservation{WorkID: thread + "-work", Disposition: disposition}, Wait: &appserver.AgentWaitObservation{WaitID: thread + "-wait", Reason: new("providerUsageLimit"), DeadlineUnixMS: deadline}})
}

func TestTemporaryUsageLimitWaitingAndClose(t *testing.T) {
	for _, timed := range []bool{false, true} {
		for _, closure := range []string{"explicit", "thread/closed", "thread/archived", "interrupt"} {
			t.Run(fmt.Sprintf("%t/%s", timed, closure), func(t *testing.T) {
				u, wire := newAppServerTestUI()
				defer terminalTest(u).Diff.Close()
				appServerTestKeys(t, u, "/oneoff question\r")
				btwTestReply(t, u, btwTestRequest(t, wire, "thread/fork", "main"), `{"thread":{"id":"side"}}`)
				btwTestReply(t, u, btwTestRequest(t, wire, "turn/start", "side"), `{"turn":{"id":"side-turn"}}`)
				var deadline *int64
				if timed {
					deadline = new(time.Now().Add(time.Hour).UnixMilli())
				}
				sideNativeWait(t, u, "side", deadline)
				u.sideFrame(100, 24)
				status := u.attached.presentation.View().Status.Composer
				if status.Running != timed || !strings.Contains(strings.ToLower(status.Text), "usage limit") || u.side.Busy != timed || u.side.Turn != "side-turn" || !u.attached.presentation.View().InterruptAvailable {
					t.Fatalf("native side wait completed: %+v", status)
				}
				if timed {
					u.side.ObserveNativeWork(appserver.AgentObservation{ThreadID: "side", Work: &appserver.AgentWorkObservation{WorkID: "side-work", Disposition: "running"}})
					if u.side.Waiting != "" || !u.side.Busy || u.side.Turn != "side-turn" {
						t.Fatal("native continuation lost side identity")
					}
					sideNativeWait(t, u, "side", deadline)
				}
				switch closure {
				case "interrupt":
					if err := terminalTest(u).send("\x1b"); err != nil {
						t.Fatal(err)
					}
					request := btwTestRequest(t, wire, "agent/interrupt", "side")
					var params map[string]any
					raw, err := json.Marshal(request.Params)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &params); err != nil {
						t.Fatal(err)
					}
					// The request decoder keeps threadId; the actual transport is covered by
					// the sessioncontrol side-native fixture below for rootThreadId as well.
					btwTestReply(t, u, request, `{}`)
					if u.side == nil || u.side.Closed || u.side.Waiting == "" || u.side.Busy != timed {
						t.Fatal("interrupt ack settled side work")
					}
					u.side.ObserveNativeWork(appserver.AgentObservation{ThreadID: "side", RootThreadID: "side", Work: &appserver.AgentWorkObservation{WorkID: "side-work", Disposition: "interrupted"}})
					if u.side.Waiting != "" || u.side.Busy {
						t.Fatal("native interruption did not settle side")
					}
				case "explicit":
					if err := closeSide(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, &u.side, &u.ownedImages); err != nil {
						t.Fatal(err)
					}
					btwTestReply(t, u, btwTestRequest(t, wire, "turn/interrupt", "side"), `{}`)
					btwTestReply(t, u, btwTestRequest(t, wire, "thread/archive", "side"), `{}`)
				default:
					appServerTestNotify(t, u, closure, map[string]any{"threadId": "side"})
					if !u.side.Unloaded() || !u.RetiredThread("side", "") {
						t.Fatal("native closure did not retire side")
					}
				}
				if wire.Len() != 0 {
					t.Fatalf("Go replayed a native wake: %s", wire.String())
				}
			})
		}
	}
}

func TestTemporaryUsageLimitExplicitInputSupersedesWait(t *testing.T) {
	for _, command := range []string{"/btw question", "/oneoff question"} {
		for _, resumed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", command, resumed), func(t *testing.T) {
				u, wire := newAppServerTestUI()
				u.attached.ctx = t.Context()
				defer terminalTest(u).Diff.Close()
				appServerTestKeys(t, u, command+"\r")
				btwTestReply(t, u, btwTestRequest(t, wire, "thread/fork", "main"), `{"thread":{"id":"side"}}`)
				btwTestReply(t, u, btwTestRequest(t, wire, "turn/start", "side"), `{"turn":{"id":"side-turn"}}`)
				b := u.side
				if command == "/btw question" {
					b = u.BTW
				}
				sideNativeWait(t, u, "side", nil)
				b.Drafts.Queued = append(b.Drafts.Queued, conversationmodel.Input{Text: "queued follow-up"})
				flush := func() {
					t.Helper()
					if err := flushBTW(&u.attached, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, preferences.Applied{}, &u.side, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations); err != nil {
						t.Fatal(err)
					}
				}
				flush()
				if wire.Len() != 0 || b.Waiting == "" {
					t.Fatal("queued input superseded native wait")
				}
				if resumed {
					b.ObserveNativeWork(appserver.AgentObservation{ThreadID: "side", Work: &appserver.AgentWorkObservation{WorkID: "side-work", Disposition: "running"}})
				}
				text := "new instruction"
				if command == "/btw question" {
					text = "/btw " + text
				}
				appServerTestKeys(t, u, text+"\r")
				flush()
				if resumed && command == "/btw question" {
					if wire.Len() != 0 || u.Editor.Text != text || len(b.Drafts.Queued) != 1 {
						t.Fatal("running BTW input bypassed its ordinary busy-draft policy")
					}
					return
				}
				method := "turn/steer"
				request := btwTestRequest(t, wire, method, "side")
				if !strings.Contains(fmt.Sprint(request.Params.Input), "new instruction") || len(b.Drafts.Queued) != 1 || b.Drafts.Queued[0].Text != "queued follow-up" {
					t.Fatal("direct native input lost text or queued follow-up")
				}
				if request.Params.ExpectedTurnID != "side-turn" || request.Params.Environments != nil {
					t.Fatal("side steering used wrong turn or start-only settings")
				}
				btwTestReply(t, u, request, `{"turn":{"id":"new-turn"}}`)
				if len(u.attached.sideInputs) != 0 {
					t.Fatal("native-dispatched side input retained its local lock")
				}
			})
		}
	}
}

func TestUsageLimitOneoffPreservesIndependentChildReplies(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprint(running), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			defer terminalTest(u).Diff.Close()
			u.startSession("main", "/workspace")
			oneoffTestStart(t, u, wire)
			oneoffTestChild(t, u)
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "child-turn"}})
			sideNativeWait(t, u, "oneoff", nil)
			if u.side.Waiting == "" {
				t.Fatal("usage limit did not pause One-off")
			}
			oneoffTestAsync(t, u, "oneoff-child", "child-turn", "child-question")
			if !running {
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "child-turn", "status": "completed"}})
			}
			u.side.Drafts.Unsent = append(u.side.Drafts.Unsent, conversationmodel.Input{Text: "next root input"})
			selectSideThread(t, u, u.attached.presentation.View().Main)
			oneoffTestAnswer(t, u)
			method := "turn/steer"
			if !running {
				method = "turn/start"
			}
			btwTestRequest(t, wire, method, "oneoff-child")
			if u.side.Waiting == "" || len(u.side.Drafts.Unsent) != 1 || u.side.Drafts.Unsent[0].Text != "next root input" {
				t.Fatal("child reply consumed root input or canceled its wait")
			}
		})
	}
}
