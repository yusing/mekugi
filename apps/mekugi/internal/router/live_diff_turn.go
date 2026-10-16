package router

import json "encoding/json/v2"

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
		t.proxy.review.Preview().FinishTurn(t.directory, t.threadID, t.shellTurnID)
	}
}
