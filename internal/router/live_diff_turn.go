package router

// Live view transitions belong to the root host turn, not a provider response,
// tool call, or child. The tuple also prevents late delivery from an older turn
// from switching a newer prompt back to diff.
type liveDiffTurn struct {
	workspace, thread, id string
}

func (a *autoLiveDiff) beginTurn(workspace, thread string, metadata codexTurnMetadata) {
	if a == nil || !a.enabled.Load() || metadata.activityIdentityInvalid ||
		metadata.RequestKind != "turn" || metadata.SubagentKind != "" ||
		metadata.TurnID == "" || workspace == "" || thread == "" ||
		metadata.ThreadID != "" && metadata.ThreadID != thread {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	turn := liveDiffTurn{workspace, thread, metadata.TurnID}
	if !a.enabled.Load() || !a.scope.Workspaces[workspace][thread] || a.turn == turn {
		return
	}
	a.turn, a.turnComplete = turn, false
	a.events.publishTurn(true)
}

func (a *autoLiveDiff) finishTurn(workspace, thread, turnID string) {
	if a == nil || !a.enabled.Load() || turnID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.turnComplete || a.turn != (liveDiffTurn{workspace, thread, turnID}) {
		return
	}
	a.turnComplete = true
	a.events.publishTurn(false)
}

// Delivered calls this only for an actual token notice or the successful journal
// terminal envelope, after the downstream transport has written and flushed it.
func (t *mekugiResponseTransform) finishLiveDiffTurn() {
	if !t.subagentTurn {
		t.proxy.autoLiveDiff.finishTurn(t.directory, t.threadID, t.shellTurnID)
	}
}
