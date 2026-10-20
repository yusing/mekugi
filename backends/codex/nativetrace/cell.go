package nativetrace

import (
	"path/filepath"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
)

// Source: apps/mekugi/internal/router/native_tool_trace.go:494:597@[d428f9ae] correlated tool evidence.

// Call contains decoded native invocation facts, not trace protocol payloads.
// LocalEnvironment is true only for a supported local command environment.
type Call struct {
	Tool, Input      string
	Command          commandanalysis.Command
	LocalEnvironment bool
	Session          string
}

// Ended reports whether the host ended the outer cell, independently of its tools.
func (c *Cell) Ended() bool { return c != nil && c.ended }

// Calls returns call inputs in host dispatch order, including repeated occurrences.
func (c *Cell) Calls() []Call {
	if c == nil {
		return nil
	}
	calls := make([]Call, 0, len(c.tools))
	for _, tool := range c.tools {
		calls = append(calls, Call{Tool: tool.Tool, Input: tool.input, Command: tool.command, LocalEnvironment: commandanalysis.LocalEnvironment(tool.environmentID), Session: tool.session})
	}
	return calls
}

// PatchPending distinguishes an observed unfinished patch from an absent call.
func (c *Cell) PatchPending(input string, occurrence int) bool {
	tool := c.patchCall(input, occurrence)
	return tool != nil && !tool.terminal
}

// patchCall selects one occurrence, so repeated identical inputs retain their
// own native identities and outcomes in host dispatch order.
func (c *Cell) patchCall(input string, occurrence int) *tool {
	if c == nil || occurrence < 0 {
		return nil
	}
	for _, tool := range c.tools {
		if tool.Tool != "apply_patch" || tool.input != input {
			continue
		}
		if occurrence == 0 {
			return tool
		}
		occurrence--
	}
	return nil
}

// Patch returns one terminal patch result only after its outer cell ended.
func (c *Cell) Patch(input string, occurrence int) (binding.ToolResult, bool) {
	tool := c.patchCall(input, occurrence)
	if tool == nil || !c.ended || !tool.terminal {
		return binding.ToolResult{}, false
	}
	return tool.ToolResult, true
}

// ExecCommands lists an ended cell's nested exec_command calls.
func (c *Cell) ExecCommands() []string {
	if c == nil || !c.ended {
		return nil
	}
	var calls []string
	for _, tool := range c.tools {
		if tool.Tool == "exec_command" && tool.CallID != "" {
			calls = append(calls, tool.CallID)
		}
	}
	return calls
}

// Pending reports an unfinished cell, patch, or yielded command.
func (c *Cell) Pending() bool {
	if c == nil {
		return false
	}
	for _, tool := range c.tools {
		if tool.Tool == "apply_patch" && !tool.terminal {
			return true
		}
		if tool.Tool == "exec_command" && !tool.terminal && tool.session != "" {
			return true
		}
	}
	return !c.ended
}

// Commands confirms each observed command by exactly one terminal host call.
// Any unobserved occurrence leaves the outcome unconfirmed.
func (c *Cell) Commands(commands []commandanalysis.Command, workspace string) ([]binding.ToolResult, bool) {
	if c == nil || !c.ended {
		return nil, false
	}
	normalize := func(command commandanalysis.Command) commandanalysis.Command {
		if command.Workdir == "" {
			command.Workdir = workspace
		} else if !filepath.IsAbs(command.Workdir) {
			command.Workdir = filepath.Join(workspace, command.Workdir)
		}
		command.Workdir = filepath.Clean(command.Workdir)
		return command
	}
	want := make(map[commandanalysis.Command]int)
	for _, command := range commands {
		want[normalize(command)]++
	}
	var results []binding.ToolResult
	for _, tool := range c.tools {
		if tool.Tool != "exec_command" {
			continue
		}
		if !commandanalysis.LocalEnvironment(tool.environmentID) {
			return nil, false
		}
		command := normalize(tool.command)
		if command.Shell == "" {
			for candidate, remaining := range want {
				if remaining > 0 && candidate.Command == command.Command && candidate.Workdir == command.Workdir {
					command.Shell = candidate.Shell
					break
				}
			}
		}
		if !tool.terminal || want[command] == 0 {
			return nil, false
		}
		want[command]--
		results = append(results, tool.ToolResult)
	}
	for _, remaining := range want {
		if remaining != 0 {
			return nil, false
		}
	}
	return results, len(results) != 0
}
