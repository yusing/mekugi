package router

import (
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/vcsguard"
)

// Use one label table for native calls and normalized exec identifiers.
// Retain full arguments: auxiliary options are part of the observed operation too.
func toolActivityBuiltinLabel(name string) string {
	switch name {
	case "list_mcp_resources":
		return "List MCP resources"
	case "list_mcp_resource_templates":
		return "List MCP resource templates"
	case "read_mcp_resource":
		return "Read MCP resource"
	case "clock.curr_time", "clock__curr_time", "curr_time":
		return "Read current time"
	case "clock.sleep", "clock__sleep", "sleep":
		return "Sleep"
	case "get_context_remaining":
		return "Check remaining context"
	case "new_context":
		return "Start new context"
	case "web.run", "web__run":
		return "Browse web"
	case "image_gen.imagegen", "image_gen__imagegen":
		return "Generate image"
	case "wait":
		return "Wait for execution"
	}
	return ""
}

func toolActivityMCPName(name string) (server, tool string, ok bool) {
	qualified, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "", "", false
	}
	server, tool, ok = strings.Cut(qualified, "__")
	return server, tool, ok && server != "" && tool != ""
}

func toolActivityFenced(language, input string) string {
	fence := "```"
	for strings.Contains(input, fence) {
		fence += "`"
	}
	trailer := "\n"
	if strings.HasSuffix(input, "\n") {
		trailer = ""
	}
	return fence + language + "\n" + input + trailer + fence
}

func toolActivityShell(script string) string {
	return toolActivityShellLanguage(toolActivityShellSource(script), "bash")
}

func toolActivityShellSource(script string) string {
	return vcsguard.DisplayScript(script, func(directory string) bool {
		if !filepath.IsAbs(directory) || filepath.Base(directory) != vcsguard.Directory {
			return false
		}
		_, ok := toolRegistryIDFromDirectory(filepath.Dir(directory))
		return ok
	})
}
