package terminalsession

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

// Main retains renderer geometry, overlay navigation and caret navigation. It owns no editor,
// transcript, backend handles, or prompt records.
type Main struct {
	session                                                sessionpresentation.View
	paste                                                  *pasteBuffer
	Keybindings, InterruptLocked                           bool
	PredictionsDisabled                                    bool
	Menu                                                   Menu
	BTW                                                    BTWDock
	Resume                                                 ResumePicker
	Panel                                                  StatusPanel
	ComposerRect, PromptRect, NoticeDetails, NoticeDismiss Rect
	ComposerWidth                                          int
	ContentPainted                                         bool
	CursorColumn, PromptColumn                             *int
}

// mainInput borrows detached values and terminal layout state for one frame.
// Secret drafts are renderer-local and are never retained by Main.
type mainInput struct {
	Session                                                      sessionpresentation.View
	Draft                                                        composer.Draft
	Now                                                          time.Time
	ReadOnly, Focused, Empty, Picker, Secret, Question, TurnIdle bool
	Welcome, ApprovalAgent                                       string
	Selection                                                    string
	QuestionLayout                                               *conversationui.QuestionLayout
	ApprovalLayout                                               *conversationui.ApprovalLayout
}

type MainFrame struct {
	Notices               NoticeReceipt
	Demands               []activitypresentation.OutputDemand
	Rows                  []string
	Feed, Dock, Questions Rect
	composer              composerDocument
}

// composerDocument retains rendered draft rows before caret-following clipping.
// Secret input is already masked; the frame publishes this only after delivery.
type composerDocument struct {
	rows []string
	top  int
}

