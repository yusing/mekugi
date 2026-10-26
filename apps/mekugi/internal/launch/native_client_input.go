package launch

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

type nativeSelectionCapture struct {
	selection *terminalsession.SourceSelection
	input     []terminalsession.Input
	thread    sessionpresentation.Thread
	displayed sessionpresentation.Thread
	workspace string
	target    conversation.EditorTarget
	ready     bool
}

// Queued editor input owns the destination visible when it arrived, including
// keys waiting behind an asynchronous menu continuation.
type nativeClientInput struct {
	event             terminalsession.Input
	thread, displayed sessionpresentation.Thread
	target            conversation.EditorTarget
}

func (c *nativeClient) queueInput(event terminalsession.Input) {
	_, target := c.selectedEditor()
	c.input = append(c.input, nativeClientInput{event: event, thread: c.event.Frame.View.Input, displayed: c.event.Frame.View.Displayed, target: target})
}

// Dependent local work retains its input destination while observing the latest
// semantic publication of that owner. This temporary renderer projection never
// changes the server's selected destination or paints an inactive interaction.
func (c *nativeClient) withInput(thread, displayed sessionpresentation.Thread, target conversation.EditorTarget, apply func()) {
	input, view, prompts := c.event.Frame.View.Input, c.event.Frame.View.Displayed, c.prompts
	c.prompts = nativeClientPrompts{attachment: prompts.attachment}
	switch {
	case target.Call != "":
		for _, call := range prompts.questions.Calls {
			if call.ID != target.Call || call.Resolved || call.Sent {
				continue
			}
			for i, question := range call.Questions {
				if question.ID == target.Question && question.Outcome == "" {
					c.prompts.questions = prompts.questions
					c.prompts.questions.Active, c.prompts.questions.Index = call, i
				}
			}
		}
		if c.prompts.questions.Active == nil {
			c.prompts = prompts
			c.notice = "The original question has already been resolved"
			return
		}
	case target.Request != "":
		for _, approval := range prompts.approvals.Pending {
			if approval.ID == target.Request {
				c.prompts.approvals = prompts.approvals
				c.prompts.approvals.Pending = slices.Clone(prompts.approvals.Pending)
				index := slices.Index(c.prompts.approvals.Pending, approval)
				c.prompts.approvals.Pending[0], c.prompts.approvals.Pending[index] = approval, c.prompts.approvals.Pending[0]
				c.prompts.approvals.Open = true
			}
		}
		if !c.prompts.approvals.Open {
			c.prompts = prompts
			c.notice = "The original approval has already been resolved"
			return
		}
	}
	if thread.ID == input.ID {
		thread = input
	}
	c.event.Frame.View.Input, c.event.Frame.View.Displayed = thread, displayed
	c.refreshView(time.Now())
	apply()
	c.event.Frame.View.Input, c.event.Frame.View.Displayed = input, view
	c.prompts = prompts
	c.refreshView(time.Now())
}

// A menu/panel continuation owns the key that opened it. Only subsequent editor
// input waits for that semantic result; terminal navigation and cancellation do
// not acquire the editor and remain independent.
func (c *nativeClient) drainInput() {
	if c.event.Attachment == "" {
		return
	}
	for i := 0; i < len(c.input); {
		pending := c.request(nativeSemanticCall)
		dependent := pending != nil && pending.next != nil || slices.ContainsFunc(c.calls, func(call *nativeClientCall) bool { return call.next != nil })
		queued := c.input[i]
		event := queued.event
		independent := event.Kind == terminalsession.ColorInput || event.Kind == terminalsession.FocusInput || event.Kind == terminalsession.MouseInput || event.Kind == terminalsession.KeyInput && (event.Text == "\x03" || event.Text == "\x02" || strings.HasPrefix(event.Text, "\x1b["))
		if dependent && !independent {
			i++
			continue
		}
		c.input = slices.Delete(c.input, i, i+1)
		_, target := c.selectedEditor()
		if !independent && (queued.thread.ID != c.event.Frame.View.Input.ID || queued.target.Call != target.Call || queued.target.Question != target.Question || queued.target.Request != target.Request) {
			c.withInput(queued.thread, queued.displayed, queued.target, func() { c.dispatch(event) })
		} else {
			c.dispatch(event)
		}
	}
}

