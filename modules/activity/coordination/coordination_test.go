package coordination

import (
	"encoding/json/jsontext"
	"reflect"
	"strconv"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func coordinationItem(tool, args, result string) observation.Item {
	return observation.Item{
		Type: "mcpToolCall", Server: "threadcontrol", Tool: tool, Status: "completed",
		Arguments: jsontext.Value(args),
		Result:    jsontext.Value(`{"content":[{"type":"text","text":` + strconv.Quote(result) + `}]}`),
	}
}

func TestProjectCoordinationRoutes(t *testing.T) {
	for _, tt := range []struct {
		tool, args, result string
		want               Coordination
	}{
		{"spawn_agent", `{"task_name":"child","message":"work"}`, `{"state":"confirmed","thread_id":"thread-1","task_name":"/root/child"}`,
			Coordination{Action: "Spawn", Kind: "start", Target: "/root/child", Thread: "thread-1", Message: "work", State: "confirmed", Confirmed: true}},
		{"followup_task", `{"target":"child","message":"continue"}`, `{"id":"op-1","state":"confirmed","thread_id":"thread-1","task_name":"/root/child"}`,
			Coordination{Action: "Follow-up", Kind: "assignment", Target: "/root/child", Thread: "thread-1", Message: "continue", State: "confirmed", ID: "op-1", Confirmed: true}},
		{"send_message", `{"target":"child","message":"update"}`, `{"id":"op-2","state":"confirmed","thread_id":"thread-1","task_name":"/root/child"}`,
			Coordination{Action: "Message", Kind: "reply", Target: "/root/child", Thread: "thread-1", Message: "update", State: "confirmed", ID: "op-2", Confirmed: true}},
		{"interrupt_agent", `{"target":"child"}`, `{"id":"op-3","state":"confirmed","thread_id":"thread-1","task_name":"/root/child"}`,
			Coordination{Action: "Interrupt", Kind: "progress", Target: "/root/child", Thread: "thread-1", State: "confirmed", ID: "op-3", Confirmed: true}},
		{"wait_agent", `{"target":["/root/a","/root/b"]}`, `{"state":"waiting"}`,
			Coordination{Action: "Wait", Kind: "progress", State: "waiting", Confirmed: true, Targets: []Target{{Name: "/root/a"}, {Name: "/root/b"}}}},
		{"list_agents", `{}`, `{"agents":[{"task_name":"/root/a"},{"task_name":"/root/b"}]}`,
			Coordination{Action: "List agents", Kind: "progress", State: "completed", Confirmed: true, Count: 2}},
		{"wait_agent", `{"wait_mode":"all"}`, `{"state":"completed","events":[{"task":"/root/a","status":"failed"},{"task":"/root/b","status":"completed"}]}`,
			Coordination{Action: "Wait", Kind: "progress", State: "completed", Confirmed: true, Targets: []Target{{Name: "/root/a", Status: "failed"}, {Name: "/root/b", Status: "completed"}}}},
		{"wait_agent", `{"wait_mode":"all"}`, `{"state":"completed","events":[{"task":"/root/a","status":"completed","kind":"delivery_failed"},{"task":"/root/b","status":"completed","kind":"completed"}]}`,
			Coordination{Action: "Wait", Kind: "progress", State: "completed", Confirmed: true, Targets: []Target{{Name: "/root/a", Status: "delivery failed"}, {Name: "/root/b", Status: "completed"}}}},
		{"compact", `{}`, `{"state":"scheduled"}`,
			Coordination{Action: "Compact", Kind: "progress", State: "scheduled", Confirmed: true}},
		{"wait_agent", `{"compact":true}`, `{"state":"scheduled"}`,
			Coordination{Action: "Wait", Kind: "progress", State: "scheduled", Confirmed: true, Targets: []Target{{Name: "all"}}}},
		{"list_agents", `{}`, `{"agents":[]}`,
			Coordination{Action: "List agents", Kind: "progress", State: "completed", Confirmed: true}},
		{"list_models", `{}`, `{"models":[{"model":"a"}]}`,
			Coordination{Action: "List models", Kind: "progress", State: "completed", Confirmed: true, Count: 1}},
		{"list_models", `{}`, `{"models":[]}`,
			Coordination{Action: "List models", Kind: "progress", State: "completed", Confirmed: true}},
		{"inspect", `{"target":"worker"}`, `{"thread_id":"thread-1","task_name":"/root/worker"}`,
			Coordination{Action: "Inspect", Kind: "progress", Target: "/root/worker", Thread: "thread-1", State: "completed", Confirmed: true}},
	} {
		t.Run(tt.tool+"/"+tt.result, func(t *testing.T) {
			got, ok := ProjectCoordination(coordinationItem(tt.tool, tt.args, tt.result))
			if !ok || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ProjectCoordination = %+v, %v; want %+v, true", got, ok, tt.want)
			}
		})
	}
}

