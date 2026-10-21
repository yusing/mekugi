package rollout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestInspectionCancellationAtSourceBoundaries(t *testing.T) {
	t.Run("empty canceled source", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result, err := Inspect(ctx, inspectionSource(t), nil)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, Inspection{}) {
			t.Fatalf("canceled empty source = %+v, %v", result, err)
		}
	})
	t.Run("final observer cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		path := inspectionSource(t, `{"type":"event_msg","payload":{"type":"turn_complete","turn_id":"turn"}}`)
		result, err := Inspect(ctx, path, func(InspectionEvent) error {
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, Inspection{}) {
			t.Fatalf("canceled final observer = %+v, %v", result, err)
		}
	})
}

func inspectionSource(t *testing.T, records ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selected.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectionOrderedCallsAndProvenance(t *testing.T) {
	path := inspectionSource(t,
		`{"type":"session_meta","payload":{"id":"child","session_id":"root","cwd":"/first","model":"alpha","source":{"kind":"fork"}}}`,
		`{"type":"response_item","timestamp":"output-first","payload":{"type":"function_call_output","call_id":"later","output":"early"}}`,
		`{"type":"response_item","timestamp":"orphan-time","payload":{"type":"custom_tool_call_output","call_id":"orphan","output":{"ok":true}}}`,
		`{"type":"turn_context","payload":{"id":"not-a-thread","cwd":"/second","model":"beta"}}`,
		`{"type":"response_item","timestamp":"call-time","payload":{"type":"function_call","call_id":"later","name":"exec","arguments":"{}","namespace":"functions"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"later","output":"late"}}`,
		`{"type":"response_item","payload":{"type":"function_call","call_id":"later","name":"exec","arguments":"{}","namespace":"functions"}}`,
		`{"type":"session_meta","payload":{"id":"child","cwd":"/third","model":"alpha","source":"cli"}}`,
		`{"type":"unknown","payload":42}`,
	)
	got, err := Inspect(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != "child" || got.Cwd != "/third" || string(got.Source) != `"cli"` || !reflect.DeepEqual(got.Models, []string{"alpha", "beta"}) {
		t.Fatalf("metadata: %+v", got.InspectionMetadata)
	}
	want := []Call{
		{Line: 5, Timestamp: "call-time", Model: "beta", Workspace: "/second", HasCall: true,
			Item:    ToolItem{Type: "function_call", CallID: "later", Name: "exec", Namespace: "functions", Arguments: "{}"},
			Outputs: []ToolItem{{Type: "function_call_output", CallID: "later", Output: []byte(`"early"`)}, {Type: "function_call_output", CallID: "later", Output: []byte(`"late"`)}}},
		{Line: 3, Timestamp: "orphan-time", CompletedTimestamp: "orphan-time", Model: "alpha", Workspace: "/first",
			Item:    ToolItem{Type: "custom_tool_call_output", CallID: "orphan", Output: []byte(`{"ok":true}`)},
			Outputs: []ToolItem{{Type: "custom_tool_call_output", CallID: "orphan", Output: []byte(`{"ok":true}`)}}},
	}
	if !reflect.DeepEqual(got.Calls, want) {
		t.Fatalf("calls:\n got %+v\nwant %+v", got.Calls, want)
	}
}

func TestInspectionConflictingIdentities(t *testing.T) {
	const call = `{"type":"response_item","payload":{"type":"custom_tool_call","call_id":"c","name":"tool","input":"active","arguments":"inactive"}}`
	for _, tc := range []struct{ name, record, want string }{
		{"inactive arguments", strings.Replace(call, `"inactive"`, `"changed"`, 1), "conflicting call identity"},
		{"active input", strings.Replace(call, `"active"`, `"changed"`, 1), "conflicting call identity"},
		{"tool name", strings.Replace(call, `"tool"`, `"other"`, 1), "conflicting call identity"},
		{"tool type", strings.Replace(call, `"custom_tool_call"`, `"function_call"`, 1), "conflicting call identity"},
		{"namespace", strings.Replace(call, `"name":"tool"`, `"name":"tool","namespace":"other"`, 1), "conflicting call identity"},
		{"workspace", `{"type":"turn_context","payload":{"cwd":"/other"}}` + "\n" + call, "conflicting call workspace"},
		{"thread", `{"type":"session_meta","payload":{"id":"other"}}`, "conflicting thread identities"},
		{"missing call id", `{"type":"response_item","payload":{"type":"function_call"}}`, "tool item has no call_id"},
		{"missing output id", `{"type":"response_item","payload":{"type":"function_call_output"}}`, "tool item has no call_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := inspectionSource(t, `{"type":"session_meta","payload":{"id":"thread","cwd":"/workspace"}}`, call, tc.record)
			got, err := Inspect(t.Context(), path, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !reflect.DeepEqual(got, Inspection{}) {
				t.Fatalf("Inspect = %+v, %v; want empty result and %q", got, err, tc.want)
			}
		})
	}
}

func TestInspectionHistoricalJSONV1Compatibility(t *testing.T) {
	// Duplicate scalar members overwrite, null leaves scalar values intact,
	// names match case-insensitively, and escaped lone surrogates become U+FFFD.
	path := inspectionSource(t,
		`{"TYPE":"session_meta","PAYLOAD":{"ID":"thread","cwd":"/old","cwd":"/new","cwd":null,"model":"\ud83d\ude80","source":"\ud800"}}`,
		`{"TYPE":"response_item","PAYLOAD":{"TYPE":"function_call","CALL_ID":"c","NAME":"old","name":"new","name":null,"ARGUMENTS":"\ud800"}}`,
		// Duplicate object members merge into the existing item, rather than
		// replacing it and losing the command type/identity.
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"i"},"item":{"exit_code":0}}}`,
	)
	var events []InspectionEvent
	got, err := Inspect(t.Context(), path, func(e InspectionEvent) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != "thread" || got.Cwd != "/new" || !reflect.DeepEqual(got.Models, []string{"🚀"}) || string(got.Source) != `"\ud800"` {
		t.Fatalf("historical metadata: %+v", got.InspectionMetadata)
	}
	if len(got.Calls) != 1 || got.Calls[0].Item.Name != "new" || got.Calls[0].Item.Arguments != "�" {
		t.Fatalf("historical tool item: %+v", got.Calls)
	}
	if len(events) != 1 || events[0].ItemID != "i" || events[0].ExitCode == nil || *events[0].ExitCode != 0 {
		t.Fatalf("duplicate item merge: %+v", events)
	}
}

func TestInspectionCommandEndpoints(t *testing.T) {
	recordTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		name, kind, endpoints string
		persisted             bool
		at, start, end        time.Time
	}{
		{"legacy start", "item_started", "", false, recordTime, time.Time{}, time.Time{}},
		{"legacy completion", "item_completed", "", false, recordTime, time.Time{}, time.Time{}},
		{"null start", "item_started", `,"started_at_ms":null`, true, time.Time{}, time.Time{}, time.Time{}},
		{"explicit start", "item_started", `,"started_at_ms":1000`, true, time.UnixMilli(1000), time.Time{}, time.Time{}},
		{"unknown completion", "item_completed", `,"started_at_ms":null,"completed_at_ms":null`, true, recordTime, time.Time{}, time.Time{}},
		{"missing completion", "item_completed", `,"started_at_ms":1000`, true, recordTime, time.UnixMilli(1000), time.Time{}},
		{"known completion", "item_completed", `,"started_at_ms":1000,"completed_at_ms":2500`, true, recordTime, time.UnixMilli(1000), time.UnixMilli(2500)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := inspectionSource(t, fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":%q,"turn_id":"turn","item":{"type":"CommandExecution","id":"item","exit_code":7,"command":["/bin/bash","-lc","# mekugi:ax:call_id=call\nprintf ok"]}%s}}`, recordTime.Format(time.RFC3339Nano), tc.kind, tc.endpoints))
			var events []InspectionEvent
			_, err := Inspect(t.Context(), path, func(e InspectionEvent) error { events = append(events, e); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("events: %+v", events)
			}
			e := events[0]
			if e.Kind != tc.kind || e.TurnID != "turn" || e.ItemID != "item" || e.CallID != "call" || e.ExitCode == nil || *e.ExitCode != 7 || e.PersistedTimes != tc.persisted || !e.At.Equal(tc.at) || !e.Started.Equal(tc.start) || !e.Completed.Equal(tc.end) {
				t.Fatalf("event: %+v; expected times %v, %v, %v persisted=%t", e, tc.at, tc.start, tc.end, tc.persisted)
			}
		})
	}
}