// layout reserves the transcript window and paints Main's surrounding docks.
// Source: apps/mekugi/internal/router/app_server_ui.go:1241:1455@[ce8700a8].
func (m *Main) layout(in mainInput, painter *activityui.Painter, width, height, dock int) (out MainFrame) {
	presentation, readOnly := in.Session, in.ReadOnly
	width, height = max(1, width), max(1, height)
	m.NoticeDetails, m.NoticeDismiss = Rect{}, Rect{}
	boxed := width >= 12 && height >= 3
	borderRows, inset := 0, min(2, width-1)
	if boxed {
		borderRows, inset = 2, 5
	}
	textWidth := width - inset
	m.ComposerWidth = textWidth
	draft, points := DraftLayout(in.Draft, m.ComposerWidth)
	if presentation.Status.ShellMode {
		PaintShellDraft(draft, in.Draft.Text, &painter.Painter)
	}
	if in.Secret {
		for i, line := range draft {
			draft[i] = strings.Repeat("•", ansi.StringWidth(line))
		}
	}
	caret := points[len(points)-1]
	for _, point := range points {
		if point.Offset == in.Draft.Cursor() {
			caret = point
			break
		}
	}
	if readOnly || presentation.Draft.Available && presentation.Admission.ReadOnly {
		draft = []string{""}
		caret.Row, caret.Column = 0, 0
	}
	if !readOnly && !in.Picker && !in.Question && !presentation.Approvals.Open && in.Draft.Text == "" && presentation.Draft.Prediction != "" {
		draft, _ = activityui.LayoutSpans(presentation.Draft.Prediction, nil, m.ComposerWidth)
		for i := range draft {
			draft[i] = terminalui.Dim + draft[i] + terminalui.Reset
		}
	}
	if dock == 0 && in.Empty && height >= 6 && height > min(6, len(draft))+borderRows && !in.Picker {
		height--
		defer func() {
			out.Rows = append([]string{ansi.Truncate(in.Welcome, max(1, width), "…")}, out.Rows...)
			out.Dock.Y++
			m.ComposerRect.Y++
			m.BTW.Rect.Y++
			m.PromptRect.Y++
			m.NoticeDetails.Y++
			m.NoticeDismiss.Y++
			out.Feed.Y++
		}()
	}
	visible := min(6, height-borderRows)
	firstRow := max(0, caret.Row-visible+1)
	out.composer = composerDocument{rows: draft, top: firstRow}
	draft = draft[firstRow:min(len(draft), firstRow+visible)]

	room := max(0, height-len(draft)-borderRows)
	var pending []string
	var approvalRows, questionRows, btwRows []string
	if !readOnly {
		if d := presentation.Approvals; d.Count > 0 {
			approvalRows = conversationui.Approvals(d, &painter.Painter, in.ApprovalLayout, in.Selection, in.ApprovalAgent, width, min(height/2, room))
		}
	}
	if len(approvalRows) > 0 {
		dock = 0
		room -= len(approvalRows)
	}
	if !readOnly {
		questionRows = conversationui.Questions(presentation.Questions, &painter.Painter, in.QuestionLayout, in.Selection, in.TurnIdle && in.Draft.Text != "", width, min(height/2, room))
	}
	if len(questionRows) > 0 {
		dock = 0
		room -= len(questionRows)
	}

	if !readOnly && m.BTW.Open() && !in.Picker {
		btwRows = m.BTW.Render(painter, in.Now, width, max(0, min(height/2, room))).Rows
		if len(btwRows) > 0 {
			dock = 0
			room -= len(btwRows)
		}
	}
	if in.Picker {
		dock = 0
	} else if !readOnly {
		// Waiting input yields to the transcript beyond half the room.
		pending = conversationui.Pending(presentation.Pending, width)
		pending = pending[:min(len(pending), room/2)]
		room -= len(pending)
	}
	// Reserve a blank row immediately above the composer, including below
	// pending input or a live dock.
	gap := 0
	if !in.Picker && room > 2 {
		gap = 1
		room--
	}
	dock = min(dock, room)
	room -= dock
	m.ContentPainted = room > 1
	out.Feed = Rect{W: width, H: room}
	frame := make([]string, room)
	dockAt := len(frame)
	frame = append(frame, make([]string, dock)...)
	frame = append(frame, pending...)

	out.Questions = Rect{0, len(frame), width, len(questionRows)}
	if presentation.Questions.Call != nil {
		m.PromptRect = out.Questions
	}
	frame = append(frame, questionRows...)
	if presentation.Approvals.Open {
		m.PromptRect = Rect{0, len(frame), width, len(approvalRows)}
	}
	frame = append(frame, approvalRows...)
	m.BTW.Rect = Rect{0, len(frame), width, len(btwRows)}
	frame = append(frame, btwRows...)
	frame = append(frame, make([]string, gap)...)
	// Visual reference: Grok CLI PromptStyle / PromptWidget::draw, source
	// crates/codegen/xai-grok-pager/src/views/prompt_widget/mod.rs:217:240,3007:3078
	// @be7ce6e8cffe46d20bef9834b211616082ee866b. Keep continuation rows aligned.
	// Colors: xai-grok-pager-render/src/theme/oscura.rs, same revision.
	border := "\x1b[38;2;52;48;72m"
	if !readOnly && (in.Question || presentation.Approvals.Open) {
		border = painter.Theme.Accent()
	} else if !readOnly && presentation.Status.ShellMode {
		border = terminalui.Red
	}
	const inputColor = "\x1b[39m"
	focused := !readOnly && in.Focused
	if boxed {
		frame = append(frame, m.noticeBorder(presentation.Status, in.Now, painter.Colors, width, len(frame), border))
	}
	textX := min(2, inset)
	if boxed {
		textX += 2
	}
	m.ComposerRect = Rect{textX, len(frame), textWidth, len(draft)}
	for i, line := range draft {
		prefix := strings.Repeat(" ", min(2, inset))
		if i == 0 && inset >= 2 {
			prefix = activityui.PromptColor + "❯ " + inputColor
		}
		line = ansi.Truncate(line, textWidth, "")
		if focused && textWidth > 1 && i+firstRow == caret.Row {
			before := ansi.Cut(line, 0, caret.Column)
			cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(in.Draft.Text[in.Draft.Cursor():], -1)
			cellWidth := max(1, ansi.StringWidth(textsanitize.Safe(cluster, false)))
			cell := ansi.Cut(line, caret.Column, caret.Column+cellWidth)
			if ansi.StringWidth(cell) == 0 {
				cell = " "
			}
			rest := ansi.Cut(line, caret.Column+cellWidth, textWidth)
			line = before + "\x1b[7m" + cell + "\x1b[27m" + rest
		}
		if boxed {
			frame = append(frame, border+"│"+inputColor+" "+prefix+line+strings.Repeat(" ", textWidth-ansi.StringWidth(line))+border+"│"+terminalui.Reset)
		} else {
			frame = append(frame, inputColor+prefix+line+terminalui.Reset)
		}
	}
	if boxed {
		cwd := strings.ReplaceAll(textsanitize.Safe(presentation.SourceWorkspace(), false), "\n", " ")
		cwd = ansi.Truncate(cwd, max(1, (width-10)/3), "…")
		captionWidth := max(0, width-7)
		if cwd != "" {
			captionWidth = max(0, width-8-ansi.StringWidth(cwd))
		}
		model := presentation.Caption.Model
		if model != "" && presentation.Caption.Effort != "" {
			model += " (" + presentation.Caption.Effort + ")"
		}
		if presentation.Caption.Tier != "" {
			model += " · " + presentation.Caption.Tier
		}
		if role := presentation.Caption.Role; role != "" {
			if model != "" {
				model = role + " · " + model
			} else {
				model = role
			}
		}
		model = strings.ReplaceAll(textsanitize.Safe(model, false), "\n", " ")
		var context strings.Builder
		context.WriteString(activityui.ContextWindowLabel(activitypresentation.Agent{
			ContextKnown: presentation.Caption.ContextKnown, ContextTokens: presentation.Caption.ContextTokens, ContextWindow: presentation.Caption.ContextWindow}))
		for _, item := range presentation.Caption.Items {
			context.WriteString(" • " + textsanitize.Safe(item.Text, false))
		}
		caption := context.String()
		if room := captionWidth - ansi.StringWidth(context.String()) - 3; model != "" && room > 0 {
			caption = ansi.Truncate(model, room, "…") + " • " + context.String()
		}
		caption = ansi.Truncate(caption, captionWidth, "…")
		if cwd != "" {
			caption = cwd + " " + caption
		}
		caption = terminalui.Dim + caption + terminalui.Reset
		frame = append(frame, composerBorder("╰", "╯", "", caption, width, border))
	}
	out.Rows, out.Dock = frame, Rect{0, dockAt, width, dock}
	return out
}

