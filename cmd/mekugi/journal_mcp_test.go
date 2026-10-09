package main

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/router"
)

func TestJournalMCPArgs(t *testing.T) {
	args := []string{"exec", "-c", `mcp_servers.other={command="other"}`, "--", "prompt"}
	if got := journalMCPArgs(args, "/path/mekugi", router.Session{}); !reflect.DeepEqual(got, args) {
		t.Fatalf("passthrough arguments changed: %q", got)
	}
	got := journalMCPArgs(args, "/path/mekugi", router.Session{JournalMCPSocket: "/private/journal.sock"})
	want := []string{"exec", "-c", `mcp_servers.other={command="other"}`, "-c", `mcp_servers.mekugi={command="/path/mekugi",args=["journal-mcp","/private/journal.sock"]}`, "--", "prompt"}
	if !reflect.DeepEqual(got, want) || args[3] != "--" {
		t.Fatalf("invocation override: %q; original: %q", got, args)
	}
}
