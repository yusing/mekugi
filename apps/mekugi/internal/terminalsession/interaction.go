package terminalsession

import (
	"iter"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Interaction owns the input stream and its frozen, successfully delivered source.
type Interaction struct {
	Decoder     Decoder
	Frame       Frame
	Selection   *SourceSelection
	Prefix      bool
	resizeUntil time.Time
}

type PendingPromptInput struct {
	Version        sessionpresentation.Version
	ApprovalsFirst bool
}

type DetailKind uint8

const (
	ErrorDetails DetailKind = iota + 1
	SkillDetails
)

type DetailInput struct {
	Version sessionpresentation.Version
	View    *activityui.View
	Kind    DetailKind
	Caller  string
}

type NoticeInput struct {
	Version  sessionpresentation.Version
	Feedback uint64
	Dismiss  bool
	Text     string
}

type ClipboardFeedback struct {
	Text  string
	Alert bool
}

// InputEffect is one application consequence of terminal input. The consumer
// applies it and publishes before requesting the next effect. Stopping iteration
// consumes the input; no continuation or effect queue survives the dispatch.
type InputEffect struct {
	Paste         *Input
	Main          *KeyContinuation
	Prompt        *PromptInput
	PendingPrompt *PendingPromptInput
	Panel         *sessionpresentation.PanelAction
	Resume        *sessionpresentation.ResumeAction
	Menu          *MenuResult
	Side          *sessionpresentation.CloseSideIntent
	BTW           *sessionpresentation.CloseSideIntent
	Dialog        *DialogIntent
	Reference     *sessionpresentation.ReferenceIntent
	Selection     *SelectionInput
	History       *sessionpresentation.HistoryIntent
	CancelHistory bool
	Display       *sessionpresentation.DisplayIntent
	Roster        *activityui.DisplayIntent
	Details       *DetailInput
	Notice        *NoticeInput
	Clipboard     *ClipboardFeedback
	Interrupt     *sessionpresentation.InterruptIntent
	Locked        bool
	EditorPath    string
	Focused       *bool
}

// Dispatch preserves the decoder's event boundaries before interpreting panes.
// Source: apps/mekugi/internal/router/terminal_ui.go:39:136@d16ed250.
func (in FrameInput) Dispatch(state *Interaction, event Input) iter.Seq[InputEffect] {
	return func(yield func(InputEffect) bool) {
		if in.Dialog != nil || in.Main != nil && in.Main.Menu.Dialog() {
			state.Prefix = false
			state.resizeUntil = time.Time{}
		}
		if !in.Now.Before(state.resizeUntil) {
			state.resizeUntil = time.Time{}
		}
		switch event.Kind {
		case NoInput:
			return
		case PasteStart, PasteInput, PasteEnd:
			state.resizeUntil = time.Time{}
			if event.Kind == PasteStart {
				state.Prefix = false
			}
			if in.Main == nil {
				return
			}
			if in.Dialog != nil || in.Main.Menu.Dialog() || in.Shell.Focus != 0 || in.Main.Panel.Open() || in.Main.Resume.Open() || in.Main.session.ReadOnly != sessionpresentation.Editable {
				in.Main.CancelPaste()
				return
			}
			if event.Kind == PasteStart && state.Selection != nil && !state.Selection.Dragging() {
				state.Selection = nil
			}
			yield(InputEffect{Paste: &event})
			return
		case ColorInput:
			in.TerminalColor(event.Text)
			return
		case FocusInput:
			focused := event.Text == "\x1b[I"
			if !focused {
				state.resizeUntil = time.Time{}
				in.ClearSkillsHover()
			}
			yield(InputEffect{Focused: &focused})
			return
		case MouseInput:
			state.resizeUntil = time.Time{}
			if in.Main != nil && in.Main.Keybindings {
				in.Main.Keybindings = false
				return
			}
			if pointer, ok := ParseMouse(event.Text); ok {
				in.mouse(state, pointer, yield)
			}
			return
		}
		key := event.Text
		if in.Main != nil && in.Main.Menu.Dialog() {
			in.send(state, key, yield)
			return
		}
		if (key == "\x1b[D" || key == "\x1b[C") && (state.Prefix || !state.resizeUntil.IsZero()) {
			state.Prefix = false
			state.resizeUntil = in.Now.Add(500 * time.Millisecond)
			in.Resize(key)
			return
		}
		state.resizeUntil = time.Time{}
		if strings.HasPrefix(key, "\x1b") {
			if state.Prefix {
				state.Prefix = false
				in.Resize(key)
				return
			}
			in.send(state, key, yield)
			return
		}
		if in.Dialog == nil && in.selectionKey(state, key, yield) {
			return
		}
		if in.Dialog != nil {
			in.send(state, key, yield)
			return
		}
		if state.Prefix {
			state.Prefix = false
			switch key {
			case "\x02":
				in.send(state, key, yield)
			case "1", "2":
				in.selectPane(state, int(key[0]-'1'), yield)
			case "3":
				in.selectPane(state, 3, yield)
			case "o":
				v := in.Main.session
				yield(InputEffect{History: &sessionpresentation.HistoryIntent{Version: v.Version, Thread: v.History.Target}})
			case "q":
				if in.Main != nil {
					if in.Main.session.ReadOnly != sessionpresentation.Editable && !in.selectPane(state, 0, yield) {
						return
					}
					yield(InputEffect{PendingPrompt: &PendingPromptInput{Version: in.Main.session.Version, ApprovalsFirst: true}})
				}
			case "!":
				if in.Main != nil && in.Shell.Focus == 0 {
					yield(InputEffect{Details: &DetailInput{Version: in.Main.session.Version, View: in.displayedTranscript(), Kind: ErrorDetails}})
				}
			case "e":
				in.Live.NextBatch(in.Main.session.Displayed.Caller)
			case "r":
				in.Live.ScrollBatch(in.Main.session.Displayed.Caller, 'r')
			default:
				in.Resize(key)
			}
			return
		}
		if key == "\x02" {
			if in.Main != nil {
				in.Main.Keybindings = false
			}
			state.Prefix = true
			return
		}
		in.send(state, key, yield)
	}
}

// Send routes a complete key, including a decoder-expired lone Escape.
func (in FrameInput) Send(state *Interaction, key string) iter.Seq[InputEffect] {
	return func(yield func(InputEffect) bool) {
		state.resizeUntil = time.Time{}
		in.send(state, key, yield)
	}
}

func (in FrameInput) SelectPane(state *Interaction, pane int) iter.Seq[InputEffect] {
	return func(yield func(InputEffect) bool) { in.selectPane(state, pane, yield) }
}

// Source: apps/mekugi/internal/router/terminal_ui.go:138:267@d16ed250.
func (in FrameInput) send(state *Interaction, key string, yield func(InputEffect) bool) {
	if in.Dialog != nil {
		intent := in.Dialog.Key(key)
		yield(InputEffect{Dialog: &intent})
		return
	}
	if in.Main != nil && in.Main.Menu.Dialog() && in.Main.session.ReadOnly == sessionpresentation.Editable {
		result := in.Main.Menu.Key(key)
		yield(InputEffect{Menu: &result})
		return
	}
	if key != "\x1b" && in.selectionKey(state, key, yield) {
		return
	}
	main, shell := in.Main, in.Shell
	if key == "\x1b" && main != nil && main.session.Loading != "" {
		v := main.session
		yield(InputEffect{History: &sessionpresentation.HistoryIntent{Version: v.Version, Thread: v.Loading}, CancelHistory: true})
		return
	}
	if main != nil && shell.Focus == 0 && main.session.ReadOnly != sessionpresentation.Editable {
		transcript := in.displayedTranscript()
		switch key {
		case "\x05":
			transcript.ToggleExpansion()
		case "\x1b[5~":
			transcript.ScrollKey('b')
		case "\x1b[6~":
			transcript.ScrollKey(' ')
		case "\x1b":
			if !in.selectionKey(state, key, yield) {
				transcript.Follow()
			}
		}
		return
	}
	if main != nil && shell.Focus == 0 && main.Panel.Open() {
		if state.Selection != nil && !state.Selection.Dragging() {
			state.Selection = nil
			if key == "\x1b" {
				return
			}
		}
		action, clear := main.Panel.Key(key)
		if clear {
			state.Selection = nil
		}
		yield(InputEffect{Panel: &action})
		return
	}
	if main != nil && shell.Focus == 0 && main.Resume.Open() {
		action := main.Resume.Key(key)
		yield(InputEffect{Resume: &action})
		return
	}
	if key == "\x1b" && main != nil && shell.Focus == 0 {
		if in.Prompts != nil && in.Prompts.Ready() {
			if input := main.PromptKey(key, in.Prompts); input != nil && !yield(InputEffect{Prompt: input}) {
				return
			}
		}
		if main.Keybindings {
			main.Keybindings = false
			return
		}
		result := main.Menu.Key(key)
		if !yield(InputEffect{Menu: &result}) {
			return
		}
	}
	if main != nil && shell.Focus == 0 {
		if key == "\x1b" && in.selectionKey(state, key, yield) {
			return
		}
		if !main.Menu.Open() {
			if handled, close := main.BTW.Key(key); handled {
				if close != nil {
					yield(InputEffect{BTW: close})
				}
				return
			}
		}
		if key == "\x1b" && main.session.Side != nil {
			yield(InputEffect{Side: &sessionpresentation.CloseSideIntent{Version: main.session.Version, Key: main.session.Side.Key, Exchange: main.session.Side.ID}})
			return
		}
	}
	if key == "\x1b" && main != nil && !main.Keybindings && shell.Focus == 0 {
		if transcript := in.displayedTranscript(); transcript != nil && !transcript.Following {
			transcript.Follow()
			state.Selection = nil
			return
		}
	}
	if state.Selection != nil && !state.Selection.Dragging() {
		if key == "\x1b" && in.selectionKey(state, key, yield) {
			return
		}
		state.Selection = nil
	}
	if key == "\x1b" && shell.Focus == 3 {
		shell.Focus = 0
		return
	}
	if key == "\x1b" && shell.Focus == 0 && main != nil {
		v := main.session
		if v.InterruptAvailable {
			if main.InterruptLocked {
				yield(InputEffect{Locked: true})
			} else {
				yield(InputEffect{Interrupt: &sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt, Mode: sessioncontrol.ExpediteSteers}})
			}
		}
		return
	}
	shell.Clamp()
	switch shell.Focus {
	case 0:
		yield(InputEffect{Main: &KeyContinuation{key: key}})
	case 1:
		if editor, ok := shell.Auxiliary.(terminalui.PaneEditor); ok && key == "e" && main != nil {
			if path := editor.EditorPath(); path != "" {
				yield(InputEffect{EditorPath: path})
				return
			}
		}
		if shell.Auxiliary.Key(key) {
			shell.DiffOpen, shell.Focus = false, 0
		}
	default:
		child, selected := main.session.Displayed.ID != main.session.Main.ID, in.Agents.Selected
		quit := in.Agents.HandleRosterKey(key)
		if !in.rosterPick(state, child, selected, yield) {
			return
		}
		if quit {
			shell.Focus = 0
		}
	}
}

