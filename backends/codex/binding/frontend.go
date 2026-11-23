// Package binding exposes the Codex mechanisms implemented during extraction.
package binding

import (
	"slices"

	"github.com/yusing/mekugi/contracts/command"
)

type Registrar interface {
	MCP(MCPServer) error
	Observation(Observation) error
	Rollout(func(RolloutSource) error) error
	Frontend(command.Spec) error

	SessionNaming(SessionNaming) error
}

// Registry validates and retains composition-ordered Codex connections.
type Registry struct {
	mcp          []MCPServer
	observations *Observation
	commands     []command.Spec

	naming *SessionNaming
}

func (r *Registry) Frontend(spec command.Spec) error {
	r.commands = append(r.commands, spec)
	return nil
}

func (r *Registry) Commands() []command.Spec { return slices.Clone(r.commands) }
