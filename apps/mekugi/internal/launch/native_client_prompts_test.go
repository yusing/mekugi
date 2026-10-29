package launch

import (
	"errors"
	"fmt"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

func TestNativeClientPromptDeliveryRoutesInput(t *testing.T) {
	for _, kind := range []string{"question", "secret", "approval"} {
		for _, input := range []string{"key", "paste"} {
			t.Run(kind+"/"+input, func(t *testing.T) {
				u, wire := newAppServerTestUI()
				u.startSession("main", "/workspace")
				if kind == "approval" {
					approvalTestCommand(t, u, "approval", map[string]any{"availableDecisions": []string{"accept", "decline"}})
				} else {
					questionTestSync(t, u, "question", kind == "secret")
				}
				client := nativeTestPromptClient(u)
				terminal := client.renderer.terminal
				terminal.Width, terminal.Height = 100, 24
				send := func(text string) {
					if input == "key" {
						client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: text})
						return
					}
					client.dispatch(terminalsession.Input{Kind: terminalsession.PasteStart})
					client.refresh(time.Now()) // A frame refresh may run between paste fragments.
					client.dispatch(terminalsession.Input{Kind: terminalsession.PasteInput, Text: text})
					client.dispatch(terminalsession.Input{Kind: terminalsession.PasteEnd})
				}
				send("x")
				if client.editor("main").Text != "x" || client.prompts.questions.Active != nil || client.prompts.approvals.Open || terminal.Prompts.Secret.Text != "" || len(client.calls) != 0 || wire.Len() != 0 {
					t.Fatal("an unseen prompt captured ordinary input or dispatched work")
				}
				client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x03"})
				if kind == "approval" && !client.prompts.approvals.Open || kind != "approval" && client.prompts.questions.Active == nil {
					t.Fatal("yielding to the draft disabled automatic prompt opening")
				}
				frame, err := client.renderer.render(t.Context(), io.Discard, time.Now(), mekugiVersion())
				if err != nil || !frame.PromptVisible || len(frame.Demands) != 0 {
					t.Fatalf("prompt delivery: visible=%v demands=%d err=%v", frame.PromptVisible, len(frame.Demands), err)
				}
				send("y")
				editor, _ := client.selectedEditor()
				if editor.Text != "y" || client.editor("main").Text != "" || len(client.calls) != 0 || wire.Len() != 0 {
					t.Fatal("a delivered prompt lost its input destination or dispatched work")
				}
				if question := client.prompts.questions.Current(); input == "paste" && kind == "question" && question.Selected != len(question.Choices) {
					t.Fatal("pasted answer retained an unrelated selected choice")
				}
				if kind == "secret" {
					terminal.Prompts.Reopen()
					send("z")
					if client.editor("main").Text != "z" || terminal.Prompts.Secret.Text != "" {
						t.Fatal("suspending a reopened secret mixed its draft with ordinary input")
					}
					client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x03"})
					if editor, _ := client.selectedEditor(); editor.Text != "y" {
						t.Fatal("suspending an unseen secret lost its parked editor")
					}
				}
			})
		}
	}
}