func (in FrameInput) selectPane(state *Interaction, pane int, yield func(InputEffect) bool) bool {
	state.resizeUntil = time.Time{}
	if pane == 0 && in.Main != nil {
		v := in.Main.session
		if !yield(InputEffect{Display: &sessionpresentation.DisplayIntent{Version: v.Version, Thread: v.Main}}) {
			return false
		}
	}
	in.Shell.Select(pane)
	state.Selection = nil
	return true
}

func (in FrameInput) rosterPick(state *Interaction, child bool, selected string, yield func(InputEffect) bool) bool {
	if intent := in.Agents.DisplayRequest; intent != nil {
		in.Agents.DisplayRequest = nil
		if !yield(InputEffect{Roster: intent}) {
			return false
		}
	}
	if (in.Main.session.Displayed.ID != in.Main.session.Main.ID) != child || in.Agents.Selected != selected {
		state.Selection = nil
		in.Agents.Runs = nil
	}
	return true
}

func (in FrameInput) selectionKey(state *Interaction, key string, yield func(InputEffect) bool) bool {
	if state.Selection == nil {
		return false
	}
	action, handled := state.Selection.Key(key, false)
	if action != SelectionScroll {
		in.selectionAction(state, action, yield)
	}
	return handled
}

func (in FrameInput) selectionAction(state *Interaction, action SelectionIntent, yield func(InputEffect) bool) {
	s := state.Selection
	if s == nil || s.Dragging() && action != 27 {
		return
	}
	switch action {
	case 'r':
		description, source := s.MentionDescription()
		intent := in.Main.Reference(description, s.Text(), source)
		if !yield(InputEffect{Reference: &intent}) {
			return
		}
		in.Shell.Focus = 0
		if !yield(InputEffect{Dialog: &DialogIntent{Action: DialogClose}}) {
			return
		}
	case 'c':
		notice, alert := state.Frame.CopyText(s.Text())
		if !yield(InputEffect{Clipboard: &ClipboardFeedback{Text: notice, Alert: alert}}) {
			return
		}
	case 27:
	default:
		return
	}
	state.Selection = nil
}

func (in FrameInput) selectionMouse(state *Interaction, pointer Mouse, yield func(InputEffect) bool) bool {
	if in.Main == nil {
		return false
	}
	for {
		result := state.Frame.SelectionMouse(in, state.Selection, pointer)
		if len(result.Demands) > 0 {
			if !yield(InputEffect{Selection: &result}) {
				return true
			}
			continue
		}
		state.Selection = result.Selection
		if result.Action != 0 {
			in.selectionAction(state, result.Action, yield)
		}
		if result.Target.Link != "" || result.Target.Entry != 0 || result.Target.Snippet != (activityui.Snippet{}) {
			if !yield(InputEffect{Selection: &result}) {
				return true
			}
		}
		return result.Handled
	}
}
