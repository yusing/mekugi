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
	InputProjection(InputProjection) error
	Guidance(Guidance) error
	SessionNaming(SessionNaming) error
	ShellApproval(ShellApproval) error
}

// Registry validates and retains composition-ordered Codex connections.
type Registry struct {
	mcp          []MCPServer
	observations *Observation
	guards       []ShellApproval
	commands     []command.Spec
	projections  []InputProjection
	guidance     *Guidance
	naming       *SessionNaming
}

func (r *Registry) Frontend(spec command.Spec) error {
	r.commands = append(r.commands, spec)
	return nil
}

func (r *Registry) Commands() []command.Spec { return slices.Clone(r.commands) }
