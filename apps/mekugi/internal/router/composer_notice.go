package router

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/notifications/notice"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Successful feedback is transient; actionable errors remain until editing.
func (u *appServerUI) setNotice(text string, alert bool) {
	u.feedback.Set(text, alert, u.now())
	u.noticeDetails, u.noticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
}

func (u *appServerUI) expireNotice(now time.Time) bool {
	if !u.feedback.Expire(now) {
		return false
	}
	u.noticeDetails, u.noticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	return true
}

// stateLabel is the session state followed by any composer notice.
func (u *appServerUI) stateLabel(now time.Time) string {
	view := u.sessionPresentation().View()
	if view.Displayed.ID != view.Main.ID {
		return "Read-only"
	}
	label := u.sessionLabel(now)
	if u.approvals.Open {
		label = "approving · turn waiting"
	} else if u.activeQuestionCall() != nil {
		label = "answering"
		if u.currentQuestion().Note {
			label += " · note"
		}
		if len(u.activeQuestionCall().request) > 0 || u.activeQuestionCall().sudo != nil {
			label += " · turn waiting"
		}
		if u.promptEditor.Parked.Snapshot.Text != "" {
			label += " · draft kept"
		}
	}
	if u.shellMode() {
		label = terminalui.Red + "Shell Mode" + terminalui.Reset + " · " + label
	}
	if count := u.runningProcessCount(); count > 0 {
		if label != "" {
			label += terminalui.Dim + " · " + terminalui.Undim
		}
		label += activityui.StatusPulse(fmt.Sprintf("%d running processes", count), now, u.view.painter.Colors) + terminalui.Reset
	}
	notice := strings.ReplaceAll(textsanitize.Safe(u.feedback.Text, false), "\n", " ")
	switch {
	case notice == "":
		return label
	case u.feedback.Alert:
		notice = terminalui.Red + "✗ " + activityui.ErrorPreview(u.feedback.Text) + terminalui.Reset
	default:
		notice = "\x1b[39m" + notice + terminalui.Reset
	}
	if label == "" {
		return notice
	}
	return label + terminalui.Dim + " · " + terminalui.Undim + notice
}

// Composer errors stay presentation-only. Model errors retain their transcript
// entries; both surfaces use the same preview and full error dialog.
func (u *appServerUI) composerErrorText() string {
	view := u.sessionPresentation().View()
	if view.Displayed.ID != view.Main.ID {
		return ""
	}
	var parts []string
	if u.alert && u.status != "" {
		parts = append(parts, u.status)
	}
	if u.feedback.Alert && u.feedback.Text != "" {
		parts = append(parts, u.feedback.Text)
	}
	return strings.Join(parts, "\n")
}

func (u *appServerUI) composerNoticeBorder(width, row int, color string) string {
	label := u.stateLabel(u.now())
	full := u.composerErrorText()
	if full == "" {
		return composerBorder("╭", "╮", label, "", width, color)
	}
	// Reserve the check before truncating, so a long error cannot hide it.
	room := max(1, width-13)
	// In compact panes the error takes precedence over the session caption.
	plain := ansi.Strip(label)
	if mark := strings.Index(plain, "✗ "); mark >= 0 && ansi.StringWidth(plain[:mark])+4 > room {
		label = terminalui.Red + "✗ " + activityui.ErrorPreview(full) + terminalui.Reset
	}
	details := activityui.ErrorHasDetails(activityui.Block{Body: full})
	if details && !strings.HasSuffix(ansi.Strip(label), "…") {
		label += "…"
	}
	clipped := ansi.StringWidth(label) > room
	label = ansi.Truncate(label, room, "…")
	if clipped || details {
		u.noticeDetails = terminalsession.Rect{3, row, ansi.StringWidth(label), 1}
	}
	u.noticeDismiss = terminalsession.Rect{width - 6, row, 3, 1}
	return composerBorder("╭", "╮", label, terminalui.Green+"[✓]"+terminalui.Reset, width, color)
}

func (u *appServerUI) composerNoticeMouse(button, x, y int, release bool) bool {
	details, dismiss := u.noticeDetails.Contains(x, y), u.noticeDismiss.Contains(x, y)
	if (!details && !dismiss) || u.composerErrorText() == "" {
		return false
	}
	if !release && button == 0 {
		if dismiss {
			u.setNotice("", false)
			if u.alert {
				u.status, u.alert = "", false
			}
		} else {
			u.shell.openBlocks(u.view, []activityui.Block{{Kind: "error", Body: u.composerErrorText()}})
		}
		u.dirty = true
	}
	return true
}

// Native delivery keeps the batch's complete composer text beside its claim.
// This lets terminal acknowledgement distinguish visible errors from overlays.
type nativeCriticalNoticeDelivery struct {
	ui               *appServerUI
	composerText     string
	errors, progress notice.Batch
}

// A successful write can show the composer while transcript progress is hidden.
// Each subset keeps the queue owner's original claim and repeat-count snapshot.
func (d *nativeCriticalNoticeDelivery) finish(success bool) {
	if d == nil {
		return
	}
	u := d.ui
	visible := success && (u.shell == nil || u.shell.output == nil && u.shell.selection == nil)
	d.errors.Finish(visible && u.feedback.Text == d.composerText && u.noticeDismiss.W > 0)
	d.progress.Finish(visible && u.mainContentPainted)
}

func (u *appServerUI) applyCriticalNotices() *nativeCriticalNoticeDelivery {
	var activity *subagentActivity
	if u.proxy != nil {
		activity = u.proxy.activity
	}
	claim := u.issues.takeNative(u.Thread, activity)
	if claim == nil {
		return nil
	}
	delivery := &nativeCriticalNoticeDelivery{ui: u, errors: claim.Errors, progress: claim.Progress}
	for _, message := range u.feedback.Receive(claim, u.now()) {
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.LastSeq + 1, Agent: "Main", Kind: "progress", Text: message.Text, Observed: u.now()}}})
	}
	if len(claim.Errors.Messages) > 0 {
		delivery.composerText = u.feedback.Text
		u.noticeDetails, u.noticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	}
	u.dirty = true
	return delivery
}
