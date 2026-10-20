package main

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/router"
)

func TestMCPArgs(t *testing.T) {
	args := []string{"exec", "-c", `mcp_servers.other={command="other"}`, "--", "prompt"}
	if got := mcpArgs(args, "/path/mekugi", router.Session{}); !reflect.DeepEqual(got, args) {
		t.Fatalf("passthrough arguments changed: %q", got)
	}
	got := mcpArgs(args, "/path/mekugi", router.Session{MCPSocket: "/private/mcp.sock"})
	want := []string{"exec", "-c", `mcp_servers.other={command="other"}`, "-c", `mcp_servers.mekugi={command="/path/mekugi",args=["mcp-bridge","/private/mcp.sock"]}`, "--", "prompt"}
	if !reflect.DeepEqual(got, want) || args[3] != "--" {
		t.Fatalf("invocation override: %q; original: %q", got, args)
	}
}
