package terminalsession

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Update delivers a detached publication before subsequent input or painting.
func (m *Main) Update(view sessionpresentation.View) {
	m.session = view
	m.Panel.Update(view)
	m.Resume.Update(view)
	m.Menu.Update(view)
	m.BTW.Update(view)
}

type MainOptions struct {
	Now     time.Time
	Focused bool
	Version string
}

// Render paints the attached views without resolving source output. The application
// hydrates returned demands and renders again before delivering the frame.
// Source: apps/mekugi/internal/router/app_server_ui.go:1131:1208@ceef0946.
func (m *Main) Render(transcript *activityui.View, painter *activityui.Painter, prompts *Prompts, options MainOptions, width, height, dock int) (out MainFrame) {
	presentation := m.session
	readOnly := presentation.ReadOnly != sessionpresentation.Editable
	defer func() {
		out.Notices = NoticeReceipt{Attachment: presentation.Version.Attachment, Feedback: presentation.Status.FeedbackRevision}
		if !readOnly {
			out.Notices.Composer = m.NoticeDismiss.W > 0 && presentation.Status.Feedback.Alert
			out.Notices.Content = m.ContentPainted
		}
	}()

	picker := presentation.ReadOnly == sessionpresentation.Editable && m.Menu.Open() && options.Focused
	width, height = max(1, width), max(1, height)
	m.Menu.Rect, m.PromptRect = Rect{}, Rect{}
	m.NoticeDetails, m.NoticeDismiss = Rect{}, Rect{}
	prompts.Rect = Rect{}
	if !readOnly && presentation.Panel != nil {
		m.ContentPainted = false
		m.ComposerRect = Rect{}
		return MainFrame{Rows: m.Panel.Render(&painter.Painter, width, height)}
	}
	if !readOnly && presentation.Resume != nil {
		m.ContentPainted = false
		m.ComposerRect = Rect{}
		return MainFrame{Rows: m.Resume.Render(&painter.Painter, width, height, SelectionStyle(&painter.Painter))}
	}
	if picker && m.Menu.Modal() {
		m.ContentPainted = false
		m.ComposerRect = Rect{}
		return MainFrame{Rows: m.Menu.ModalFrame(transcript, options.Now, width, height), Demands: transcript.OutputDemands()}
	}
	in := mainInput{
		Session: presentation, Draft: presentation.Draft.Draft, Now: options.Now, ReadOnly: readOnly,
		Focused: options.Focused, Empty: transcript.Empty(),
		Picker: picker, Welcome: welcome(options.Version, presentation.Info.BackendVersion), Selection: SelectionStyle(&painter.Painter), TurnIdle: presentation.Interrupt.Turn == "",
	}
	if call := presentation.Questions.Call; call != nil {
		in.Question, in.Secret, in.QuestionLayout = true, call.Questions[presentation.Questions.Index].Secret, prompts.QuestionLayout(presentation.Questions)
		in.Draft = presentation.PromptDraft.Draft
		if in.Secret {
			in.Draft = prompts.Secret.Draft
		}
	}
	if presentation.Approvals.Count > 0 {
		in.ApprovalLayout = prompts.ApprovalLayout(presentation.Approvals.Approval.ID)
		in.ApprovalAgent = presentation.Info.ApprovalCaller
		if presentation.Approvals.Open {
			in.Draft = presentation.PromptDraft.Draft
		}
	}

	frame := m.layout(in, painter, width, height, dock)
	transcript.Conversation, transcript.FeedOnly, transcript.StatusLabel = true, true, textsanitize.Safe(StatusText(presentation.Status.State), false)
	transcript.FeedRows = 0
	transcript.FeedQuestions, transcript.FeedSnippets = nil, nil
	if room := frame.Feed.H; room > 0 {
		rows := transcript.Render(width, room, in.Now)
		for row := range rows {
			rows[row] = terminalui.AttachCopy(rows[row], transcript.CopyRows[row])
		}
		if !readOnly && m.Keybindings && in.Focused {
			m.ContentPainted = false
			rows = Keybindings(width, room)
		}
		if in.Picker {
			m.ContentPainted = false
			popupHeight := min(room, m.Menu.Height(width))
			popup := m.Menu.Render(&painter.Painter, SelectionStyle(&painter.Painter), width, popupHeight)
			m.Menu.Rect = Rect{0, frame.Feed.Y + len(rows) - popupHeight, width, popupHeight}
			copy(rows[len(rows)-popupHeight:], popup)
		}
		copy(frame.Rows[frame.Feed.Y:], rows)
	}
	transcript.FeedTop += frame.Feed.Y
	prompts.Rect = frame.Questions

	frame.Demands = transcript.OutputDemands()
	return frame
}

// Source: apps/mekugi/internal/router/native_welcome.go:46:58@ceef0946.
func welcome(version, backendVersion string) string {
	label := "Mekugi " + version
	if strings.HasPrefix(version, "v") {
		label = "mekugi-" + version
	}
	label += " • codex"
	if backendVersion != "" {
		label += " v" + backendVersion
	}
	return "\x1b[1m" + label + terminalui.Reset
}