func TestInspectionOptionalObserver(t *testing.T) {
	for _, payload := range []string{
		`42`,
		`{"type":"item_completed","started_at_ms":"bad","item":{"type":"CommandExecution"}}`,
	} {
		path := inspectionSource(t, `{"type":"event_msg","payload":`+payload+`}`)
		if _, err := Inspect(t.Context(), path, nil); err != nil {
			t.Fatalf("nil observer interpreted event %s: %v", payload, err)
		}
		if _, err := Inspect(t.Context(), path, func(InspectionEvent) error { return nil }); err == nil {
			t.Fatalf("observer accepted malformed event %s", payload)
		}
	}
	t.Run("synchronous failure stops before next record", func(t *testing.T) {
		path := inspectionSource(t,
			`{"type":"event_msg","payload":{"type":"turn_started","turn_id":"turn"}}`,
			`invalid next line`,
		)
		want := errors.New("observer failed")
		count := 0
		got, err := Inspect(t.Context(), path, func(e InspectionEvent) error {
			count++
			if e.Kind != "turn_started" || e.TurnID != "turn" {
				t.Fatalf("event: %+v", e)
			}
			return want
		})
		if !errors.Is(err, want) || count != 1 || !reflect.DeepEqual(got, Inspection{}) {
			t.Fatalf("Inspect = %+v, %v; callbacks=%d", got, err, count)
		}
	})
}

