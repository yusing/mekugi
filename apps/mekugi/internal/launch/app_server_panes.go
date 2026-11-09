package launch

import (
	"encoding/json/jsontext"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

type nativePreferences struct {
	service    *preferences.Service
	attachment *preferences.Attachment
	encoding   terminalsession.PreferenceEncoder
	writer     nativePreferenceWriter
}

type nativePreferenceSnapshot struct {
	preferences.Snapshot
	layout terminalsession.Preferences
}

func (p *nativePreferences) read(workspace, thread string) (nativePreferenceSnapshot, error) {
	scope := preferences.Scope{Workspace: workspace, Thread: thread}
	var state preferences.Snapshot
	var err error
	retained := p.writer.activeOwner
	for _, owner := range p.writer.outgoing {
		if owner.Scope() == scope {
			retained = owner
		}
	}
	if retained != nil && retained.Scope() == scope {
		state = retained.Snapshot()
	} else {
		state, err = p.service.Read(scope)
	}
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

func (p *nativePreferences) open(workspace, thread string, resume bool) error {
	var initial jsontext.Value
	if p.attachment != nil {
		initial = p.attachment.Snapshot().Presentation
		p.writer.outgoing = append(p.writer.outgoing, p.attachment)
	}
	p.attachment = nil
	var err error
	p.attachment, err = p.service.Attach(preferences.Scope{Workspace: workspace, Thread: thread}, initial)
	if err != nil || !resume {
		return err
	}
	state, err := p.read(workspace, thread)
	if err != nil || state.Presentation == nil {
		return err
	}
	p.attachment.Restore(state.Snapshot)
	return nil
}

// Only authoritative start/resume results and settings notifications reach here.
func (p *nativePreferences) retainAppliedSettings(scope preferences.Scope, restoreEffort bool, applied preferences.Applied) error {
	if p == nil || p.attachment == nil || applied.Model == "" || restoreEffort {
		return nil
	}
	if p.attachment.Scope() != scope {
		if err := p.open(scope.Workspace, scope.Thread, false); err != nil {
			return err
		}
	}
	p.attachment.Apply(applied)
	return p.savePrepared(applied.Observed, true)
}

func paneError(source *sessionpresentation.ActivitySource, err error) bool {
	if err == nil {
		return false
	}
	source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: source.LastSeq + 1, Agent: "Session", Kind: "text", Text: "Pane layout or settings could not be restored or saved: " + err.Error(), Observed: time.Now()}}})
	return true
}
