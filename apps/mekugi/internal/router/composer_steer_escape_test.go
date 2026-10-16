package router

import (
	"fmt"
	"testing"
	"time"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/libraries/diffrender"
)

func composerShellEscape(t *testing.T, u *appServerUI) {
	t.Helper()
	composerShellKeys(t, u, "\x1b")
	u.shell.sequenceAt = time.Now().Add(-time.Second)
	if err := u.shell.flushEscape(); err != nil {
		t.Fatal(err)
	}
}

func TestComposerEscapeExpeditesSteersInHostEventOrder(t *testing.T) {
	for _, order := range []string{"sie", "sei", "ise", "ies", "esi", "eis"} {
		for _, rejectedSteer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rejected=%v", order, rejectedSteer), func(t *testing.T) {
				u, wire := newAppServerTestUI()
				u.ensureShell()
				t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
				appServerTestTurn(t, u, "t")
				appServerTestKeys(t, u, "older\r")
				older := appServerOneRequest(t, wire, "turn/steer", "older")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, older.ID))
				appServerTestKeys(t, u, "first\r")
				steer := appServerOneRequest(t, wire, "turn/steer", "first")
				appServerTestKeys(t, u, "second\rqueued\tdraft\x1b[D")
				composerShellEscape(t, u)
				interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
				// Repeated Escape must neither retract nor request another interrupt.
				composerShellEscape(t, u)
				for i, event := range order {
					switch event {
					case 's':
						response := `"result":{"turnId":"t"}`
						if rejectedSteer {
							response = `"error":{"code":-1,"message":"no active turn to steer"}`
						}
						appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, steer.ID, response))
					case 'i':
						appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
					case 'e':
						appServerTestTurnEnd(t, u, "t", "interrupted")
					}
					if i < 2 && wire.Len() != 0 {
						t.Fatalf("resubmitted before host settled: %s", wire.String())
					}
					if i < 2 {
						composerShellEscape(t, u)
						if wire.Len() != 0 || u.Text != "draft" {
							t.Fatalf("repeated Escape disturbed settlement after %c: draft=%q wire=%s", event, u.Text, wire.String())
						}
					}
				}
				appServerOneRequest(t, wire, "turn/start", "older\nfirst\nsecond")
				if u.Text != "draft" || u.CursorBack != 1 || len(u.queued) != 1 || u.queued[0].Text != "queued" {
					t.Fatalf("expedite altered draft or queue: draft=%q caret=%d queued=%+v", u.Text, u.CursorBack, u.queued)
				}
				appServerTestTurn(t, u, "expedited")
				if wire.Len() != 0 {
					t.Fatalf("repeated Escape interrupted the new turn: %s", wire.String())
				}
			})
		}
	}
}

func TestComposerEscapeCommittedSteerIsNotResent(t *testing.T) {
	for _, unsent := range []bool{false, true} {
		t.Run(fmt.Sprint(unsent), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ensureShell()
			t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
			appServerTestTurn(t, u, "t")
			appServerTestKeys(t, u, "first\r")
			steer := appServerOneRequest(t, wire, "turn/steer", "first")
			if unsent {
				appServerTestKeys(t, u, "second\r")
			}
			appServerTestKeys(t, u, "queued\tdraft")
			composerShellEscape(t, u)
			interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
			appServerTestUserMessage(t, u, "committed", steer.Params.ClientUserMessageID, "first")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
			appServerTestTurnEnd(t, u, "t", "interrupted")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
			if unsent {
				appServerOneRequest(t, wire, "turn/start", "second")
				if u.Text != "draft" || len(u.queued) != 1 {
					t.Fatal("remaining steer did not preserve editor and queue")
				}
			} else if wire.Len() != 0 || u.Text != "queued\ndraft" {
				t.Fatalf("committed input or queue was resent: draft=%q wire=%s", u.Text, wire.String())
			}
		})
	}
}

func TestComposerEscapeRejectedInterruptDoesNotResend(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(completionFirst), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ensureShell()
			t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
			appServerTestTurn(t, u, "t")
			appServerTestKeys(t, u, "first\r")
			steer := appServerOneRequest(t, wire, "turn/steer", "first")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
			appServerTestKeys(t, u, "queued\tdraft")
			composerShellEscape(t, u)
			interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
			if completionFirst {
				appServerTestTurnEnd(t, u, "t", "interrupted")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, interrupt.ID))
			if !completionFirst {
				appServerTestTurnEnd(t, u, "t", "interrupted")
			}
			if wire.Len() != 0 || u.Text != "first\nqueued\ndraft" {
				t.Fatalf("rejected interrupt delivered input: draft=%q wire=%s", u.Text, wire.String())
			}
		})
	}
}

