package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// The terminal owns its schema; the preference module keeps it opaque.
type nativePaneState struct {
	Focus            int  `json:"focus"`
	Split            int  `json:"split"`
	DiffOpen         bool `json:"diffOpen"`
	InterruptLocked  bool `json:"interruptLocked,omitzero"`
	NavigatorColumns int  `json:"navigatorColumns"`
}

func (u *terminalUI) paneState() nativePaneState {
	state := nativePaneState{Focus: u.focus, Split: u.split, DiffOpen: u.diffOpen, NavigatorColumns: u.diff.navigation.Columns}
	if u.main != nil {
		state.InterruptLocked = u.main.interruptLocked
	}
	return state
}

type nativePreferences struct {
	service    *preferences.Service
	attachment *preferences.Attachment
	layout     nativePaneState
	encoded    jsontext.Value
}

// Cache only terminal encoding, so unchanged frames do not marshal JSON.
func (p *nativePreferences) presentation(u *terminalUI) (jsontext.Value, error) {
	state := u.paneState()
	if p.encoded == nil || state != p.layout {
		encoded, err := json.Marshal(&state)
		if err != nil {
			return nil, err
		}
		p.layout, p.encoded = state, encoded
	}
	return p.encoded, nil
}

type nativePreferenceSnapshot struct {
	preferences.Snapshot
	layout nativePaneState
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
	if err := json.Unmarshal(state.Presentation, &result.layout); err != nil {
		return nativePreferenceSnapshot{}, err
	}
	layout := result.layout
	if layout.Focus < 0 || layout.Focus > 3 || layout.Split < 0 || layout.Split > 10000 || layout.NavigatorColumns < 0 || layout.NavigatorColumns > 10000 || layout.Focus == 1 && !layout.DiffOpen || layout.Focus == 2 && layout.DiffOpen {
		return nativePreferenceSnapshot{}, errors.New("invalid or unsupported pane state")
	}
	return result, nil
}

func (p *nativePreferences) open(u *terminalUI, workspace, thread string, resume bool) error {
	p.attachment = nil
	initial, err := p.presentation(u)
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
	u.focus, u.split, u.diffOpen = layout.Focus, layout.Split, layout.DiffOpen
	if u.focus == 2 || u.focus == 1 && !u.diffOpen {
		u.focus = 0
	}
	u.main.interruptLocked = layout.InterruptLocked
	u.diff.navigation.Columns = layout.NavigatorColumns
	state.Presentation, err = p.presentation(u)
	if err != nil {
		return err
	}
	p.attachment.Restore(state.Snapshot)
	return nil
}

func (p *nativePreferences) save(u *terminalUI, now time.Time, flush bool) error {
	if p == nil || p.attachment == nil {
		return nil
	}
	state, err := p.presentation(u)
	if err != nil {
		return err
	}
	return p.attachment.Save(state, now, flush)
}

// Only authoritative start/resume results and settings notifications reach here.
func (u *appServerUI) retainAppliedSettings() {
	if u.panes == nil || u.panes.attachment == nil || u.model == "" || u.settings.restoreEffort {
		return
	}
	scope := preferences.Scope{Workspace: u.session.cwd, Thread: u.thread}
	if u.panes.attachment.Scope() != scope {
		if err := u.panes.open(u.shell, u.session.cwd, u.thread, false); err != nil {
			u.paneError(err)
			return
		}
	}
	u.panes.attachment.Apply(preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier, Observed: u.now()})
	u.paneError(u.panes.save(u.shell, u.now(), true))
}

func (u *appServerUI) paneError(err error) {
	if err == nil {
		return
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: "Pane layout or settings could not be restored or saved: " + err.Error(), Observed: time.Now()}}})
	u.dirty = true
}
