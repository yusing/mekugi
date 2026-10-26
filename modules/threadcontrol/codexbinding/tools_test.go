package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestMCPToolsValidateArgumentsBeforeAdmission(t *testing.T) {
	driver, err := NewDriver(control.New())
	if err != nil {
		t.Fatal(err)
	}
	contribution := Codex.MCP(driver)
	left, right := net.Pipe()
	server, err := contribution.Server.Connect(t.Context(), &mcp.IOTransport{Reader: left, Writer: left}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: right, Writer: right}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range catalog.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	expected := []string{"followup_task", "inspect", "interrupt_agent", "list_agents", "send_message", "spawn_agent", "wait_agent"}
	if !slices.Equal(names, expected) {
		t.Fatalf("catalog=%v", names)
	}
	for _, test := range []struct{ name, arguments string }{
		{"spawn_agent", `{"task_name":"child"}`},
		{"spawn_agent", `{"task_name":"child","message":"task","preload":{"sources":[{"unknown":"file"}]}}`},
		{"spawn_agent", `{"task_name":"child","message":"task","preload":{"sources":[{"path":"file","ranges":[[1]]}]}}`},
		{"list_agents", `{"unknown":true}`},
		{"wait_agent", `{"target":[]}`},
		{"wait_agent", `{"timeout_ms":1.5}`},
		{"wait_agent", `{"cancel_id":"wait"}`},
		{"send_message", `{"message":"hello"}`},
		{"followup_task", `{"target":"child","message":3}`},
		{"interrupt_agent", `{"target":"child","message":"extra"}`},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: jsontext.Value(test.arguments)})
		if err != nil || !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `validating "arguments"`) {
			t.Fatalf("%s(%s): result=%+v err=%v", test.name, test.arguments, result, err)
		}
	}
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_agents", Arguments: map[string]any{}})
	if err != nil || !result.IsError || result.Content[0].(*mcp.TextContent).Text != "incomplete host MCP identity" {
		t.Fatalf("missing identity: result=%+v err=%v", result, err)
	}
}

func TestControlActionReturnsCanonicalTargetForAlias(t *testing.T) {
	for _, tool := range []string{"send_message", "followup_task"} {
		t.Run(tool, func(t *testing.T) {
			driver, _, _, caller := reconciliationFixture(t)
			caller.Call = tool
			result, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"next assignment"}`), tool)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := result.(map[string]any)
			if !ok || got["id"] != caller.Call || got["state"] != "confirmed" || got["thread_id"] != "child" || got["task_name"] != "/root/child" {
				t.Fatalf("action acknowledgement = %#v; want confirmed action identifying child and /root/child", result)
			}
		})
	}
}

func TestWaitAgentKeepsRegistrationIdentityInternal(t *testing.T) {
	driver, _, _, caller := reconciliationFixture(t)
	caller.Call, caller.Item = "wait", "exec"
	result, err := driver.waitAgent(t.Context(), caller, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["id"] != nil || fields["all"] != nil || string(fields["state"]) != `"registered"` || string(fields["targets"]) != `["/root/child"]` {
		t.Fatalf("wait result: %s", payload)
	}
	member, _ := driver.control.Member(caller.Thread)
	if member.Wait == nil || member.Wait.ID != caller.Call || !member.Wait.All {
		t.Fatal("lost internal wait correlation")
	}
}
