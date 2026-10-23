package presentation

import (
	"strings"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

// MCPText keeps opaque host arguments and outcomes on the existing tool detail
// surface. Content blocks and structured results are not interpreted as commands.
func MCPText(item observation.Item) string {
	label := "MCP " + markdownsyntax.QuoteCode(item.Server) + " · " + markdownsyntax.QuoteCode(item.Tool)
	if item.Status == "failed" {
		label += " · failed"
	}
	if detail := MCPDetail(item); detail != "" {
		label += "\n" + markdownsyntax.SourceFence("json", detail)
	}
	return label
}

// MCPDetail preserves the host envelope independently of its transcript label.
func MCPDetail(item observation.Item) string {
	var fields []string
	for _, field := range []struct{ name, raw string }{
		{"arguments", string(item.Arguments)}, {"result", string(item.Result)}, {"error", string(item.Error)},
	} {
		if field.raw != "" && (field.name == "arguments" || field.raw != "null") {
			fields = append(fields, `"`+field.name+`": `+field.raw)
		}
	}
	if len(fields) > 0 {
		return "{\n" + strings.Join(fields, ",\n") + "\n}"
	}
	return ""
}