func (c *nativeClient) dispatch(event terminalsession.Input) {
	c.dirty = true
	if event.Kind == terminalsession.PasteStart && c.runtime != nil {
		c.runtime.pasteOwner = c
	}
	if event.Kind == terminalsession.PasteEnd {
		defer func() {
			c.pasteEditor = nil
			if c.runtime != nil && c.runtime.pasteOwner == c {
				c.runtime.pasteOwner = nil
			}
		}()
	}
	if pending := c.selection; pending != nil && event.Kind != terminalsession.ColorInput && event.Kind != terminalsession.FocusInput {
		if event.Kind == terminalsession.KeyInput && event.Text == "\x1b" {
			c.selection = nil
			return
		}
		mouse, press := terminalsession.ParseMouse(event.Text)
		gesture := event.Kind == terminalsession.MouseInput && press && (mouse.Release || mouse.Button == 32)
		reference := event.Kind == terminalsession.KeyInput && slices.Contains([]string{"r", "R", "c", "C"}, event.Text)
		if gesture || reference {
			pending.input = append(pending.input, event)
			return
		}
		// Only continuation of the frozen selection depends on hydration.
		// Editing, navigation and interruption keep their normal input path.
		c.selection = nil
	}
	if c.restoration != nil && c.restoration.complete && c.restoration.err != nil {
		c.restoration = nil
		c.applyEffects()
	}
	u := c.renderer.terminal
	for effect := range u.FrameInput(time.Now(), mekugiVersion()).Dispatch(&u.Interaction, event) {
		switch {
		case effect.Focused != nil:
			c.notifications.focused = *effect.Focused
			c.enqueue(sessionapi.NativeOperation{Focused: effect.Focused}, nil, nil)
		case effect.Main != nil:
			c.continueKey(*effect.Main)
		case effect.Paste != nil:
			if effect.Paste.Kind == terminalsession.PasteStart {
				u.Main.CancelPaste()
				c.pasteEditor = nil
				if !c.prepareComposerInput() {
					break
				}
				c.pasteEditor, c.pasteTarget = c.selectedEditor()
				c.pasteSecret = c.secretKey != ""
			}
			pasted, err := u.Main.PasteInput(*effect.Paste, u.Prompts)
			if err != nil {
				c.notice = err.Error()
				break
			}
			if pasted != nil {
				editor, target := c.pasteEditor, c.pasteTarget
				retained := editor != nil && c.retainsEditor(editor)
				if c.pasteSecret {
					current, currentTarget := c.selectedEditor()
					retained = current == editor && currentTarget == target
				}
				if !retained || pasted.Intent.Target != target {
					if !c.pasteSecret {
						c.editor("main").Remember(composer.Draft{Text: pasted.Text})
						c.notice = "The original prompt resolved · pasted text saved in input history (↑)"
					}
					break
				}
				for _, call := range c.prompts.questions.Calls {
					if call.ID == target.Call {
						for i := range call.Questions {
							if question := &call.Questions[i]; question.ID == target.Question {
								question.Edit(!question.Note)
							}
						}
					}
				}
				if path, ok := pastedImagePath(pasted.Text); ok {
					editor.InsertImage(path)
					editor.InsertText(" ")
				} else {
					editor.InsertText(pasted.Text)
				}
				editor.Run = composer.NoRun
			}
		case effect.Panel != nil:
			c.enqueue(sessionapi.NativeOperation{Panel: effect.Panel}, nil, nil)
		case effect.Resume != nil:
			c.enqueue(sessionapi.NativeOperation{Resume: effect.Resume}, nil, nil)
		case effect.Menu != nil:
			if c.menu(*effect.Menu, nil) {
				return
			}
		case effect.Display != nil:
			thread := effect.Display.Thread
			if c.displayParent != nil && thread.ID == c.event.Frame.View.Main.ID {
				thread = c.displayParent.event.Frame.View.Main
			}
			c.displayThread(thread)
		case effect.Roster != nil:
			if agents := c.renderer.frame.Agents; agents != nil {
				for _, row := range agents.Roster {
					if row.Agent.Name == effect.Roster.Caller {
						c.displayThread(sessionpresentation.Thread{ID: row.Agent.Thread, Caller: effect.Roster.Caller})
						break
					}
				}
			}
		case effect.Interrupt != nil:
			c.enqueue(sessionapi.NativeOperation{Interrupt: effect.Interrupt}, nil, nil)
		case effect.Side != nil:
			c.enqueue(sessionapi.NativeOperation{Side: effect.Side}, nil, nil)
		case effect.BTW != nil:
			c.enqueue(sessionapi.NativeOperation{BTW: effect.BTW}, nil, nil)
		case effect.Notice != nil:
			if c.feedback.Text != "" {
				if effect.Notice.Dismiss {
					c.feedback.Set("", false, time.Now())
				} else {
					c.openFeedbackDetails(effect.Notice.Text)
				}
				break
			}
			c.enqueue(sessionapi.NativeOperation{Notice: &sessionapi.NativeNoticeAction{Version: effect.Notice.Version, Feedback: effect.Notice.Feedback, Dismiss: effect.Notice.Dismiss, Text: effect.Notice.Text}}, nil, nil)
		case effect.Details != nil:
			kind := sessionapi.NativeErrorDetails
			if effect.Details.Kind == terminalsession.SkillDetails {
				kind = sessionapi.NativeSkillDetails
			}
			c.details(effect.Details.Version, sessionapi.NativeDetailRequest{Kind: kind, Agent: effect.Details.View == u.Agents, Caller: effect.Details.Caller})
		case effect.Dialog != nil:
			c.dialog(*effect.Dialog)
		case effect.Selection != nil:
			selection := effect.Selection
			if len(selection.Demands) > 0 {
				if selection.Selection.View != u.Transcript && selection.Selection.View != u.Agents {
					c.notice = sessionpresentation.ErrStaleIntent.Error()
					return
				}
				_, target := c.selectedEditor()
				pending := &nativeSelectionCapture{workspace: c.event.Frame.View.Info.Workspace, selection: selection.Selection, thread: c.event.Frame.View.Input, displayed: c.event.Frame.View.Displayed, target: target}
				c.selection = pending
				c.enqueue(sessionapi.NativeOperation{Output: &sessionapi.NativeOutputDemand{Agent: selection.Selection.View == u.Agents, Demands: selection.Demands}}, nil, func(err error) {
					if c.selection != pending {
						return
					}
					c.selection = nil
					s := pending.selection
					view := c.event.Frame.View
					if err != nil {
						return
					}
					if view.Version.Attachment != s.Attachment || view.Info.Workspace != pending.workspace {
						c.notice = sessionpresentation.ErrStaleIntent.Error()
						return
					}
					if !pending.ready {
						c.notice = "Selected output could not be loaded; select the text again"
						return
					}
					previous := u.Selection
					c.withInput(pending.thread, pending.displayed, pending.target, func() {
						u.Selection = s
						for _, event := range pending.input {
							c.dispatch(event)
							if c.quit || c.handoff != nil {
								break
							}
						}
					})
					if view.Displayed.ID != s.Thread {
						u.Selection = previous
					}
				})
				return
			} else if target := selection.Target; target.Link != "" || target.Entry != 0 || target.Snippet != (activityui.Snippet{}) {
				if target.View != u.Transcript && target.View != u.Agents || target.Workspace != c.event.Frame.View.SourceWorkspace() {
					c.notice = sessionpresentation.ErrStaleIntent.Error()
					break
				}
				if target.Link != "" {
					if link, err := url.Parse(target.Link); err == nil && (link.Scheme == "http" || link.Scheme == "https") {
						c.notice, _ = u.Frame.CopyText(target.Link)
					} else {
						c.details(target.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: target.Link, Displayed: sessionpresentation.Thread{ID: target.Thread}})
					}
				} else {
					request := sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: target.Entry, Agent: target.View == u.Agents, Displayed: sessionpresentation.Thread{ID: target.Thread}}
					if target.Entry == 0 {
						request.Source, request.Path = target.Snippet.Run, target.Snippet.Path
						if block, ok := target.View.SnippetBlock(target.Snippet); ok {
							request.Source, request.Output = block.Source, block.Output
							for _, member := range block.Members {
								if !slices.Contains(request.Sources, member.Source) {
									request.Sources = append(request.Sources, member.Source)
								}
							}
						}
					}
					c.details(target.Version, request)
				}
			}
		case effect.Reference != nil:
			c.enqueue(sessionapi.NativeOperation{Reference: effect.Reference}, nil, nil)
		case effect.Clipboard != nil:
			c.notice = effect.Clipboard.Text
		case effect.Locked:
			c.notice = "Locked · /unlock restores Esc and Ctrl-C"
		case effect.EditorPath != "":
			c.handoff = &nativeEditorHandoff{path: effect.EditorPath}
		case effect.Prompt != nil:
			if c.prompt(*effect.Prompt, nil) {
				c.refresh(time.Now())
				return
			}
		case effect.PendingPrompt != nil:
			c.openPrompts()
		}
		c.refresh(time.Now())
	}
}

