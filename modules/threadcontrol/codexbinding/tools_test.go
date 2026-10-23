package codexbinding

import (
	"encoding/json/jsontext"
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
	expected := []string{"followup_task", "interrupt_agent", "list_agents", "send_message", "spawn_agent", "wait_agent"}
	if !slices.Equal(names, expected) {
		t.Fatalf("catalog=%v", names)
	}
	for _, test := range []struct{ name, arguments string }{
		{"spawn_agent", `{"task_name":"child"}`},
		{"list_agents", `{"unknown":true}`},
		{"wait_agent", `{"target":[]}`},
		{"wait_agent", `{"timeout_ms":1.5}`},
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
