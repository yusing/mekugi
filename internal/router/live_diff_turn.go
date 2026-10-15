package router

import json "encoding/json/v2"

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

// Delivered confirms storage completion independently of final-answer painting.
// Transformation alone cannot release the turn's storage lease.
func (t *mekugiResponseTransform) Delivered(payload []byte) {
	if !t.storageCompletionReady && !t.liveDiffCompletionReady {
		return
	}
	var envelope struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.Type != "response.completed" && envelope.Status != "completed" {
		return
	}
	if t.storageCompletionReady {
		t.storageIdle = true
	}
	if t.liveDiffCompletionReady {
		t.proxy.autoLiveDiff.finishTurn(t.directory, t.threadID, t.shellTurnID)
	}
}
