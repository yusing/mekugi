package router

// Tool input generation ends before host execution. Keep this observation
// separate from previews, which can be disabled or incomplete.
func (t *mekugiResponseTransform) setToolInputEmission(itemID string, active bool) {
	if t.proxy == nil || t.threadID == "" || itemID == "" {
		return
	}
	p := t.proxy
	p.mu.Lock()
	defer p.mu.Unlock()
	if active {
		if p.toolInputEmissions == nil {
			p.toolInputEmissions = make(map[*mekugiResponseTransform]map[string]bool)
		}
		if p.toolInputEmissions[t] == nil {
			p.toolInputEmissions[t] = make(map[string]bool)
		}
		p.toolInputEmissions[t][itemID] = true
	} else {
		delete(p.toolInputEmissions[t], itemID)
		if len(p.toolInputEmissions[t]) == 0 {
			delete(p.toolInputEmissions, t)
		}
	}
}

func (t *mekugiResponseTransform) clearToolInputEmissions() {
	if t.proxy != nil {
		t.proxy.mu.Lock()
		delete(t.proxy.toolInputEmissions, t)
		t.proxy.mu.Unlock()
	}
}

func (p *mekugiProxy) emittingToolInput(workspace, thread string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for request := range p.toolInputEmissions {
		// Codex can omit cwd from provider input. Its native thread identity
		// still owns generation; no filesystem operand is resolved here.
		if request.threadID == thread && (request.directory == "" || request.directory == workspace) {
			return true
		}
	}
	return false
}
