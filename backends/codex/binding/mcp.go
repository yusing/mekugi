package binding

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPServer borrows a feature-owned SDK server and its invocation tool names.
type MCPServer struct {
	Name   string
	Server *mcp.Server
	Tools  []string
}

func (r *Registry) MCP(server MCPServer) error {
	if server.Name == "" || server.Server == nil || len(server.Tools) == 0 || strings.ContainsAny(server.Name, ".\"' \\[]=\t\r\n") {
		return errors.New("MCP contribution requires a bare server name, SDK server and tool names")
	}
	if slices.ContainsFunc(r.mcp, func(old MCPServer) bool { return old.Name == server.Name }) {
		return fmt.Errorf("duplicate MCP server %q", server.Name)
	}
	server.Tools = slices.Clone(server.Tools)
	r.mcp = append(r.mcp, server)
	return nil
}

func (r *Registry) RegisteredMCP() []MCPServer {
	servers := slices.Clone(r.mcp)
	for i := range servers {
		servers[i].Tools = slices.Clone(servers[i].Tools)
	}
	return servers
}