func (c *nativeClient) details(version sessionpresentation.Version, request sessionapi.NativeDetailRequest) {
	target := c.event.Target()
	target.Version = version
	if request.Displayed.ID == "" {
		request.Displayed = c.event.Frame.View.Displayed
	}
	c.calls = append(c.calls, &nativeClientCall{owner: c, target: target, operation: sessionapi.NativeOperation{Details: &request}})
}

func (c *nativeClient) continueKey(next terminalsession.KeyContinuation) {
	if !c.prepareComposerInput() {
		return
	}
	u := c.renderer.terminal
	for {
		transcript := u.Transcript
		if c.event.Frame.View.Displayed.ID != c.event.Frame.View.Main.ID {
			transcript = u.Agents
		}
		input := u.Main.ContinueKey(next, u.Prompts, transcript, u.Focus == 0)
		switch {
		case input.Prompt != nil:
			if c.prompt(*input.Prompt, input.Next) {
				return
			}
		case input.Panel != nil:
			if input.ClearSelection {
				u.Selection = nil
			}
			c.enqueue(sessionapi.NativeOperation{Panel: input.Panel}, input.Next, nil)
			return
		case input.Menu != nil:
			if c.menu(*input.Menu, input.Next) {
				return
			}
		case input.Composer != nil:
			c.composer(*input.Composer)
			return
		}
		if input.Next == nil {
			return
		}
		next = *input.Next
		c.refreshView(time.Now())
	}
}

