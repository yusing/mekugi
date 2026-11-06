package executionobservation

import json "encoding/json/v2"

// Delivered confirms input boundaries and storage independently of painting.
// Transformation alone cannot release input or the turn's storage lease.
func (t *mekugiResponseTransform) Delivered(payload []byte) {
	t.deliveredGeneration(payload)
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
		t.owners.Review.Preview().FinishTurn(t.directory, t.threadID, t.shellTurnID)
	}
}