func TestNativeClientOpenPromptsPreservesDeliveredTarget(t *testing.T) {
	for _, kind := range []string{"question", "approval"} {
		for _, state := range []string{"visible", "reopened"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				u, _ := newAppServerTestUI()
				u.startSession("main", "/workspace")
				if kind == "approval" {
					approvalTestCommand(t, u, "approval", map[string]any{"availableDecisions": []string{"accept", "decline"}})
				} else {
					questionTestSync(t, u, "question", false)
				}
				client := nativeTestPromptClient(u)
				terminal := client.renderer.terminal
				terminal.Width, terminal.Height = 100, 24
				frame, err := client.renderer.render(t.Context(), io.Discard, time.Now(), mekugiVersion())
				if err != nil || !frame.PromptVisible || !terminal.Prompts.Ready() {
					t.Fatalf("prompt delivery: visible=%v err=%v", frame.PromptVisible, err)
				}
				send := func(key string) {
					client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: key})
				}
				if state == "reopened" {
					send("\x1b")
					if client.prompts.questions.Active != nil || client.prompts.approvals.Open {
						t.Fatal("Escape did not hide the delivered prompt")
					}
				}
				send("\x02")
				send("q")
				if state == "reopened" {
					if terminal.Prompts.Ready() {
						t.Fatal("a reopened prompt reused its previous delivery")
					}
					send("x")
					if client.editor("main").Text != "x" || len(client.calls) != 0 {
						t.Fatal("a reopened unseen prompt captured ordinary input")
					}
					return
				}
				send("1")
				send("\r")
				if len(client.calls) != 1 || client.editor("main").Text != "" {
					t.Fatal("opening an already delivered prompt redirected its answer to Main")
				}
				operation := client.calls[0].operation
				if kind == "question" {
					if operation.Answers == nil || len(operation.Answers.Answers) != 1 || operation.Answers.Answers[0].Choice != 0 {
						t.Fatal("the delivered question did not submit its selected answer")
					}
				} else if operation.Approval == nil || operation.Approval.Choice != 0 {
					t.Fatal("the delivered approval did not submit its selected decision")
				}
			})
		}
	}
}

func TestNativeClientEscapeDistinguishesUnseenAndDeliveredPrompt(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	appServerTestTurn(t, u, "turn")
	questionTestSync(t, u, "question", false)
	client := nativeTestPromptClient(u)
	terminal := client.renderer.terminal
	terminal.Width, terminal.Height = 100, 24
	client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
	if len(client.calls) != 1 || client.calls[0].operation.Interrupt == nil || !client.prompts.questions.AutoOpen {
		t.Fatal("an unseen prompt captured Escape or disabled automatic opening")
	}
	client.calls = nil
	frame, err := client.renderer.render(t.Context(), io.Discard, time.Now(), mekugiVersion())
	if err != nil || !frame.PromptVisible {
		t.Fatalf("prompt delivery: visible=%v err=%v", frame.PromptVisible, err)
	}
	client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
	if client.prompts.questions.Active != nil || len(client.calls) != 0 {
		t.Fatal("Escape did not stop after dismissing the delivered prompt")
	}
}

func TestNativeClientUndeliveredPromptFramesYieldInput(t *testing.T) {
	for _, failure := range []string{"write", "covered"} {
		t.Run(failure, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			questionTestSync(t, u, "question", false)
			client := nativeTestPromptClient(u)
			terminal := client.renderer.terminal
			terminal.Width, terminal.Height = 100, 24
			var writer io.Writer = io.Discard
			writeFailure := errors.New("terminal write failed")
			if failure == "write" {
				writer = serverErrorWriter{err: writeFailure}
			} else {
				terminal.Dialog = &terminalsession.ContentDialog{View: terminal.Transcript}
				terminal.Dialog.Update(sessionpresentation.DetailView{Pages: []sessionpresentation.DetailItem{{ID: "covered"}}})
			}
			frame, err := client.renderer.render(t.Context(), writer, time.Now(), mekugiVersion())
			if failure == "write" && !errors.Is(err, writeFailure) || failure == "covered" && (err != nil || frame.PromptVisible) {
				t.Fatalf("undelivered fixture: visible=%v err=%v", frame.PromptVisible, err)
			}
			terminal.Dialog = nil
			client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "x"})
			if client.editor("main").Text != "x" || client.prompts.questions.Active != nil || len(client.calls) != 0 {
				t.Fatal("an undelivered frame made the prompt capture input")
			}
			client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x03"})
			frame, err = client.renderer.render(t.Context(), io.Discard, time.Now(), mekugiVersion())
			if err != nil || !frame.PromptVisible {
				t.Fatalf("retry delivery: visible=%v err=%v", frame.PromptVisible, err)
			}
			client.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "2"})
			if question := client.prompts.questions.Current(); question == nil || question.Selected != 1 || client.editor("main").Text != "" {
				t.Fatal("successful retry did not admit prompt input")
			}
		})
	}
}