func (c *nativeClient) menu(menu terminalsession.MenuResult, next *terminalsession.KeyContinuation) bool {
	if menu.Action.Kind != sessionpresentation.MenuNoAction {
		if next == nil {
			next = menu.Continuation()
		}
		if menu.Handled && !menu.Submit {
			next = nil
		}
		c.enqueue(sessionapi.NativeOperation{Menu: &menu.Action}, next, nil)
		return true
	}
	return menu.Handled
}

func (c *nativeClient) composer(input terminalsession.ComposerInput) {
	editor, target := c.selectedEditor()
	if input.Err != nil {
		c.notice = input.Err.Error()
		return
	}
	if input.Editor.Action.Kind != composer.NoAction {
		if input.Editor.Target != target {
			c.notice = "The editor changed; try the edit again"
			return
		}
		if _, err := editor.Apply(input.Editor.Action); err != nil {
			c.notice = err.Error()
		} else {
			c.notice = ""
			c.feedback.Set("", false, time.Now())
		}
		c.refresh(time.Now())
		return
	}
	intent := input.Command
	switch intent.Operation {
	case sessionpresentation.NoComposerOperation:
		return
	case sessionpresentation.AcceptPrediction:
		if text := c.renderer.frame.View.Draft.Prediction; text != "" {
			editor.InsertText(text)
		}
		c.refresh(time.Now())
		return
	case sessionpresentation.CancelComposer:
		if editor.Text != "" {
			_, _ = editor.Apply(composer.Action{Kind: composer.DeleteAction, End: len(editor.Text)})
			c.notice = "Draft cleared · Ctrl+Z restores"
			c.refresh(time.Now())
			return
		}
		view := c.event.Frame.View
		switch {
		case view.BTW != nil:
			c.enqueue(sessionapi.NativeOperation{BTW: &sessionpresentation.CloseSideIntent{Version: view.Version, Key: view.BTW.Key, Exchange: view.BTW.ID}}, nil, nil)
		case view.Side != nil && view.Input == view.SideThread:
			c.enqueue(sessionapi.NativeOperation{Side: &sessionpresentation.CloseSideIntent{Version: view.Version, Key: view.Side.Key, Exchange: view.Side.ID}}, nil, nil)
		case c.renderer.terminal.Main.InterruptLocked:
			c.notice = "Locked · /unlock restores Esc and Ctrl-C"
		case view.InterruptAvailable:
			c.enqueue(sessionapi.NativeOperation{Interrupt: &sessionpresentation.InterruptIntent{Version: view.Version, Target: view.Interrupt, Mode: sessioncontrol.RestoreWaiting}}, nil, nil)
		default:
			c.quit = true
		}
		return
	case sessionpresentation.SubmitComposer, sessionpresentation.QueueComposer:
		if strings.TrimSpace(editor.Text) == "/quit" {
			editor.Take()
			c.quit = true
			return
		}
		if !c.inputAvailable() {
			c.notice = "Previous input is still settling · draft kept in the editor"
			c.refresh(time.Now())
			return
		}
	case sessionpresentation.EditExternally:
		if c.secretKey != "" {
			c.notice = "External editor is unavailable for secret answers"
			return
		}
		c.handoff = &nativeEditorHandoff{editor: editor, target: target, attachment: c.event.Frame.View.Version.Attachment}
		return
	case sessionpresentation.AttachClipboard:
		c.clipboard()
		return
	}
	value := editor.Draft.Clone()
	if c.secretKey != "" {
		value = composer.Draft{}
	}
	var after func(error)
	if intent.Operation == sessionpresentation.SubmitComposer || intent.Operation == sessionpresentation.QueueComposer {
		editor.Take()
		after = func(err error) {
			if err == nil {
				return
			}
			if nativeAdmissionRefused(err) {
				editor.Restore(value)
				return
			}
			c.unknown = append(c.unknown, nativeUnknownInput{Draft: value, Thread: intent.Thread})
		}
	}
	c.enqueue(sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: intent, Draft: value, Consumed: after != nil}}, nil, after)
	c.refresh(time.Now())
}

