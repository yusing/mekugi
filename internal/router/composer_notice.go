package router

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Successful feedback is transient; actionable errors remain until editing.
func (u *appServerUI) setNotice(text string, alert bool) {
	u.notice, u.noticeAlert = text, alert
	u.noticeDetails, u.noticeDismiss = terminalRect{}, terminalRect{}
	u.noticeUntil = time.Time{}
	if text != "" && !alert {
		u.noticeUntil = u.now().Add(3 * time.Second)
	}
}

func (u *appServerUI) expireNotice(now time.Time) bool {
	if u.noticeUntil.IsZero() || now.Before(u.noticeUntil) {
		return false
	}
	u.setNotice("", false)
	return true
}

// stateLabel is the session state followed by any composer notice.
func (u *appServerUI) stateLabel(now time.Time) string {
	label := u.sessionLabel(now)
	if u.approvals.open {
		label = "approving · turn waiting"
	} else if u.questions.active != nil {
		label = "answering"
		if u.currentQuestion().note {
			label += " · note"
		}
		if len(u.questions.active.request) > 0 {
			label += " · turn waiting"
		}
		if u.questions.parked.snapshot.text != "" {
			label += " · draft kept"
		}
	}
	if u.shellMode() {
		label = activityui.Red + "Shell Mode" + activityui.Reset + " · " + label
	}
	notice := strings.ReplaceAll(livediff.Safe(u.notice, false), "\n", " ")
	switch {
	case notice == "":
		return label
	case u.noticeAlert:
		notice = activityui.Red + "✗ " + activityui.ErrorPreview(u.notice) + activityui.Reset
	default:
		notice = "\x1b[39m" + notice + activityui.Reset
	}
	if label == "" {
		return notice
	}
	return label + activityui.Dim + " · " + activityui.Undim + notice
}

// Composer errors stay presentation-only. Model errors retain their transcript
// entries; both surfaces use the same preview and full error dialog.
func (u *appServerUI) composerErrorText() string {
	var parts []string
	if u.alert && u.status != "" {
		parts = append(parts, u.status)
	}
	if u.noticeAlert && u.notice != "" {
		parts = append(parts, u.notice)
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
		label = activityui.Red + "✗ " + activityui.ErrorPreview(full) + activityui.Reset
	}
	details := activityui.ErrorHasDetails(activityui.Block{Body: full})
	if details && !strings.HasSuffix(ansi.Strip(label), "…") {
		label += "…"
	}
	clipped := ansi.StringWidth(label) > room
	label = ansi.Truncate(label, room, "…")
	if clipped || details {
		u.noticeDetails = terminalRect{3, row, ansi.StringWidth(label), 1}
	}
	u.noticeDismiss = terminalRect{width - 6, row, 3, 1}
	return composerBorder("╭", "╮", label, activityui.Green+"[✓]"+activityui.Reset, width, color)
}

func (u *appServerUI) composerNoticeMouse(button, x, y int, release bool) bool {
	details, dismiss := u.noticeDetails.contains(x, y), u.noticeDismiss.contains(x, y)
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
	errors, progress criticalNoticeDelivery
}

// A successful write can show the composer while transcript progress is hidden.
// Each subset keeps the queue owner's original claim and repeat-count snapshot.
func (d *nativeCriticalNoticeDelivery) finish(success bool) {
	if d == nil {
		return
	}
	u := d.ui
	visible := success && (u.shell == nil || u.shell.output == nil && u.shell.selection == nil)
	d.errors.finish(visible && u.notice == d.composerText && u.noticeDismiss.w > 0)
	d.progress.finish(visible && u.mainContentPainted)
}

func (u *appServerUI) applyCriticalNotices() *nativeCriticalNoticeDelivery {
	var activity *subagentActivity
	if u.proxy != nil {
		activity = u.proxy.activity
	}
	claim := u.issues.takeNative(u.thread, activity)
	if claim == nil {
		return nil
	}
	delivery := &nativeCriticalNoticeDelivery{ui: u, errors: criticalNoticeDelivery{owner: claim.owner}, progress: criticalNoticeDelivery{owner: claim.owner}}
	if u.noticeEntries == nil {
		u.noticeEntries = make(map[string]bool)
	}
	var errors []string
	for i, notice := range claim.snapshots {
		text := noticeText(&notice)
		if notice.thread != "" && notice.thread != u.thread && activity != nil {
			activity.mu.Lock()
			if node := activity.threads[notice.thread]; node != nil {
				text = node.name + ": " + text
			}
			activity.mu.Unlock()
		}
		if notice.category != "storage_cleanup_planning" && notice.category != "storage_cleanup_reclaimed" {
			errors = append(errors, text)
			delivery.errors.notices = append(delivery.errors.notices, claim.notices[i])
			delivery.errors.snapshots = append(delivery.errors.snapshots, notice)
			continue
		}
		delivery.progress.notices = append(delivery.progress.notices, claim.notices[i])
		delivery.progress.snapshots = append(delivery.progress.snapshots, notice)
		if !u.noticeEntries[notice.id] {
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
				Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "progress", Text: text, Observed: u.now(),
			}}})
			u.noticeEntries[notice.id] = true
		}
	}
	if len(errors) > 0 {
		delivery.composerText = strings.Join(errors, "\n\n")
		u.setNotice(delivery.composerText, true)
	}
	u.dirty = true
	return delivery
}
