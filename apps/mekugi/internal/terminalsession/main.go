package terminalsession

import (
	"cmp"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
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
	ProcessCount                                           Rect
	ProcessHover                                           bool // The pointer is over ProcessCount.
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
	HideComposer                                                 bool
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
	m.NoticeDetails, m.NoticeDismiss, m.ProcessCount = Rect{}, Rect{}, Rect{}
	if in.HideComposer {
		m.ComposerRect, m.BTW.Rect = Rect{}, Rect{}
		m.ContentPainted = height > 1
		return MainFrame{Rows: make([]string, height), Feed: Rect{W: width, H: height}}
	}
	boxed := width >= 12 && height >= 3
	// Rules frame the draft only when the transcript keeps rows beside all
	// three chrome rows.
	ruled := boxed && height >= 8
	borderRows, inset := 0, min(2, width-1)
	if boxed {
		borderRows, inset = 1, 3
	}
	if ruled {
		borderRows = 3
	}
	textWidth := width - inset
	m.ComposerWidth = textWidth
	draft, points := DraftLayout(painter.Theme, in.Draft, m.ComposerWidth)
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
		draft, _ = activityui.LayoutSpans(painter.Theme, presentation.Draft.Prediction, nil, m.ComposerWidth)
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
			m.ProcessCount.Y++
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
			approvalRows = conversationui.Approvals(d, &painter.Painter, in.ApprovalLayout, in.ApprovalAgent, width, min(height/2, room))
		}
	}
	if len(approvalRows) > 0 {
		dock = 0
		room -= len(approvalRows)
	}
	if !readOnly {
		questionRows = conversationui.Questions(presentation.Questions, &painter.Painter, in.QuestionLayout, presentation.Info.Caller, in.TurnIdle && in.Draft.Text != "", width, min(height/2, room))
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
	// The draft sits between two rules, under a plain status line; the
	// session caption is the shell's first stat line. Its prompt glyph carries the input state: accent
	// while a prompt above takes the input, red in shell mode, which also
	// colors the rules.
	promptColor := activityui.PromptColor
	focused := !readOnly && in.Focused
	switch {
	case !readOnly && presentation.Status.ShellMode:
		promptColor = painter.Theme.Danger()
	case !readOnly && (in.Question || presentation.Approvals.Open):
		promptColor = painter.Theme.Accent()
	}
	const inputColor = "\x1b[39m"
	rule := terminalui.Dim + strings.Repeat("─", width) + terminalui.Reset
	if !readOnly && presentation.Status.ShellMode {
		rule = painter.Theme.Danger() + strings.Repeat("─", width) + terminalui.Reset
	}
	if boxed {
		frame = append(frame, m.statusLine(painter.Theme, presentation.Status, in.Now, painter.Colors, width, len(frame)))
	}
	if ruled {
		frame = append(frame, rule)
	}
	textX := min(2, inset)
	m.ComposerRect = Rect{textX, len(frame), textWidth, len(draft)}
	for i, line := range draft {
		prefix := strings.Repeat(" ", min(2, inset))
		if i == 0 && inset >= 2 {
			prefix = promptColor + "❯ " + inputColor
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
		frame = append(frame, inputColor+prefix+line+terminalui.Reset)
	}
	if ruled {
		frame = append(frame, rule)
	}
	out.Rows, out.Dock = frame, Rect{0, dockAt, width, dock}
	return out
}

// captionModel colors the stat lines' model and figures, after the stock
// status line; limits use the theme's information color.
func captionModel(theme diffrender.Theme) string {
	switch theme {
	case diffrender.LightTheme:
		return "\x1b[38;2;24;100;110m"
	case diffrender.DarkTheme:
		return "\x1b[38;5;73m"
	default:
		return "\x1b[36m"
	}
}

// captionUsage is the displayed thread's token and cost use, with the
// session total on Main.
type captionUsage struct{ Tokens, Cost, Total string }

// captionLine is the first stat line, at the screen bottom: model, context
// bar, cost, tokens, contributed items such as throughput, then account usage.
// Narrow panes drop context
// tokens, then items, the total cost, tokens and usage, shorten the context
// to its percentage, drop the cost, then shorten the model, and finally drop
// the context.
func captionLine(theme diffrender.Theme, c sessionpresentation.Caption, usage captionUsage, width int) string {
	const indent, sep = "  ", " | "
	inner := max(0, width-1-len(indent))
	model := c.Model
	if model != "" && c.Effort != "" {
		model += " (" + c.Effort + ")"
	}
	if c.Tier != "" {
		if model != "" {
			model += " · "
		}
		model += c.Tier
	}
	model = strings.ReplaceAll(textsanitize.Safe(model, false), "\n", " ")
	bare := model
	if model != "" {
		model = captionModel(theme) + bare + terminalui.Reset
	}
	var limits []string
	for _, limit := range c.Limits {
		label := textsanitize.Safe(limit.Label, false)
		if label != "" {
			label = strings.ToUpper(label[:1]) + label[1:]
		}
		limits = append(limits, theme.Info()+label+": "+fmt.Sprintf("%.0f%%", limit.Used)+terminalui.Reset)
	}
	var items []string
	for _, item := range c.Items {
		if text := strings.ReplaceAll(textsanitize.Safe(item.Text, false), "\n", " "); text != "" {
			items = append(items, captionModel(theme)+text+terminalui.Reset)
		}
	}
	context := contextLevels(theme, c)
	level := 0
	cost, total, tokens := usage.Cost, usage.Total, usage.Tokens
	paint := func() (string, int) {
		var parts []string
		costText := ""
		switch {
		case cost != "" && total != "":
			costText = captionModel(theme) + cost + terminalui.Dim + " · total " + total + terminalui.Reset
		case cost != "" || total != "":
			costText = captionModel(theme) + cmp.Or(cost, total) + terminalui.Reset
		}
		tokenText := ""
		if tokens != "" {
			tokenText = terminalui.Dim + tokens + terminalui.Undim
		}
		for _, part := range []string{model, context[level], costText, tokenText} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		// Contributed items such as throughput follow the token figures.
		parts = append(parts, items...)
		if len(limits) > 0 {
			parts = append(parts, strings.Join(limits, " "))
		}
		line := strings.Join(parts, terminalui.Dim+sep+terminalui.Undim)
		return line, ansi.StringWidth(line)
	}
	line, used := paint()
	if used > inner && len(context) > 2 {
		level = 1
		line, used = paint()
	}
	if used > inner {
		items = nil
		line, used = paint()
	}
	if used > inner && cost != "" && total != "" {
		total = ""
		line, used = paint()
	}
	if used > inner {
		tokens = ""
		line, used = paint()
	}
	if used > inner {
		limits = nil
		line, used = paint()
	}
	for used > inner && level < len(context)-2 {
		level++
		line, used = paint()
	}
	if used > inner {
		cost, total = "", ""
		line, used = paint()
	}
	if used > inner && model != "" {
		if room := ansi.StringWidth(model) - (used - inner); room >= 3 {
			model = captionModel(theme) + ansi.Truncate(bare, room, "…") + terminalui.Reset
		} else {
			model = ""
		}
		line, used = paint()
	}
	if used > inner {
		level = len(context) - 1
		line, _ = paint()
	}
	return indent + ansi.Truncate(line, inner, "…") + terminalui.Reset
}

// contextLevels lists the context segment from fullest to empty: the fill bar
// with percentage and tokens, the bar with percentage, then the percentage.
func contextLevels(theme diffrender.Theme, c sessionpresentation.Caption) []string {
	if !c.ContextKnown || c.ContextWindow == 0 {
		return []string{activityui.ContextWindowLabel(activitypresentation.Agent{
			ContextKnown: c.ContextKnown, ContextTokens: c.ContextTokens, ContextWindow: c.ContextWindow}), ""}
	}
	used := min(1, float64(c.ContextTokens)/float64(c.ContextWindow))
	color := captionModel(theme)
	switch {
	case used >= .85:
		color = theme.Danger()
	case used >= .6:
		color = theme.Warning()
	}
	percent := color + fmt.Sprintf("%.0f%%", 100*used) + terminalui.Reset
	bar := contextBar(used, color) + " " + percent
	tokens := activityui.FormatUsageTokens(c.ContextTokens) + "/" + activityui.FormatUsageTokens(c.ContextWindow)
	return []string{bar + terminalui.Dim + " " + tokens + terminalui.Undim, bar, percent, ""}
}

// contextBar draws window use as a ten-cell rule: a heavy colored fill with a
// half-cell tip on a dim track.
func contextBar(used float64, color string) string {
	const cells = 10
	halves := int(math.Round(used * cells * 2))
	full, half := halves/2, halves%2
	return color + strings.Repeat("━", full) + strings.Repeat("╸", half) + terminalui.Reset +
		terminalui.Dim + strings.Repeat("─", cells-full-half) + terminalui.Undim
}

// statusLine shows the turn status above the draft, from column 0. Notices
// keep a dismissal control after their label.
func (m *Main) statusLine(theme diffrender.Theme, status sessionpresentation.MainStatus, now time.Time, colors terminalui.Colors, width, row int) string {
	label, count := paintMainStatus(theme, status, now, colors, m.session.ReadOnly != sessionpresentation.Replay, m.ProcessHover)
	hit := func(visible int) {
		if count.W > 0 && count.X+count.W <= visible {
			m.ProcessCount = Rect{count.X, row, count.W, 1}
		}
	}
	full := MainStatusDetails(status)
	if full == "" {
		room := max(1, width-1)
		visible := room
		if ansi.StringWidth(label) > room {
			visible--
		}
		hit(visible)
		return ansi.Truncate(label, room, "…") + terminalui.Reset
	}
	// Reserve dismissal before truncating, so a long error cannot hide it.
	room := max(1, width-5)
	// In compact panes the error takes precedence over the session caption.
	plain := ansi.Strip(label)
	if before, _, ok := strings.Cut(plain, "✗ "); ok && ansi.StringWidth(before)+4 > room {
		label = theme.Danger() + "✗ " + activitypresentation.ErrorPreview(full) + terminalui.Reset
		count = Rect{}
	}
	details := activityui.ErrorHasDetails(activityui.Block{Body: full})
	if details && !strings.HasSuffix(ansi.Strip(label), "…") {
		label += "…"
	}
	clipped := ansi.StringWidth(label) > room
	visible := room
	if clipped {
		visible--
	}
	hit(visible)
	label = ansi.Truncate(label, room, "…")
	if clipped || details {
		m.NoticeDetails = Rect{0, row, ansi.StringWidth(label), 1}
	}
	m.NoticeDismiss = Rect{1 + ansi.StringWidth(label), row, 3, 1}
	return label + terminalui.Reset + " " + terminalui.Dim + "[x]" + terminalui.Reset
}
