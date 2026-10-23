// Package binding exposes the Codex mechanisms implemented during extraction.
package binding

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/contracts/capability"
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

type Binder interface {
	Registrar
	Capability(capability.Mechanism) capability.Result
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

func (r *Registry) Capability(mechanism capability.Mechanism) capability.Result {
	if mechanism == capability.MCPTools {
		return capability.Result{State: capability.Available, Reason: "composed invocation-local MCP tools"}
	}
	if mechanism == capability.Observation {
		return capability.Result{State: capability.Available, Reason: "public transcript and reasoning observations"}
	}
	if mechanism == capability.Rollout {
		return capability.Result{State: capability.Available, Reason: "bounded offline rollout reader"}
	}
	if mechanism == capability.ShellApproval {
		return capability.Result{State: capability.Available, Reason: "composed shell approval policies; host hook availability checked at launch"}
	}
	if mechanism == capability.ExecutableFrontend {
		return capability.Result{State: capability.Available, Reason: "compiled frontend registration"}
	}
	if mechanism == capability.ThreadNaming {
		return capability.Result{State: capability.Available, Reason: "logical thread naming observations and operation admission"}
	}
	if mechanism == capability.GuidanceProjection {
		return capability.Result{State: capability.Available, Reason: "phased request guidance and frontend assembly"}
	}
	if mechanism == capability.InputProjection {
		return capability.Result{State: capability.Available, Reason: "request-local input projection"}
	}
	return capability.Result{State: capability.Unsupported, Reason: "mechanism has no implemented binding"}
}

func (r *Registry) Frontend(spec command.Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if slices.ContainsFunc(r.commands, func(old command.Spec) bool { return old.Name == spec.Name }) {
		return fmt.Errorf("duplicate frontend %q", spec.Name)
	}
	r.commands = append(r.commands, spec)
	return nil
}

func (r *Registry) Commands() []command.Spec { return slices.Clone(r.commands) }
