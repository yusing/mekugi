package launch

import "github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

// Thread identity is a passive native observation, not an input admission or
// wait owner. Native admits human input atomically with turn/start or turn/steer.
type threadInput struct {
	admitted     bool
	caller, role string
}

// Side input locks retain the already-acquired composer payload until its one
// native request is dispatched or rejected. They do not reserve logical work.
func (p *executionPresentation) releaseSideInput(side *sessioncontrol.Side) {
	delete(p.sideInputs, side)
}
func (p *executionPresentation) releaseSideInputs() { clear(p.sideInputs) }
