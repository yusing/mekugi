package main

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/internal/router"
)

func journalMCPArgs(args []string, executable string, session router.Session) []string {
	if session.JournalMCPSocket == "" {
		return args
	}
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	setting := fmt.Sprintf(`mcp_servers.mekugi={command=%q,args=["journal-mcp",%q]}`, executable, session.JournalMCPSocket)
	return slices.Insert(slices.Clone(args), index, "-c", setting)
}
