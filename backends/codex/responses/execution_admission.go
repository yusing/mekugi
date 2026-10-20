package responses

import (
	"regexp"
	"strings"
)

// Source: internal/router/request.go:40:125@06fc40e4 execution admission.

// ExecutionFree identifies catalogs that need no editing or
// process-execution projection. Other tools and output schemas remain Codex-owned.
func ExecutionFree(r *Request) bool {
	// Admission precedes explicit instruction omission, which can remove input items.
	catalog := DecodeToolCatalog(r.Fields)
	if catalog.InputError != nil {
		return false
	}
	var execSeen, waitSeen bool
	var acceptSection func(*ToolSection, bool) bool
	acceptSection = func(section *ToolSection, namespaces bool) bool {
		if section == nil || section.Err != nil {
			return false
		}
		seen := make(map[string]bool)
		for _, tool := range section.Tools {
			if !tool.IsObject() {
				return false
			}
			if tool.Name() == "" || seen[tool.Name()] {
				return false
			}
			seen[tool.Name()] = true
			if tool.Type() == "namespace" {
				if !namespaces || tool.Nested == nil || !tool.Nested.Array || !acceptSection(tool.Nested, false) {
					return false
				}
				continue
			}
			if tool.Nested != nil || (tool.Type() != "function" && tool.Type() != "custom") {
				return false
			}
			switch tool.Name() {
			case "apply_patch", "exec_command", "functions.exec":
				return false
			case "exec":
				if execSeen || tool.Type() != "custom" || !isExecutionFreeCodeModeDescription(tool.Description()) {
					return false
				}
				execSeen = true
			case "wait":
				if waitSeen || tool.Type() != "function" {
					return false
				}
				waitSeen = true
			}
		}
		return true
	}
	if !acceptSection(catalog.Top, true) {
		return false
	}
	for _, group := range catalog.Additional {
		if !group.Tools.Array || !acceptSection(group.Tools, true) {
			return false
		}
	}
	return !waitSeen || execSeen
}

// Both clients advertise nested tools through headings; the App also exposes
// TypeScript declarations. Match declarations, not examples such as
// `await tools.exec_command(...)`, and do not depend on the client's preamble.
var codeModeExecutionDeclaration = regexp.MustCompile(`(?:^|[;{])\s*(apply_patch|exec_command)\s*\(`)

func isExecutionFreeCodeModeDescription(description string) bool {
	return !CodeModeDeclaresExecutionTool(description, "apply_patch") && !CodeModeDeclaresExecutionTool(description, "exec_command")
}

func CodeModeDeclaresExecutionTool(description, name string) bool {
	for line := range strings.SplitSeq(description, "\n") {
		heading := strings.Fields(line)
		if len(heading) < 2 || heading[0] != "###" {
			continue
		}
		if strings.Trim(heading[1], "`") == name {
			return true
		}
	}
	for _, declaration := range codeModeExecutionDeclaration.FindAllStringSubmatch(description, -1) {
		if declaration[1] == name {
			return true
		}
	}
	return false
}
