package router

import (
	"strings"
	"testing"
)

func TestSessionInspectionRecognizesJournalResult(t *testing.T) {
	item := map[string]any{"type": "function_call_output", "id": journalClientResultID("journal-1"),
		"name": "journal", "namespace": "functions", "output": `{"ok":true}`}
	path := writeInspectionSession(t, t.TempDir(), item)
	calls, err := readSessionInspection(t.Context(), path, nil)
	if err != nil || len(calls) != 1 || calls[0].item.CallID != "journal-1" || !calls[0].outputs[0].JournalResult {
		t.Fatalf("journal recognition: %+v %v", calls, err)
	}
	projected, err := inspectSessionCall(calls[0], replayRecord{}, false, "", 100)
	if err != nil || projected.Replay != "missing" || projected.Outcome != "unavailable" {
		t.Fatalf("%+v %v", projected, err)
	}
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { m["id"] = "fco_mekugi_journal_bad=" },
		func(m map[string]any) { m["name"] = "other" },
		func(m map[string]any) { m["namespace"] = "other" },
	} {
		other := map[string]any{}
		for k, v := range item {
			other[k] = v
		}
		mutate(other)
		_, err := readSessionInspection(t.Context(), writeInspectionSession(t, t.TempDir(), other), nil)
		if err == nil || !strings.Contains(err.Error(), "no call_id") {
			t.Fatalf("accepted malformed journal: %v", err)
		}
	}
}
