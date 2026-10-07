package claude

import (
	"fmt"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestAdapterNativeUsagePresenceAndCumulativeSnapshots(t *testing.T) {
	var a adapter
	for _, total := range []uint64{10, 15, 0} {
		// The latest result is a snapshot, not a delta. Optional fields stay nil.
		frame := `{"kind":"event","event":{"type":"result","modelUsage":{"sonnet":{"inputTokens":` + fmt.Sprint(total) + `,"outputTokens":0}},"total_cost_usd":0.02}}`
		got, err := a.decode([]byte(frame))
		if err != nil || len(got) != 2 || got[0].Kind != "done" || got[1].Usage == nil {
			t.Fatalf("events=%+v err=%v", got, err)
		}
		u := got[1].Usage
		m := u.Models["sonnet"]
		if m.Input == nil || *m.Input != total || m.Output == nil || *m.Output != 0 || m.CacheRead != nil || m.Thinking != nil || u.CostUSD == nil || *u.CostUSD != 0.02 {
			t.Fatalf("snapshot: %+v", u)
		}
	}
	for _, frame := range []string{
		`{"kind":"event","event":{"type":"result","is_error":true,"modelUsage":{},"total_cost_usd":0}}`,
		`{"kind":"event","event":{"type":"result","is_error":true,"startup_failure_reason":"cwd_unavailable","modelUsage":{"sonnet":{"inputTokens":0}},"total_cost_usd":0}}`,
	} {
		got, err := a.decode([]byte(frame))
		if err != nil || len(got) != 1 || got[0].Kind != "done" {
			t.Fatalf("zeroed startup/crash became usage: %+v %v", got, err)
		}
	}
}

func TestAdapterNativeLimitAndTaskEvents(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"event","event":{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","utilization":0.8,"resetsAt":1790985600}}}`, []session.Event{{Kind: "limit", Limit: &session.RateLimit{Status: "allowed_warning", Window: "five_hour", Utilization: new(0.8), ResetsAt: new(1790985600.0)}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"task_started","task_id":"t","tool_use_id":"tool","task_type":"local_agent","subagent_type":"reviewer","description":"Inspect"}}`, []session.Event{{Kind: "task", Role: "task_started", Task: &session.Task{ID: "t", ToolID: "tool", Kind: "local_agent", Role: "reviewer", Description: "Inspect", Status: "running"}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"task_progress","task_id":"t","description":"Inspect","summary":"Read two files"}}`, []session.Event{{Kind: "task", Role: "task_progress", Task: &session.Task{ID: "t", Description: "Inspect", Summary: "Read two files"}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"task_updated","task_id":"t","patch":{"status":"killed","error":"Stopped by user"}}}`, []session.Event{{Kind: "task", Role: "task_updated", Task: &session.Task{ID: "t", Status: "killed", Summary: "Stopped by user"}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"task_notification","task_id":"t","tool_use_id":"tool","status":"completed","summary":"Inspected"}}`, []session.Event{{Kind: "task", Role: "task_notification", Task: &session.Task{ID: "t", ToolID: "tool", Status: "completed", Summary: "Inspected"}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"task_started","task_id":"watcher","ambient":true}}`, []session.Event{{Kind: "task", Role: "task_started", Task: &session.Task{ID: "watcher", Status: "running", Ambient: true}}})
	assertDecode(t, &a, `{"kind":"task_control","id":"t","failed":true,"text":"not found"}`, []session.Event{{Kind: "task_control", ID: "t", Failed: true, Text: "not found"}})
}
