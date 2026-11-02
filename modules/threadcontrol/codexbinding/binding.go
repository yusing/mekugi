package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
)

type Bindings = binding.Bindings[*Driver]

var Codex = Bindings{
	MCP: (*Driver).mcp,
}