func TestProjectCoordinationUnconfirmedTraffic(t *testing.T) {
	for _, tool := range []string{"spawn_agent", "followup_task", "send_message"} {
		for _, tt := range []struct {
			name, status, result, hostError, state, failure string
		}{
			{name: "pending", status: "inProgress", result: `{"id":"op","state":"confirmed","task_name":"/root/child","thread_id":"thread-1"}`, state: "pending"},
			{name: "uncertain", status: "completed", result: `{"id":"op","state":"uncertain","task_name":"/root/child","thread_id":"thread-1"}`, state: "uncertain"},
			{name: "missing identity", status: "completed", result: `{"state":"confirmed"}`, state: "unconfirmed"},
			{name: "opaque acknowledgement", status: "completed", result: `accepted`, state: "unconfirmed"},
			{name: "failed", status: "failed", result: `{}`, hostError: `{"message":"delivery rejected"}`, failure: "delivery rejected"},
		} {
			t.Run(tool+"/"+tt.name, func(t *testing.T) {
				item := coordinationItem(tool, `{"task_name":"child","target":"child","message":"work"}`, tt.result)
				item.Status, item.Error = tt.status, jsontext.Value(tt.hostError)
				got, ok := ProjectCoordination(item)
				if !ok || got.Confirmed || got.State != tt.state || got.Failure != tt.failure || got.Target != "child" || got.Thread != "" || got.Message != "work" {
					t.Fatalf("unconfirmed traffic = %+v, %v; want state %q, failure %q and requested target only", got, ok, tt.state, tt.failure)
				}
			})
		}
	}
}

func TestProjectCoordinationAcknowledgementGuards(t *testing.T) {
	for _, tt := range []struct {
		name, tool, args, result, envelope, hostError, failure string
	}{
		{name: "spawn missing thread", tool: "spawn_agent", result: `{"state":"confirmed","task_name":"/root/child"}`},
		{name: "spawn missing task", tool: "spawn_agent", result: `{"state":"confirmed","thread_id":"thread-1"}`},
		{name: "malformed arguments", tool: "send_message", args: `{broken`, result: `{"id":"op","state":"confirmed"}`},
		{name: "malformed envelope", tool: "send_message", envelope: `{broken`},
		{name: "ambiguous content", tool: "send_message", envelope: `{"content":[{"type":"text","text":"{}"},{"type":"text","text":"{}"}]}`},
		{name: "nontext content", tool: "send_message", envelope: `{"content":[{"type":"image"}]}`},
		{name: "MCP failure", tool: "send_message", envelope: `{"isError":true,"content":[{"type":"text","text":"denied"}]}`, failure: "denied"},
		{name: "host error", tool: "send_message", result: `{"id":"op","state":"confirmed"}`, hostError: `"host rejected"`, failure: "host rejected"},
		{name: "wait unknown disposition", tool: "wait_agent", result: `{"id":"wait-1","state":"confirmed"}`},
		{name: "list missing agents", tool: "list_agents", result: `{}`},
		{name: "models failed", tool: "list_models", envelope: `{"isError":true,"content":[{"type":"text","text":"catalog unavailable"}]}`, failure: "catalog unavailable"},
		{name: "inspect host error", tool: "inspect", result: `{}`, hostError: `"no such agent"`, failure: "no such agent"},
		{name: "models missing", tool: "list_models", result: `{}`},
		{name: "models null", tool: "list_models", result: `{"models":null}`},
		{name: "inspect missing identity", tool: "inspect", result: `{"thread_id":"thread-1"}`},
		{name: "list null agents", tool: "list_agents", result: `{"agents":null}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args
			if args == "" {
				args = `{}`
			}
			item := coordinationItem(tt.tool, args, tt.result)
			if tt.envelope != "" {
				item.Result = jsontext.Value(tt.envelope)
			}
			item.Error = jsontext.Value(tt.hostError)
			got, ok := ProjectCoordination(item)
			if !ok || got.Confirmed || got.Failure != tt.failure {
				t.Fatalf("acknowledgement = %+v, %v; want unconfirmed with failure %q", got, ok, tt.failure)
			}
		})
	}
}

func TestProjectCoordinationWaitDispositions(t *testing.T) {
	for _, tt := range []struct {
		name, args, result, state string
		targets                   []Target
	}{
		{"implicit all", `{}`, `{"state":"waiting","targets":["/root/a","/root/b"]}`, "waiting", []Target{{Name: "all"}}},
		{"explicit all", `{"target":"all"}`, `{"state":"waiting","targets":["/root/a","/root/b"]}`, "waiting", []Target{{Name: "all"}}},
		{"all failed target", `{}`, `{"state":"completed","event":{"task":"/root/child","status":"failed"}}`, "completed", []Target{{Name: "all", Status: "child: failed"}}},
		{"completed event", `{"target":["child"]}`, `{"state":"completed","event":{"task":"/root/child","status":"completed"}}`, "completed", []Target{{Name: "/root/child", Status: "completed"}}},
		{"timeout", `{"target":["child"]}`, `{"state":"timeout"}`, "timeout", []Target{{Name: "child"}}},
		{"paused", `{"target":["child"]}`, `{"state":"paused"}`, "paused", []Target{{Name: "child"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ProjectCoordination(coordinationItem("wait_agent", tt.args, tt.result))
			want := Coordination{Action: "Wait", Kind: "progress", State: tt.state, Confirmed: true, Targets: tt.targets}
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("wait = %+v, %v; want %+v, true", got, ok, want)
			}
		})
	}
}

func TestProjectCoordinationLeavesOtherToolsGeneric(t *testing.T) {
	for _, tt := range []struct{ itemType, server, tool string }{
		{"mcpToolCall", "other", "spawn_agent"},
		{"mcpToolCall", "threadcontrol", "other"},
		{"mcpToolCall", "threadcontrol", "spawn_agent_extra"},
		{"functionCall", "threadcontrol", "spawn_agent"},
	} {
		t.Run(tt.itemType+"/"+tt.server+"/"+tt.tool, func(t *testing.T) {
			item := coordinationItem(tt.tool, `{}`, `{}`)
			item.Type, item.Server = tt.itemType, tt.server
			if got, ok := ProjectCoordination(item); ok || !reflect.DeepEqual(got, Coordination{}) {
				t.Fatalf("generic tool projected as %+v, %v", got, ok)
			}
		})
	}
}