// composerBorder embeds optional left and right labels in a box edge. The
// right label yields first when the edge is too narrow for both.
func composerBorder(open, close, left, right string, width int, color string) string {
	segment := func(label string) string {
		if label == "" {
			return ""
		}
		return " \x1b[39m" + label + color + " "
	}
	inner := width - 2
	l, r := segment(left), segment(right)
	if inner-ansi.StringWidth(l)-ansi.StringWidth(r)-2 < 0 {
		r = ""
	}
	if room := inner - ansi.StringWidth(r) - 2; ansi.StringWidth(l) > room {
		l = ""
		if room > 4 {
			l = ansi.Truncate(segment(left), room-1, "…") + color + " "
		}
	}
	fill := max(0, inner-ansi.StringWidth(l)-ansi.StringWidth(r)-2)
	return color + open + "─" + l + strings.Repeat("─", fill) + r + "─" + close + terminalui.Reset
}

func (m *Main) noticeBorder(status sessionpresentation.MainStatus, now time.Time, colors terminalui.Colors, width, row int, color string) string {
	label := PaintMainStatus(status, now, colors)
	full := MainStatusDetails(status)
	if full == "" {
		return composerBorder("╭", "╮", label, "", width, color)
	}
	// Reserve the check before truncating, so a long error cannot hide it.
	room := max(1, width-13)
	// In compact panes the error takes precedence over the session caption.
	plain := ansi.Strip(label)
	if before, _, ok := strings.Cut(plain, "✗ "); ok && ansi.StringWidth(before)+4 > room {
		label = terminalui.Red + "✗ " + activityui.ErrorPreview(full) + terminalui.Reset
	}
	details := activityui.ErrorHasDetails(activityui.Block{Body: full})
	if details && !strings.HasSuffix(ansi.Strip(label), "…") {
		label += "…"
	}
	clipped := ansi.StringWidth(label) > room
	label = ansi.Truncate(label, room, "…")
	if clipped || details {
		m.NoticeDetails = Rect{3, row, ansi.StringWidth(label), 1}
	}
	m.NoticeDismiss = Rect{width - 6, row, 3, 1}
	return composerBorder("╭", "╮", label, terminalui.Green+"[✓]"+terminalui.Reset, width, color)
}
