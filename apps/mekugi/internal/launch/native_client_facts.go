package launch

import (
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (c *nativeClient) acceptFacts(previous string) {
	facts := c.event.Frame.Facts
	if previous != c.event.Frame.View.Main.ID && c.notifications.focused {
		c.enqueue(sessionapi.NativeOperation{Focused: new(c.notifications.focused)}, nil, nil)
	}
	history := make([]composer.HistoryEntry, len(facts.History))
	for i, entry := range facts.History {
		history[i] = entry
		history[i].Draft = entry.Draft.Clone()
		history[i].Restored = true
	}
	c.editor("main").PrependHistory(history)
	n := &c.notifications
	n.ready, n.settings.Events, n.settings.Method, n.settings.Condition = facts.Notifications.Ready, facts.Notifications.Events, facts.Notifications.Method, facts.Notifications.Condition
	if c.event.Frame.View.Main.ID != "" && (!c.preferenceReady || previous != c.event.Frame.View.Main.ID) && facts.Preferences != nil && facts.Preferences.Kind() != 'n' {
		layout, err := terminalsession.ReadPreferences(facts.Preferences)
		if err != nil {
			c.notice = err.Error()
			return
		}
		u := c.renderer.terminal
		u.Shell.RestorePreferences(layout, u.Main)
		c.preferenceState, c.preferenceReady = u.Shell.Preferences(u.Main), true
		c.dirty = true
	}
}

// Only one completion acquisition is outstanding. Edits made during it remain in
// the local editor and the newest value is sampled when the owner is available.
func (c *nativeClient) acquireCompletion() {
	if c.event.Attachment == "" || c.secretKey != "" {
		return
	}
	editor, target := c.selectedEditor()
	if target.Call != "" || target.Request != "" {
		return
	}
	input := c.event.Frame.View.Input.ID
	if c.completionReady && target == c.completionTarget && input == c.completionInput && c.completionCatalog == c.event.Frame.CompletionCatalog {
		return
	}
	c.completionTarget, c.completionInput, c.completionReady = target, input, true
	c.completionCatalog = c.event.Frame.CompletionCatalog
	c.enqueue(sessionapi.NativeOperation{Completion: &sessionapi.NativeEditorInput{Target: target, Draft: editor.Draft.Clone()}}, nil, nil)
}

// Applied effects need no paint or output hydration. Notice identities come only
// from successful paints, and a queued receipt can cover newer applied effects.
func (c *nativeClient) stageReceipt(notice *sessionapi.NativeNoticeReceipt) {
	for _, call := range c.calls {
		if call.operation.Receipt != nil && (call.target.Attachment != c.event.Attachment || call.target.Version.Attachment != c.event.Frame.View.Version.Attachment) {
			call.target = c.event.Target()
			call.operation.Receipt.Notice = sessionapi.NativeNoticeReceipt{}
			c.receipt = sessionapi.NativeNoticeReceipt{}
		}
	}
	if c.effect == c.receiptEffect && (notice == nil || *notice == c.receipt) {
		return
	}
	if notice != nil {
		c.receipt = *notice
	}
	c.receiptEffect = c.effect
	for _, call := range c.calls {
		if call.operation.Receipt != nil {
			call.target = c.event.Target()
			call.operation.Receipt.Effect = c.effect
			if notice != nil {
				call.operation.Receipt.Notice = *notice
			}
			return
		}
	}
	receipt := &sessionapi.NativeReceipt{Effect: c.effect}
	if notice != nil {
		receipt.Notice = *notice
	}
	c.enqueue(sessionapi.NativeOperation{Receipt: receipt}, nil, nil)
}

func (c *nativeClient) stagePreferences(flush bool) {
	if !c.preferenceReady || c.event.Frame.View.Main.ID == "" {
		return
	}
	u := c.renderer.terminal
	state := u.Shell.Preferences(u.Main)
	if !flush && state == c.preferenceState {
		return
	}
	data, err := c.preferences.Encode(state)
	if err != nil {
		c.notice = err.Error()
		return
	}
	c.preferenceState = state
	for _, call := range c.calls {
		if update := call.operation.Preferences; update != nil {
			update.Presentation = data
			update.Flush = update.Flush || flush
			return
		}
	}
	c.enqueue(sessionapi.NativeOperation{Preferences: &sessionapi.NativePreferenceUpdate{Presentation: data, Flush: flush}}, nil, nil)
}

func (c *nativeClient) flushNativeView() {
	if c.event.Frame.View.Main.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if text := c.editor("main").Text; text != "" {
		if _, err := c.connection.Send(ctx, c.event.Target(), sessionapi.NativeOperation{SaveDraft: &text}); err != nil {
			c.notice = err.Error()
		}
	}
	c.flushPreferences(ctx)
}

func (c *nativeClient) flushPreferences(ctx context.Context) {
	if !c.preferenceReady || c.event.Frame.View.Main.ID == "" {
		return
	}
	u := c.renderer.terminal
	data, err := c.preferences.Encode(u.Shell.Preferences(u.Main))
	if err == nil {
		_, err = c.connection.Send(ctx, c.event.Target(), sessionapi.NativeOperation{Preferences: &sessionapi.NativePreferenceUpdate{Presentation: data, Flush: true}})
	}
	if err != nil {
		c.notice = err.Error()
	}
}

func (c *nativeClient) hydrateDetails() {
	d := c.renderer.terminal.Dialog
	if c.detailHydrating || d == nil || d.Page < 0 || d.Page >= len(d.Pages) {
		return
	}
	page := d.Pages[d.Page]
	if !page.Retained || d.Output != nil && d.Output.Page == page.ID && d.Output.Version == page.OutputVersion {
		return
	}
	c.detailHydrating = true
	c.enqueue(sessionapi.NativeOperation{DetailOutput: &sessionpresentation.DetailOutputRequest{ID: d.ID, Page: page.ID, Version: page.OutputVersion}}, nil, func(error) { c.detailHydrating = false })
}
