package sessionpresentation

import "github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

type InterruptIntent struct {
	Version Version
	Target  sessioncontrol.InterruptTarget
	Mode    sessioncontrol.InterruptMode
}

// Interrupt validates the retained view and exact work identity before any
// control mutation. Displaying a child never changes Main's execution target.
func (m *Model) Interrupt(intent InterruptIntent, control *sessioncontrol.Live, unsentSteer bool) (sessioncontrol.Interrupt, error) {
	if intent.Version != m.view.Version || intent.Target != control.InterruptTarget() ||
		intent.Mode != sessioncontrol.RestoreWaiting && intent.Mode != sessioncontrol.ExpediteSteers {
		return sessioncontrol.Interrupt{}, ErrStaleIntent
	}
	if m.replay {
		return sessioncontrol.Interrupt{}, ErrReplayIntent
	}
	return control.RequestInterrupt(intent.Mode, unsentSteer), nil
}