func TestComposerEscapeExpeditesAttachments(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	appServerTestTurn(t, u, "t")
	u.insertImage("first.png")
	u.insertSelection("message", "exact quote", "")
	appServerTestKeys(t, u, "\r")
	steer := appServerTurnRequests(t, wire)[0]
	u.insertImage("second.png")
	appServerTestKeys(t, u, "\r")
	composerShellEscape(t, u)
	interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	appServerTestTurnEnd(t, u, "t", "interrupted")
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "turn/start" {
		t.Fatalf("requests=%+v", requests)
	}
	input := conversationbinding.Input(u.inputPayloads[u.Submission().ID].Draft)
	if input[0]["path"] != "first.png" || input[2]["path"] != "second.png" || len(u.inputPayloads[u.Submission().ID].Selections) != 1 || u.inputPayloads[u.Submission().ID].Selections[0].Text != "exact quote" || u.inputPayloads[u.Submission().ID].Text != "[Image 1][Selected message] \n[Image 2]" {
		t.Fatalf("expedite corrupted attachments: %+v draft=%+v", input, u.inputPayloads[u.Submission().ID].composerDraft)
	}
}

func TestComposerCtrlCCancelsEscapeDelivery(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(completionFirst), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ensureShell()
			t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
			appServerTestTurn(t, u, "t")
			appServerTestKeys(t, u, "first\r")
			steer := appServerOneRequest(t, wire, "turn/steer", "first")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
			appServerTestKeys(t, u, "queued\t")
			composerShellEscape(t, u)
			interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
			if completionFirst {
				appServerTestTurnEnd(t, u, "t", "interrupted")
			}
			composerShellKeys(t, u, "\x03")
			if !completionFirst {
				appServerTestTurnEnd(t, u, "t", "interrupted")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
			if wire.Len() != 0 || u.Text != "first\nqueued" {
				t.Fatalf("Ctrl-C failed to restore: draft=%q wire=%s", u.Text, wire.String())
			}
		})
	}
}

func TestComposerEscapeContextPrecedesSteerDelivery(t *testing.T) {
	for _, context := range []string{"selection", "scrollback", "picker", "locked"} {
		t.Run(context, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ensureShell()
			t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
			appServerTestTurn(t, u, "t")
			appServerTestKeys(t, u, "first\r")
			steer := appServerOneRequest(t, wire, "turn/steer", "first")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
			switch context {
			case "selection":
				u.shell.selection = &terminalSelection{moved: true}
			case "scrollback":
				u.view.following = false
			case "picker":
				appServerTestKeys(t, u, "/")
			case "locked":
				u.interruptLocked = true
			}
			composerShellEscape(t, u)
			if wire.Len() != 0 || len(u.Steers()) != 1 {
				t.Fatalf("contextual Escape touched steer: wire=%s steers=%+v", wire.String(), u.Steers())
			}
		})
	}
}

func TestComposerEscapeExpeditesOnlyLocallyWaitingSteer(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	appServerTestTurn(t, u, "t")
	u.Settings.BeginLive()
	appServerTestKeys(t, u, "first\rdraft")
	if wire.Len() != 0 {
		t.Fatal("settings did not hold input")
	}
	composerShellEscape(t, u)
	interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	appServerTestTurnEnd(t, u, "t", "interrupted")
	if wire.Len() != 0 {
		t.Fatal("Escape bypassed pending settings")
	}
	u.Settings = sessioncontrol.Settings{}
	if err := u.flushInput(); err != nil {
		t.Fatal(err)
	}
	appServerOneRequest(t, wire, "turn/start", "first")
	if u.Text != "draft" {
		t.Fatalf("local steer restored over draft: %q", u.Text)
	}
}

func TestUISnapshotNativeExpeditedSteer(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.view.painter.Theme = diffrender.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	appServerTestTurn(t, u, "t")
	u.turnStarted = u.now()
	appServerTestKeys(t, u, "Apply this instruction now.\r")
	steer := appServerOneRequest(t, wire, "turn/steer", "Apply this instruction now.")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
	appServerTestKeys(t, u, "Next turn stays queued.\tKeep editing this draft.")
	composerShellEscape(t, u)
	rows, _ := u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "native-expedited-steer", rows)
}
