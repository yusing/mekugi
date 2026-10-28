package launch

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestInvocationMCPComposedEndpoint(t *testing.T) {
	t.Parallel()
	calls := make(chan *mcp.CallToolRequest, 2)
	contribution := binding.MCPServer{Name: "threadcontrol", Server: mcp.NewServer(&mcp.Implementation{Name: "threadcontrol", Version: "1"}, nil), Tools: []string{"list_agents"}}
	tool := &mcp.Tool{Name: "list_agents", Description: "List admitted agents", InputSchema: jsontext.Value(`{"type":"object"}`)}
	mcp.AddTool(contribution.Server, tool, func(_ context.Context, call *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		calls <- call
		if args["fail"] == true {
			return nil, nil, errors.New("caller is not admitted")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"agents":[]}`}}}, nil, nil
	})
	bridge := filepath.Join(t.TempDir(), "bridge with spaces")
	args, stop, err := openInvocationMCP(t.Context(), []binding.MCPServer{contribution}, bridge)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	})
	if len(args) != 2 || args[0] != "-c" {
		t.Fatalf("overrides: %q", args)
	}
	var config struct {
		Servers map[string]struct {
			Command           string
			Args              []string
			Enabled, Required bool
			EnabledTools      []string `toml:"enabled_tools"`
			DisabledTools     []string `toml:"disabled_tools"`
			OmitToolsFrom     []string `toml:"omit_tools_from"`
			Approval          string   `toml:"default_tools_approval_mode"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(args[1], &config); err != nil {
		t.Fatal(err)
	}
	selected := config.Servers["threadcontrol"]
	if !reflect.DeepEqual(selected.OmitToolsFrom, []string{"deferred"}) {
		t.Fatalf("invocation tools must be declared before inference: %+v", selected)
	}
	if selected.Command != bridge || len(selected.Args) != 2 || selected.Args[0] != "mcp-bridge" || !selected.Enabled || !selected.Required || selected.Approval != "approve" || !reflect.DeepEqual(selected.EnabledTools, []string{"list_agents"}) || len(selected.DisabledTools) != 0 {
		t.Fatalf("invalid invocation configuration: %+v", selected)
	}
	socket := selected.Args[1]
	connection, err := (&net.Dialer{}).DialContext(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: connection, Writer: connection}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.ListTools(t.Context(), nil)
	if err != nil || len(catalog.Tools) != 1 || catalog.Tools[0].Name != "list_agents" || catalog.Tools[0].Description != tool.Description {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	metadata := mcp.Meta{"threadId": "root", "itemId": "exec-item", "x-codex-turn-metadata": map[string]any{"turn_id": "turn"}}
	expectedMetadata := maps.Clone(metadata)
	for _, fail := range []bool{false, true} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_agents", Meta: metadata, Arguments: map[string]any{"fail": fail}})
		if err != nil || result.IsError != fail {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		call := <-calls
		for key, value := range expectedMetadata {
			if !reflect.DeepEqual(call.Params.Meta[key], value) {
				t.Fatalf("host metadata %s changed: got %v want %v", key, call.Params.Meta[key], value)
			}
		}
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(socket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("endpoint survived shutdown: %v", err)
	}
}

func TestInvocationMCPEmptySlot(t *testing.T) {
	args, stop, err := openInvocationMCP(t.Context(), nil, "")
	if err != nil || len(args) != 0 || stop() != nil {
		t.Fatalf("empty slot: args=%v err=%v", args, err)
	}
}