func nativeTestPromptClient(u *appServerUI) *nativeClient {
	u.testMainFrame(80, 18, 0)
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	terminal.Transcript.Painter.Theme = diffrender.DarkTheme
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Questions: nativePendingQuestions(u.attached.conversation), Approvals: nativePendingApprovals(u.attached.conversation), Main: u.attached.source.Publication(u.attached.presentation, nil)}
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}, event: sessionapi.NativeEvent{Attachment: "client", Frame: frame}, editors: make(map[string]*composer.Editor)}
	c.prompts.update(&frame)
	c.refresh(presentationTime(u.clock))
	return c
}

func nativeTestPromptKey(t *testing.T, c *nativeClient, key string) {
	t.Helper()
	input := c.renderer.terminal.Main.PromptKey(key, c.renderer.terminal.Prompts)
	if input == nil {
		t.Fatal("prompt is hidden")
	}
	c.prompt(*input, nil)
	c.refresh(time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
}

func TestNativeClientPromptSelectionDraftAndRetirementIsolation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	questionTestSync(t, u, "call", false)
	first, second := nativeTestPromptClient(u), nativeTestPromptClient(u)
	nativeTestPromptKey(t, first, "\x1b[B")
	nativeTestPromptKey(t, first, "\t")
	editor, _ := first.selectedEditor()
	editor.InsertText("first client's note")
	nativeTestPromptKey(t, first, "\x1b") // leave note editing
	nativeTestPromptKey(t, first, "\x1b") // hide, retaining the local editor
	if first.prompts.questions.Active != nil || second.prompts.questions.Current().Selected != 0 || u.questions.Current().Selected != 0 || wire.Len() != 0 {
		t.Fatal("local selection/navigation reached another client or execution")
	}
	if status := first.renderer.frame.View.Status; status.Composer != status.Session || status.ComposerOverride {
		t.Fatalf("hidden prompt retained a prompt-owned composer status: %+v", status)
	}
	first.editor("main").InsertText("ordinary draft")
	first.openPrompts()
	first.refresh(time.Now())
	if current, _ := first.selectedEditor(); current.Text != "first client's note" || first.prompts.questions.Current().Selected != 1 {
		t.Fatal("hide/reopen lost client draft or selection")
	}
	if status := first.renderer.frame.View.Status; !strings.HasSuffix(status.Composer.Text, " · draft kept") || !status.ComposerOverride {
		t.Fatalf("reopened prompt omitted the local parked draft: %+v", status)
	}
	refreshed := second.event.Frame
	first.prompts.update(&refreshed)
	first.refresh(time.Now())
	if first.prompts.questions.Current().Selected != 1 {
		t.Fatal("server refresh replaced local selection")
	}
	replacement := second.event
	replacement.Attachment = "replacement-client"
	first.accept(replacement)
	first.openPrompts()
	first.refresh(time.Now())
	if editor, _ := first.selectedEditor(); editor.Text != "" || first.prompts.questions.Current().Selected != 0 {
		t.Fatal("a new stream inherited a reused prompt identity's editor or selection")
	}
	retired := refreshed
	retired.Questions = nil
	first.reconcilePromptEditors(&retired)
	first.prompts.update(&retired)
	second.reconcilePromptEditors(&retired)
	second.prompts.update(&retired)
	if len(first.editor("main").InputHistory) != 1 || first.editor("main").InputHistory[0].Text != "first client's note" || len(second.editor("main").InputHistory) != 0 {
		t.Fatal("ordinary restoration crossed clients or lost unsent draft")
	}
}

