package preview

import "slices"

type rootTurn struct{ workspace, thread, id string }

func (b *Session) Enable() {
	if b != nil {
		b.enabled.Store(true)
	}
}
func (b *Session) Disable() {
	if b != nil {
		b.enabled.Store(false)
	}
}
func (b *Session) Enabled() bool { return b != nil && b.enabled.Load() }

// SetScope consumes the application's admitted review membership. Scope inputs
// are copied so later caller mutation cannot alter preview authorization.
func (b *Session) SetScope(scope Scope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scope = cloneScope(scope)
	withdrawn := func(p Preview) bool { return !b.scope.Workspaces[p.Workspace][p.Thread] }
	b.previews = slices.DeleteFunc(b.previews, withdrawn)
	b.completedPreviews = slices.DeleteFunc(b.completedPreviews, withdrawn)
	if b.subscriber != nil {
		b.subscriber.previews = slices.DeleteFunc(b.subscriber.previews, func(event Event) bool {
			return event.Preview != nil && event.Preview.Workspace != "" && withdrawn(*event.Preview)
		})
	}
}

// BeginTurn starts provisional state only for an admitted root host turn.
func (b *Session) BeginTurn(workspace, thread, id string) {
	if !b.Enabled() || id == "" || workspace == "" || thread == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	turn := rootTurn{workspace, thread, id}
	if !b.Enabled() || !b.scope.Workspaces[workspace][thread] || b.turn == turn {
		return
	}
	b.turn, b.turnComplete = turn, false
	b.publishTurnLocked(true)
}

func (b *Session) FinishTurn(workspace, thread, id string) {
	if !b.Enabled() || id == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turnComplete || b.turn != (rootTurn{workspace, thread, id}) {
		return
	}
	b.turnComplete = true
	b.publishTurnLocked(false)
}
