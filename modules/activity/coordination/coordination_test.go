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
			Coordination{Action: "Follow-up", Kind: "assignment", Target: "/root/child", Thread: "thread-1", Message: "continue", State: "confirmed", Confirmed: true}},
		{"send_message", `{"target":"child","message":"update"}`, `{"id":"op-2","state":"confirmed","thread_id":"thread-1","task_name":"/root/child"}`,
			Coordination{Action: "Message", Kind: "reply", Target: "/root/child", Thread: "thread-1", Message: "update", State: "confirmed", Confirmed: true}},
		{"interrupt_agent", `{"target":"child"}`, `{"id":"op-3","state":"confirmed","thread_id":"thread-1","task_name":"/root/child"}`,
			Coordination{Action: "Interrupt", Kind: "progress", Target: "/root/child", Thread: "thread-1", State: "confirmed", Confirmed: true}},
		{"wait_agent", `{"target":["/root/a","/root/b"]}`, `{"id":"wait-1","state":"registered"}`,
			Coordination{Action: "Wait", Kind: "progress", State: "registered", Confirmed: true, Targets: []Target{{Name: "/root/a"}, {Name: "/root/b"}}}},
		{"list_agents", `{}`, `{"agents":[{"task_name":"/root/a"},{"task_name":"/root/b"}]}`,
			Coordination{Action: "List agents", Kind: "progress", State: "completed", Confirmed: true, Count: 2}},
		{"list_agents", `{}`, `{"agents":[]}`,
			Coordination{Action: "List agents", Kind: "progress", State: "completed", Confirmed: true}},
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
		{name: "wait missing identity", tool: "wait_agent", result: `{"state":"registered"}`},
		{name: "wait unknown disposition", tool: "wait_agent", result: `{"id":"wait-1","state":"confirmed"}`},
		{name: "list missing agents", tool: "list_agents", result: `{}`},
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
		{"implicit all", `{}`, `{"id":"w","state":"registered"}`, "registered", []Target{{Name: "All descendants"}}},
		{"explicit all", `{"target":"all"}`, `{"id":"w","state":"registered"}`, "registered", []Target{{Name: "All descendants"}}},
		{"completed event", `{"target":["child"]}`, `{"id":"w","state":"completed","event":{"task":"/root/child","status":"completed"}}`, "completed", []Target{{Name: "/root/child", Status: "completed"}}},
		{"timeout", `{"target":["child"]}`, `{"id":"w","state":"timeout"}`, "timeout", []Target{{Name: "child"}}},
		{"canceled", `{"cancel_id":"w"}`, `{"id":"w","state":"canceled"}`, "canceled", nil},
		{"paused", `{"target":["child"]}`, `{"id":"w","state":"paused"}`, "paused", []Target{{Name: "child"}}},
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