func TestNativeClientSecretCompletionIsOnlyFinalNativeInput(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	questionTestSync(t, u, "secret", true)
	first, second := nativeTestPromptClient(u), nativeTestPromptClient(u)
	q := first.event.Frame.View.Questions
	first.prompt(terminalsession.PromptInput{Version: first.event.Frame.View.Version, PromptKey: conversationui.PromptKey{Question: prompt.QuestionInput{Call: q.Call.ID, Question: q.Call.Questions[0].ID, Kind: prompt.InputChoose, Choice: len(q.Call.Questions[0].Choices)}}}, nil)
	first.renderer.terminal.Prompts.Secret.InsertText("fixture-private-secret")
	first.refresh(time.Now())
	if first.event.Frame.View.PromptDraft.Draft.Text != "" || second.renderer.terminal.Prompts.Secret.Text != "" || wire.Len() != 0 {
		t.Fatal("secret editor escaped into a publication, sibling, or execution")
	}
	nativeTestPromptKey(t, first, "\r")
	if len(first.calls) != 1 || first.calls[0].operation.Answers == nil {
		t.Fatal("secret completion did not produce final native answer")
	}
	batch := *first.calls[0].operation.Answers
	if batch.Answers[0].Secret != "fixture-private-secret" || batch.Answers[0].Draft.Text != "" {
		t.Fatal("secret was lost or put in ordinary draft payload")
	}
	if refusal := nativeTestAnswers(t, u, batch); refusal != "" {
		t.Fatal(refusal)
	}
	if !strings.Contains(wire.String(), "fixture-private-secret") {
		t.Fatal("host request did not receive explicit final secret")
	}
	replacement := second.event
	replacement.Attachment = "replacement-client"
	first.accept(replacement)
	if first.renderer.terminal.Prompts.Secret.Text != "" || len(first.secrets) != 0 {
		t.Fatal("a new stream inherited a reused secret prompt identity's contents")
	}
	retired := first.event.Frame
	retired.Questions = nil
	first.reconcilePromptEditors(&retired)
	if first.secretKey != "" || first.renderer.terminal.Prompts.Secret.Text != "" || len(first.secrets) != 0 || len(first.editor("main").InputHistory) != 0 {
		t.Fatal("retired secret survived locally or entered ordinary history")
	}
}

func TestUISnapshotNativeClientPromptIsolation(t *testing.T) {
	for _, secret := range []bool{false, true} {
		name := "ordinary"
		if secret {
			name = "secret"
		}
		t.Run(name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return now }
			u.startSession("main", "/workspace")
			questionTestSync(t, u, "snapshot", secret)
			client := nativeTestPromptClient(u)
			if secret {
				client.renderer.terminal.Prompts.Secret.InsertText("fixture-password")
			} else {
				nativeTestPromptKey(t, client, "\t")
				editor, _ := client.selectedEditor()
				editor.InsertText("Only the selected client's note.")
			}
			client.refresh(now)
			terminal := client.renderer.terminal
			painted := terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true, Version: mekugiVersion()}, 80, 18, 0)
			assertNativeUISnapshot(t, "native-client-prompt-"+name, painted.Rows)
			if !secret {
				nativeTestPromptKey(t, client, "\x1b")
				nativeTestPromptKey(t, client, "\x1b")
				client.refresh(now)
				painted = terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true, Version: mekugiVersion()}, 80, 18, 0)
				assertNativeUISnapshot(t, "native-client-prompt-hidden", painted.Rows)
			}
		})
	}
}

