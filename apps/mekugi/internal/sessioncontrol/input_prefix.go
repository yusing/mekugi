package sessioncontrol

import codexresponses "github.com/yusing/mekugi/backends/codex/responses"

type inputContext struct {
	session, workspace string
	prefix             *codexresponses.InputPrefix
	opening            bool
}

// CaptureInputPrefix borrows immutable context for a confirmed full-history
// import. The receiving runtime still admits its own identity and permissions.
func (o *Observations) CaptureInputPrefix(thread, workspace string) *codexresponses.InputPrefix {
	if o == nil || workspace == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if current := o.inputContexts[thread]; current != nil && current.workspace == workspace {
		return current.prefix.Fork()
	}
	return nil
}

func (o *Observations) AdmitInputPrefix(thread, workspace string, prefix *codexresponses.InputPrefix) {
	if o == nil || thread == "" || workspace == "" || prefix == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.inputContexts == nil {
		o.inputContexts = make(map[string]*inputContext)
	}
	if o.inputContexts[thread] == nil {
		o.inputContexts[thread] = &inputContext{workspace: workspace, prefix: prefix, opening: true}
	}
}

// InputPrefix selects the admitted native context's immutable initial layer.
// Session and workspace changes are fresh contexts, not routing cache matches.
func (o *Observations) InputPrefix(session, thread, workspace string) *codexresponses.InputPrefix {
	if o == nil {
		return new(codexresponses.InputPrefix)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.inputContexts == nil {
		o.inputContexts = make(map[string]*inputContext)
	}
	current := o.inputContexts[thread]
	if current == nil || current.workspace != workspace || current.session != "" && current.session != session {
		current = &inputContext{session: session, workspace: workspace, prefix: new(codexresponses.InputPrefix)}
		o.inputContexts[thread] = current
	}
	current.session = session
	return current.prefix
}

// ForkInputPrefix accepts confirmed native ancestry and workspace only. A child
// binds its own session/window on first request, with independent later resets.
func (o *Observations) ForkInputPrefix(source, thread, workspace string) {
	if o == nil || source == thread || source == "" || thread == "" || workspace == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	parent := o.inputContexts[source]
	if parent == nil || parent.workspace != workspace || o.inputContexts[thread] != nil {
		return
	}
	o.inputContexts[thread] = &inputContext{workspace: workspace, prefix: parent.prefix.Fork(), opening: true}
}

// RewindContext replaces native history while keeping the applicable initial
// context. It never revives a prefix already invalidated by a reset.
func (o *Observations) RewindContext(thread, workspace string) {
	if o == nil || thread == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.contextRevision == nil {
		o.contextRevision = make(map[string]uint64)
	}
	o.contextRevision[thread]++
	if current := o.inputContexts[thread]; current != nil {
		if workspace != "" && current.workspace == workspace {
			current.opening = true
		} else {
			delete(o.inputContexts, thread)
		}
	}
}

// PreparingContextReset retires an inherited context that has not yet bound its
// new native window. Already-bound contexts change only on confirmed reset.
func (o *Observations) PreparingContextReset(thread string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if current := o.inputContexts[thread]; current != nil && current.opening {
		delete(o.inputContexts, thread)
	}
}
