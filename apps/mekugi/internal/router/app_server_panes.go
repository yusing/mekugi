package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func (u *terminalUI) paneState() terminalsession.Preferences {
	u.Shell.Clamp()
	state := terminalsession.Preferences{Focus: u.Focus, Split: u.Split, DiffOpen: u.DiffOpen}
	if navigator, ok := u.Auxiliary.(terminalui.PaneNavigator); ok {
		state.NavigatorColumns = navigator.NavigatorColumns()
	}
	if u.main != nil {
		state.InterruptLocked = u.main.mainView.InterruptLocked
	}
	return state
}

type nativePreferences struct {
	service    *preferences.Service
	attachment *preferences.Attachment
	encoding   terminalsession.PreferenceEncoder
}

type nativePreferenceSnapshot struct {
	preferences.Snapshot
	layout terminalsession.Preferences
}

func (p *nativePreferences) read(workspace, thread string) (nativePreferenceSnapshot, error) {
	state, err := p.service.Read(preferences.Scope{Workspace: workspace, Thread: thread}, "terminal")
	if err != nil {
		return nativePreferenceSnapshot{}, err
	}
	result := nativePreferenceSnapshot{Snapshot: state}
	if state.Presentation == nil {
		return result, nil
	}
	result.layout, err = terminalsession.ReadPreferences(state.Presentation)
	if err != nil {
		return nativePreferenceSnapshot{}, err
	}
	return result, nil
}

func (p *nativePreferences) open(u *terminalUI, workspace, thread string, resume bool) error {
	p.attachment = nil
	initial, err := p.encoding.Encode(u.paneState())
	if err != nil {
		return err
	}
	p.attachment, err = p.service.Attach(preferences.Scope{Workspace: workspace, Thread: thread}, "terminal", initial)
	if err != nil || !resume {
		return err
	}
	state, err := p.read(workspace, thread)
	if err != nil || state.Presentation == nil {
		return err
	}
	layout := state.layout
	u.Panes.Restore(layout)
	u.main.mainView.InterruptLocked = layout.InterruptLocked
	u.Shell.Clamp()
	if navigator, ok := u.Auxiliary.(terminalui.PaneNavigator); ok {
		navigator.SetNavigatorColumns(layout.NavigatorColumns)
	}
	state.Presentation, err = p.encoding.Encode(u.paneState())
	if err != nil {
		return err
	}
	p.attachment.Restore(state.Snapshot)
	return nil
}

func (p *nativePreferences) stage(layout terminalsession.Preferences) error {
	state, err := p.encoding.Encode(layout)
	if err != nil {
		return err
	}
	p.attachment.StagePresentation(state)
	return nil
}

func (p *nativePreferences) save(u *terminalUI, now time.Time, flush bool) error {
	if p == nil || p.attachment == nil {
		return nil
	}
	if err := p.stage(u.paneState()); err != nil {
		return err
	}
	return p.attachment.Save(now, flush)
}

// Only authoritative start/resume results and settings notifications reach here.
func (u *appServerUI) retainAppliedSettings() {
	if u.panes == nil || u.panes.attachment == nil || u.model == "" || u.Settings.RestoreEffort {
		return
	}
	scope := preferences.Scope{Workspace: u.cwd, Thread: u.Thread}
	if u.panes.attachment.Scope() != scope {
		if err := u.panes.open(u.shell, u.cwd, u.Thread, false); err != nil {
			u.paneError(err)
			return
		}
	}
	if err := u.panes.stage(u.shell.paneState()); err != nil {
		u.paneError(err)
		return
	}
	u.panes.attachment.Apply(preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier, Observed: u.now()})
	u.paneError(u.panes.attachment.Save(u.now(), true))
}

func (u *appServerUI) paneError(err error) {
	if err == nil {
		return
	}
	u.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: u.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: "Pane layout or settings could not be restored or saved: " + err.Error(), Observed: time.Now()}}})
	u.dirty = true
}
