package router

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These event_msg records match the persisted CommandExecution completion shape:
// execution endpoints are epoch milliseconds, while the envelope records write time.
func inspectCommandTiming(t *testing.T, events ...map[string]any) (sessionInspection, int, string) {
	t.Helper()
	root := t.TempDir()
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for i, event := range events {
		at := time.Date(2026, 9, 12, 12, 1, i, 0, time.UTC).Format(time.RFC3339Nano)
		if err := encoder.Encode(map[string]any{
			"type": "event_msg", "timestamp": at, "payload": event,
		}); err != nil {
			t.Fatal(err)
		}
	}
	session := filepath.Join(root, "rollout.jsonl")
	if err := os.WriteFile(session, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return inspectFixture(t, root, session, "--ax")
}

func commandTimingEvent(kind, id string, fields map[string]any) map[string]any {
	event := map[string]any{
		"type": kind, "thread_id": "thread", "turn_id": "turn",
		"item": map[string]any{
			"type": "CommandExecution", "id": id,
			"command": []string{"bash", "-lc", "# mekugi:ax:call_id=call-1\nfalse"},
			"cwd":     "/tmp", "parsed_cmd": []any{}, "source": "unified_exec_startup",
			"status": "failed", "exit_code": 1,
		},
	}
	maps.Copy(event, fields)
	return event
}

func TestSessionInspectionPersistedCommandTiming(t *testing.T) {
	start := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).UnixMilli()
	first := commandTimingEvent("item_completed", "command-1", map[string]any{
		"started_at_ms": start, "completed_at_ms": start + 125,
	})
	second := commandTimingEvent("item_completed", "command-2", map[string]any{
		"started_at_ms": start + 200, "completed_at_ms": start + 250,
	})
	second["item"].(map[string]any)["command"] = []string{"bash", "-lc", "env MEKUGI_AX_CALL_ID=call-1 false"}
	delete(second["item"].(map[string]any), "exit_code")
	result, code, stderr := inspectCommandTiming(t, first, second)
	if code != 0 || result.AX == nil {
		t.Fatalf("code %d: %s", code, stderr)
	}
	got := result.AX.Commands
	if got.Started != 2 || got.Completed != 2 || got.DurationMS != 175 || got.GapMS != 75 || got.UnpairedEvents != 0 {
		t.Fatalf("timing = %+v", got)
	}
	a, b := got.Commands[0], got.Commands[1]
	if a.StartedAt.UnixMilli() != start || a.CompletedAt.UnixMilli() != start+125 ||
		a.LogicalCallID != "call-1" || a.ExitCode == nil || *a.ExitCode != 1 ||
		b.LogicalCallID != "" || b.ExitCode != nil {
		t.Fatalf("execution evidence = %+v, %+v", a, b)
	}
}

func TestSessionInspectionCommandTimingMissingAndInvalid(t *testing.T) {
	const start int64 = 1789214400000
	for _, test := range []struct {
		name                                     string
		fields                                   map[string]any
		wantStarted, wantCompleted, wantUnpaired uint64
		wantError                                bool
	}{
		{"missing start", map[string]any{"completed_at_ms": start + 100}, 0, 1, 1, false},
		{"null start", map[string]any{"started_at_ms": nil, "completed_at_ms": start + 100}, 0, 1, 1, false},
		{"missing end", map[string]any{"started_at_ms": start}, 1, 0, 2, false},
		{"null end", map[string]any{"started_at_ms": start, "completed_at_ms": nil}, 1, 0, 2, false},
		{"reversed", map[string]any{"started_at_ms": start, "completed_at_ms": start - 1}, 1, 1, 1, false},
		{"no endpoints", nil, 0, 1, 1, false},
		{"string start", map[string]any{"started_at_ms": "1789214400000", "completed_at_ms": start}, 0, 0, 0, true},
		{"fractional end", map[string]any{"started_at_ms": start, "completed_at_ms": 1.5}, 0, 0, 0, true},
		{"overflow", map[string]any{"completed_at_ms": json.Number("9223372036854775808")}, 0, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, code, stderr := inspectCommandTiming(t, commandTimingEvent("item_completed", "command", test.fields))
			if test.wantError {
				if code == 0 {
					t.Fatal("invalid timestamp accepted")
				}
				return
			}
			if code != 0 || result.AX == nil {
				t.Fatalf("code %d: %s", code, stderr)
			}
			got := result.AX.Commands
			if got.Started != test.wantStarted || got.Completed != test.wantCompleted || got.UnpairedEvents != test.wantUnpaired ||
				got.DurationMS != 0 || got.GapMS != 0 || len(got.Commands) != 1 || got.Commands[0].DurationMS != nil {
				t.Fatalf("invented duration = %+v", got)
			}
		})
	}
}

