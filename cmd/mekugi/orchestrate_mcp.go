package main

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/internal/router"
)

func orchestrateMCPArgs(args []string, executable string, session router.Session) []string {
	if session.OrchestrateMCPSocket == "" {
		return args
	}
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	setting := fmt.Sprintf(`mcp_servers.orchestrate={command=%q,args=["orchestrate-mcp",%q],omit_tools_from=["direct"],default_tools_approval_mode="approve"}`, executable, session.OrchestrateMCPSocket)
	return slices.Insert(slices.Clone(args), index, "-c", setting)
}