func TestNativeClientApprovalReasonAndSelectionIsolation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	approvalTestCommand(t, u, "approval", map[string]any{"availableDecisions": []string{"accept", "decline"}})
	first, second := nativeTestPromptClient(u), nativeTestPromptClient(u)
	nativeTestPromptKey(t, first, "\x1b")
	if status := first.renderer.frame.View.Status; status.Composer != status.Session || status.ComposerOverride {
		t.Fatalf("hidden approval retained a prompt-owned composer status: %+v", status)
	}
	first.openPrompts()
	first.refresh(time.Now())
	nativeTestPromptKey(t, first, "\t")
	editor, _ := first.selectedEditor()
	editor.InsertText("keep this local")
	first.refresh(time.Now())
	nativeTestPromptKey(t, first, "\r")
	if len(first.calls) != 1 || first.calls[0].operation.Approval == nil || first.calls[0].operation.Approval.Reason != "keep this local" {
		t.Fatal("final approval omitted explicit denial reason")
	}
	if second.prompts.approvals.Pending[0].Selected != 0 || u.approvals.Pending[0].Selected != 0 || wire.Len() != 0 {
		t.Fatal("approval navigation changed another client's choice or execution")
	}
}

func TestNativeClientSkippedAnswerRequiresConfirmation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	questionTestSync(t, u, "skip", false)
	client := nativeTestPromptClient(u)
	nativeTestPromptKey(t, client, "\x1d")
	if len(client.calls) != 0 || !client.prompts.questions.Confirm {
		t.Fatal("skip bypassed incomplete-answer confirmation")
	}
	nativeTestPromptKey(t, client, "\r")
	if len(client.calls) != 1 || !client.calls[0].operation.Answers.Incomplete {
		t.Fatal("confirmed skip did not produce one final batch")
	}
	if refusal := nativeTestAnswers(t, u, *client.calls[0].operation.Answers); refusal != "" {
		t.Fatal(refusal)
	}
	if !strings.Contains(wire.String(), `"answers":{}`) {
		t.Fatal("all-skipped native call lost ordinary empty-response semantics")
	}
}

func TestNativeClientRejectedAsyncAnswerRestoresLocalDraft(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	questionTestAsync(t, u, "async", "Who receives it?")
	client, sibling := nativeTestPromptClient(u), nativeTestPromptClient(u)
	nativeTestPromptKey(t, client, "\x1b[A") // Other
	editor, _ := client.selectedEditor()
	editor.InsertText("client's retryable answer")
	siblingEditor, _ := sibling.selectedEditor()
	siblingEditor.InsertText("sibling's unsent draft")
	nativeTestPromptKey(t, client, "\r")
	call := client.calls[0]
	client.calls = nil
	client.runtime = &nativeClientRuntime{active: client, editors: make(map[[2]string]*composer.Editor)}
	client.requests[nativeInputCall] = call
	if refusal := nativeTestAnswers(t, u, *call.operation.Answers); refusal != "" {
		t.Fatal(refusal)
	}
	frame := client.event.Frame
	frame.Questions = nativePendingQuestions(u.attached.conversation)
	client.accept(sessionapi.NativeEvent{Attachment: "client", Frame: frame})
	sibling.accept(sessionapi.NativeEvent{Attachment: "sibling", Frame: frame})
	call.after(nil)
	client.requests[nativeInputCall] = nil
	if client.prompts.questions.Active != nil || sibling.prompts.questions.Active != nil {
		t.Fatal("sent call remained actionable")
	}
	if !u.questions.Calls[0].Reject() {
		t.Fatal("fixture did not reject async delivery")
	}
	frame.Questions = nativePendingQuestions(u.attached.conversation)
	client.accept(sessionapi.NativeEvent{Attachment: "client", Frame: frame})
	if restored, _ := client.selectedEditor(); restored.Text != "client's retryable answer" || client.prompts.questions.Current().Selected != 2 || len(client.calls) != 0 {
		t.Fatal("host rejection lost client-local answer or automatically resubmitted it")
	}
	// A sibling's sent call never makes this client's unsent draft submitted.
	frame.Questions = nil
	sibling.accept(sessionapi.NativeEvent{Attachment: "sibling", Frame: frame})
	if history := sibling.editor("main").InputHistory; len(history) != 1 || history[0].Text != "sibling's unsent draft" {
		t.Fatal("sibling submission discarded this client's ordinary restoration")
	}
}

