package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
)

type Bindings = binding.Bindings[*Driver]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.MCPTools, Required: true}},
	MCP:      (*Driver).mcp,
}
