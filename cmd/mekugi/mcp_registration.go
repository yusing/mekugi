package main

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/internal/router"
)

func mcpArgs(args []string, executable string, session router.Session) []string {
	if session.MCPSocket == "" {
		return args
	}
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	setting := fmt.Sprintf(`mcp_servers.mekugi={command=%q,args=["mcp-bridge",%q]}`, executable, session.MCPSocket)
	return slices.Insert(slices.Clone(args), index, "-c", setting)
}