func TestNativeClientPartialQuestionResolutionKeepsOtherEditor(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	questionTestAsync(t, u, "partial", "First?", "Second?")
	client := nativeTestPromptClient(u)
	first, _ := client.selectedEditor()
	first.InsertText("first unsent draft")
	nativeTestPromptKey(t, client, "\x1b[C")
	second, _ := client.selectedEditor()
	second.InsertText("second remains editable")
	call := u.questions.Calls[0]
	if _, ok := call.Commit(0, "another-client", "answered elsewhere"); !ok {
		t.Fatal("fixture did not resolve first question")
	}
	frame := client.event.Frame
	frame.Questions = nativePendingQuestions(u.attached.conversation)
	client.accept(sessionapi.NativeEvent{Attachment: "client", Frame: frame})
	if editor, _ := client.selectedEditor(); editor != second || editor.Text != "second remains editable" {
		t.Fatal("partial resolution replaced another local editor")
	}
	if history := client.editor("main").InputHistory; len(history) != 1 || history[0].Text != "first unsent draft" {
		t.Fatal("partial resolution did not restore the discarded ordinary draft")
	}
}

func TestNativeClientRetiredPromptPreservesUnadmittedDraft(t *testing.T) {
	for _, kind := range []string{"question", "approval"} {
		for _, phase := range []string{"editing", "queued", "preparing", "admitted"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				u, _ := newAppServerTestUI()
				u.startSession("main", "/workspace")
				if kind == "question" {
					u.Turn = "turn"
					questionTestAsync(t, u, "async", "Who receives it?")
				} else {
					approvalTestCommand(t, u, "approval", map[string]any{"availableDecisions": []string{"accept", "decline"}})
				}
				c := nativeTestPromptClient(u)
				if kind == "question" {
					nativeTestPromptKey(t, c, "\x1b[A")
				} else {
					nativeTestPromptKey(t, c, "\t")
				}
				editor, _ := c.selectedEditor()
				editor.InsertText("originating prompt draft")
				r := &nativeClientRuntime{active: c, owners: []*nativeClient{c}, editors: make(map[[2]string]*composer.Editor)}
				c.runtime = r
				var call *nativeClientCall
				if phase != "editing" {
					nativeTestPromptKey(t, c, "\r")
					if len(c.calls) != 1 {
						t.Fatalf("expected one submission, got %d", len(c.calls))
					}
					call = c.calls[0]
					if phase != "queued" {
						c.calls = nil
						call.owner.requests[nativeCallLane(call.operation)] = call
					}
				}
				frame := c.event.Frame
				frame.Questions, frame.Approvals = nil, nil
				c.accept(sessionapi.NativeEvent{Attachment: "client", Frame: frame})
				if call != nil {
					if history := c.editor("main").InputHistory; len(history) != 0 {
						t.Fatal("pending admission was labeled unsubmitted")
					}
					if phase == "queued" {
						c.finishPendingCalls()
					} else {
						call.complete, call.result.Version = true, frame.View.Version
						if phase != "admitted" {
							call.err = &sessionapi.NativeFailure{Status: http.StatusConflict, Reason: "question resolved elsewhere"}
						}
						r.settle()
					}
				}
				history := c.editor("main").InputHistory
				if phase == "admitted" {
					if len(history) != 0 {
						t.Fatal("admitted input was restored as unsent")
					}
				} else if len(history) != 1 || history[0].Text != "originating prompt draft" {
					t.Fatalf("retirement lost originating draft: %+v", history)
				}
			})
		}
	}
}