func TestInspectionSourceValidationAndBounds(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"malformed JSON", `{"type":"event_msg","payload":`, "invalid JSON or UTF-8"},
		{"raw invalid UTF8", "{\"type\":\"unknown\",\"payload\":\"\xff\"}", "invalid JSON or UTF-8"},
		{"metadata", `{"type":"session_meta","payload":42}`, "invalid workspace metadata"},
		{"response", `{"type":"response_item","payload":42}`, "invalid response item"},
		{"line bound", `{"type":"unknown","payload":"` + strings.Repeat("x", 32<<20) + `"}`, "maximum line size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Inspect(t.Context(), inspectionSource(t, tc.data), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !reflect.DeepEqual(got, Inspection{}) {
				t.Fatalf("Inspect = %+v, %v; want %q", got, err, tc.want)
			}
		})
	}
	t.Run("regular file and size", func(t *testing.T) {
		if _, err := Inspect(t.Context(), t.TempDir(), nil); err == nil {
			t.Fatal("accepted directory")
		}
		path := inspectionSource(t)
		if err := os.Truncate(path, (64<<20)+1); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, nil); err == nil || !strings.Contains(err.Error(), "64 MiB") {
			t.Fatalf("oversized file: %v", err)
		}
	})
	t.Run("logical call bound", func(t *testing.T) {
		var records strings.Builder
		for i := range 10000 {
			fmt.Fprintf(&records, "{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call_output\",\"call_id\":\"%d\"}}\n", i)
		}
		path := inspectionSource(t, records.String())
		got, err := Inspect(t.Context(), path, nil)
		if err != nil || len(got.Calls) != 10000 {
			t.Fatalf("10000 calls: count=%d, %v", len(got.Calls), err)
		}
		records.WriteString(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"overflow"}}`)
		if err := os.WriteFile(path, []byte(records.String()), 0600); err != nil {
			t.Fatal(err)
		}
		got, err = Inspect(t.Context(), path, nil)
		if err == nil || !strings.Contains(err.Error(), "10000 logical calls") || !reflect.DeepEqual(got, Inspection{}) {
			t.Fatalf("overflow: %+v, %v", got, err)
		}
	})
	t.Run("selected source only", func(t *testing.T) {
		path := inspectionSource(t, `{"type":"session_meta","payload":{"id":"selected","cwd":"/unavailable/workspace"}}`)
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "other.jsonl"), []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := Inspect(t.Context(), path, nil)
		if err != nil || got.ThreadID != "selected" || got.Cwd != "/unavailable/workspace" {
			t.Fatalf("selected source: %+v, %v", got, err)
		}
	})
}
