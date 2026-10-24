package launch

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/rollout"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Freeze the admitted owners before asynchronous work can outlive navigation.
func compactionSummaryReader(retained history.Session, path, thread, item string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if thread != retained.Thread {
			return "", errors.New("compaction summary belongs to another session")
		}
		reference := rollout.CompactionReferences(ctx, thread, path)[item]
		if err := ctx.Err(); err != nil {
			return "", err
		}
		summary, _, err := compact.ReadSummary(retained.Store, thread, reference)
		return summary, err
	}
}

// Borrow the existing physical-read slot and dialog cancellation/identity owner.
// Summary reads stay outside observation dispatch and carry no file-copy effect.
func (u *detailFiles) openCompactionSummary(session *sessionpresentation.Attachment, model *sessionpresentation.Model, workspace string, terminal *terminalsession.Attachment, view *activityui.View, read func(context.Context) (string, error)) {
	u.openDetailSelection(session, model.View().Version, nil, terminal, view, activity.DetailPages([]activity.Value{{Kind: "compaction", Body: "Loading summary…"}}), false)
	result := markdownFileResult{id: terminal.Dialog.ID, workspace: workspace, summary: true}
	if u.fileReadSlot == nil {
		u.fileReadSlot = make(chan struct{}, 1)
	}
	select {
	case u.fileReadSlot <- struct{}{}:
	default:
		result.err = errors.New("summary read capacity exhausted; close and reopen after the pending read finishes")
		u.loadedMarkdownFile(session, model, workspace, terminal, result)
		return
	}
	parent := u.context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	u.cancelRead = cancel
	results := make(chan markdownFileResult, 1)
	u.fileReads = results
	go func(slot chan struct{}) {
		result.source, result.err = read(ctx)
		if err := ctx.Err(); err != nil {
			result.source, result.err = "", err
		}
		<-slot
		results <- result
	}(u.fileReadSlot)
}
