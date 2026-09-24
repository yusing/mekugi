package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
)

const journalFinishHostCallsError = "journal finish cannot complete with host-dispatched calls; inspect their results, then retry finish in a response without host calls"

// A streamed finish can precede a host call. Do not publish its provisional
// success until the complete response has established who owns the next step.
func (t *mekugiResponseTransform) deferJournalFinishResult(result map[string]jsonv1.RawMessage) bool {
	var outcome struct {
		FinishRequested bool `json:"finish_requested"`
	}
	if json.Unmarshal([]byte(jsonString(result, "output")), &outcome) != nil || !outcome.FinishRequested {
		return false
	}
	if t.journalDeferredFinish == nil {
		t.journalDeferredFinish = make(map[string]bool)
	}
	t.journalDeferredFinish[jsonString(result, "call_id")] = true
	return true
}

func (t *mekugiResponseTransform) finishDeferredJournalResults() [][]byte {
	var events [][]byte
	for _, result := range t.journalResults {
		callID := jsonString(result, "call_id")
		if !t.journalDeferredFinish[callID] {
			continue
		}
		if t.journalClientCalls {
			var outcome map[string]jsonv1.RawMessage
			if json.Unmarshal([]byte(jsonString(result, "output")), &outcome) == nil {
				outcome["ok"] = mustMarshalJSON(false)
				outcome["error"] = mustMarshalJSON(journalFinishHostCallsError)
				delete(outcome, "finish_requested")
				result["output"] = mustMarshalJSON(string(mustMarshalJSON(outcome)))
			}
			t.journalFinishRequested = false
		}
		events = append(events, journalResultEvent(result))
		delete(t.journalDeferredFinish, callID)
	}
	return events
}