func TestUISnapshotNativeClientFamilyQuestions(t *testing.T) {
	u, wire := newAppServerTestUI()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	u.startSession("main", "/workspace")
	questionTestSync(t, u, "main-question", false)
	client := nativeTestPromptClient(u)
	frame := client.event.Frame
	for _, thread := range []string{"child-a", "child-b"} {
		frame.Questions = append(frame.Questions, prompt.CallView{ID: thread, Thread: thread, Sync: true, Questions: []prompt.QuestionView{{ID: thread + "/0", Header: thread, Text: "Which release scope for " + thread + "?", Pending: true, Choices: []prompt.Option{{Label: "Focused"}, {Label: "Full"}}}}})
	}
	client.prompts.update(&frame)
	client.refresh(now)
	nativeTestPromptKey(t, client, "\x1b[C")
	if client.prompts.questions.Active.ID != "child-a" {
		t.Fatal("next question stayed inside the main call")
	}
	nativeTestPromptKey(t, client, "\t")
	editor, _ := client.selectedEditor()
	editor.InsertText("Keep this child note")
	nativeTestPromptKey(t, client, "\x1b")
	nativeTestPromptKey(t, client, "\x1b[C")
	nativeTestPromptKey(t, client, "\x1b[D")
	if editor, _ := client.selectedEditor(); editor.Text != "Keep this child note" {
		t.Fatal("cross-agent navigation lost answer draft")
	}
	terminal := client.renderer.terminal
	painted := terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true, Version: mekugiVersion()}, 80, 18, 0)
	assertNativeUISnapshot(t, "native-client-family-questions", painted.Rows)
	if client.renderer.frame.View.Questions.Total != 3 || wire.Len() != 0 {
		t.Fatal("shared navigation changed host state or omitted questions")
	}
}

// Async replies may resume their asking child, but are not input to its waiting parent.
func TestNativeChildAsyncAnswersKeepParentWait(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(fmt.Sprint("idle=", idle), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, threads, _ := logicalWaitUI(t, nil)
				if idle {
					if err := threads.shared.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
						t.Fatal(err)
					}
				}
				u.notifications = new(nativeNotifications)
				u.Thread, u.Turn, u.LocalStop, u.Waiting = "child", "", "", ""
				u.startSession("child", u.attached.cwd)
				if !idle {
					u.Turn = "child-turn"
				}
				u.attached.threadInput = &threadInput{driver: threads.driver, caller: "/root/worker", admitted: true}
				questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": map[string]any{
					"id": "child-async", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Which scope?", "questions": []any{map[string]any{"title": "Which scope?", "options": []string{"Narrow", "Broad"}}},
				}})
				calls := nativePendingQuestions(u.attached.conversation)
				if len(calls) != 1 {
					t.Fatalf("child question not admitted: %+v", calls)
				}
				before, _ := threads.shared.Member("main")
				wire := u.Host.Input.(*appServerTestInput)
				wire.Reset()
				if refusal := nativeTestAnswers(t, u, nativeTestAnswerBatch(calls[0])); refusal != "" {
					t.Fatal(refusal)
				}
				for range 2 {
					synctest.Wait()
					if err := flushInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, &u.Editor, t.Context(), false, u.controlObservations, u.review, &u.pendingEditReceipts); err != nil {
						t.Fatal(err)
					}
				}
				method := "turn/steer"
				if idle {
					method = "turn/start"
				}
				requests := appServerTurnRequests(t, wire)
				if len(requests) != 1 || requests[0].Method != method || requests[0].Params.ThreadID != "child" {
					t.Fatalf("reply left its child or used the wrong operation: %+v", requests)
				}
				replies := binding.QuestionReplies(requests[0].text())
				if len(replies) != 1 || replies[0].Answer != "Narrow" {
					t.Fatalf("child answer payload = %+v", replies)
				}
				after, _ := threads.shared.Member("main")
				if before.Generation != after.Generation || before.Status != after.Status || !reflect.DeepEqual(before.Wait, after.Wait) {
					t.Fatalf("child answer changed parent wait: before=%+v after=%+v", before, after)
				}
			})
		})
	}
}
