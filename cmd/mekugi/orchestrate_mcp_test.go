package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router"
)

func TestOrchestrateMCPArgs(t *testing.T) {
	args := []string{"exec", "--", "prompt"}
	if got := orchestrateMCPArgs(args, "/mekugi", router.Session{}); !reflect.DeepEqual(got, args) {
		t.Fatalf("unavailable server changed arguments: %q", got)
	}
	got := orchestrateMCPArgs(args, "/mekugi", router.Session{OrchestrateMCPSocket: "/private/orchestrate.sock"})
	if len(got) != 5 || got[1] != "-c" || got[3] != "--" || args[1] != "--" {
		t.Fatalf("invocation arguments: %q; original %q", got, args)
	}
	for _, part := range []string{`mcp_servers.orchestrate=`, `args=["orchestrate-mcp","/private/orchestrate.sock"]`, `omit_tools_from=["direct"]`, `default_tools_approval_mode="approve"`} {
		if !strings.Contains(got[2], part) {
			t.Fatalf("registration missing %q: %s", part, got[2])
		}
	}
}