func TestSessionInspectionCommandTimingDuplicateRepresentations(t *testing.T) {
	const start int64 = 1789214400000
	started := commandTimingEvent("item_started", "command", map[string]any{"started_at_ms": start})
	completed := commandTimingEvent("item_completed", "command", map[string]any{
		"started_at_ms": start, "completed_at_ms": start + 100,
	})
	result, code, stderr := inspectCommandTiming(t, started, completed, completed)
	if code != 0 || result.AX == nil {
		t.Fatalf("code %d: %s", code, stderr)
	}
	got := result.AX.Commands
	if got.Started != 1 || got.Completed != 1 || got.DurationMS != 100 || got.UnpairedEvents != 1 {
		t.Fatalf("double-counted timing = %+v", got)
	}
	conflicting := commandTimingEvent("item_completed", "command", map[string]any{
		"started_at_ms": start - 1, "completed_at_ms": start + 100,
	})
	if _, code, _ := inspectCommandTiming(t, started, conflicting); code == 0 {
		t.Fatal("conflicting embedded start accepted")
	}
}

func TestSessionInspectionLegacyCommandTiming(t *testing.T) {
	result, code, stderr := inspectCommandTiming(t,
		commandTimingEvent("item_started", "command", nil),
		commandTimingEvent("item_completed", "command", nil))
	if code != 0 || result.AX == nil {
		t.Fatalf("code %d: %s", code, stderr)
	}
	got := result.AX.Commands
	if got.Started != 1 || got.Completed != 1 || got.UnpairedEvents != 0 ||
		len(got.Commands) != 1 || got.Commands[0].DurationMS == nil || *got.Commands[0].DurationMS != 1000 {
		t.Fatalf("legacy pair = %+v", got)
	}
}

func TestSessionInspectionCommandTimingCompletionOrder(t *testing.T) {
	const base int64 = 1789214400000
	for _, test := range []struct {
		name    string
		extra   map[string]any
		wantGap int64
	}{
		{"overlapping late completion", commandTimingEvent("item_completed", "a", map[string]any{
			"started_at_ms": base, "completed_at_ms": base + 100,
		}), 0},
		{"incomplete earlier start", commandTimingEvent("item_started", "a", map[string]any{
			"started_at_ms": base,
		}), 0},
		{"incomplete intervening start", commandTimingEvent("item_started", "a", map[string]any{
			"started_at_ms": base + 35,
		}), 5},
		{"unmatched later completion", commandTimingEvent("item_completed", "a", map[string]any{
			"completed_at_ms": base + 100,
		}), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := commandTimingEvent("item_completed", "b", map[string]any{
				"started_at_ms": base + 20, "completed_at_ms": base + 30,
			})
			c := commandTimingEvent("item_completed", "c", map[string]any{
				"started_at_ms": base + 40, "completed_at_ms": base + 50,
			})
			result, code, stderr := inspectCommandTiming(t, b, c, test.extra)
			if code != 0 || result.AX == nil {
				t.Fatalf("code %d: %s", code, stderr)
			}
			got := result.AX.Commands
			if got.GapMS != test.wantGap || got.Commands[1].GapBeforeMS != nil {
				t.Fatalf("invented idle gap: %+v", got)
			}
		})
	}
}

func TestSessionInspectionCommandTimingPresentNullDoesNotBorrowEnvelope(t *testing.T) {
	const base int64 = 1789214400000
	for _, fields := range []map[string]any{
		{"started_at_ms": nil, "completed_at_ms": nil},
		{"started_at_ms": nil},
		{"completed_at_ms": nil},
		{"started_at_ms": base, "completed_at_ms": nil},
	} {
		started := commandTimingEvent("item_started", "command", map[string]any{"started_at_ms": base})
		completed := commandTimingEvent("item_completed", "command", fields)
		result, code, stderr := inspectCommandTiming(t, started, completed)
		if code != 0 || result.AX == nil {
			t.Fatalf("code %d: %s", code, stderr)
		}
		got := result.AX.Commands
		if got.Completed != 0 || got.DurationMS != 0 || got.Commands[0].CompletedAt != nil ||
			got.Commands[0].DurationMS != nil || got.Commands[0].ExitCode != nil {
			t.Fatalf("null endpoint became completion: %+v", got)
		}
	}
}

func TestSessionInspectionCommandTimingPresentNullStart(t *testing.T) {
	for _, fields := range []map[string]any{
		{"started_at_ms": nil},
		{"completed_at_ms": nil},
		{"started_at_ms": nil, "completed_at_ms": nil},
	} {
		result, code, stderr := inspectCommandTiming(t,
			commandTimingEvent("item_started", "command", fields),
			commandTimingEvent("item_completed", "command", nil))
		if code != 0 || result.AX == nil {
			t.Fatalf("code %d: %s", code, stderr)
		}
		got := result.AX.Commands
		if got.Started != 0 || got.DurationMS != 0 || got.Commands[0].StartedAt != nil ||
			got.Commands[0].DurationMS != nil {
			t.Fatalf("null start borrowed envelope: %+v", got)
		}
	}
}