func (c *nativeClient) dialog(intent terminalsession.DialogIntent) {
	u := c.renderer.terminal
	if u.Dialog == nil {
		return
	}
	switch intent.Action {
	case terminalsession.DialogClose:
		if c.closeLocalDetails() {
			c.refresh(time.Now())
			return
		}
		id := u.Dialog.ID
		c.enqueue(sessionapi.NativeOperation{CloseDetails: &id}, nil, nil)
	case terminalsession.DialogCopy:
		c.notice, _ = u.Frame.CopyText(intent.Text)
	case terminalsession.DialogOpenLink:
		var source sessionpresentation.Thread
		if details := c.event.Frame.Details; details != nil && details.View.ID == u.Dialog.ID {
			source = details.Source
		}
		c.closeLocalDetails()
		c.details(c.event.Frame.View.Version, sessionapi.NativeDetailRequest{Displayed: source, Kind: sessionapi.NativeFileDetails, Path: intent.Text})
	case terminalsession.DialogReference:
		c.closeLocalDetails()
		intent := u.Main.Reference("Selected output", intent.Text, "")
		c.enqueue(sessionapi.NativeOperation{Reference: &intent}, nil, nil)
	}
}

// Only an explicit pre-admission refusal makes a submitted payload retryable.
func nativeAdmissionRefused(err error) bool {
	if refusal, ok := errors.AsType[*sessionapi.NativeFailure](err); ok {
		switch refusal.Status {
		case http.StatusBadRequest, http.StatusConflict, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusRequestEntityTooLarge:
			return true
		}
	}
	return false
}
