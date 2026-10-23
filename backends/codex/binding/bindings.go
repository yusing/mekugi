package binding

import (
	"slices"

	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
)

// Bindings declares existing Codex connections. Factories receive the constructed
// service during registration; reference collection never invokes them.
type Bindings[S any] struct {
	Requires        []capability.Requirement
	Observation     func(S) Observation
	Frontends       bool // Exposes the descriptor's command contributions.
	Rollout         func(S, RolloutSource) error
	InputProjection func(S) InputProjection
	Guidance        func(S) Guidance
	SessionNaming   func(S) SessionNaming
	ShellApproval   func(S) ShellApproval
	MCP             func(S) MCPServer
}

func (b Bindings[S]) Register(r Registrar, service S, commands []command.Definition[S]) error {
	if b.MCP != nil {
		if err := r.MCP(b.MCP(service)); err != nil {
			return err
		}
	}
	if b.Frontends {
		for _, definition := range commands {
			if err := r.Frontend(definition.Spec); err != nil {
				return err
			}
		}
	}
	if b.Rollout != nil {
		if err := r.Rollout(func(source RolloutSource) error { return b.Rollout(service, source) }); err != nil {
			return err
		}
	}
	if b.InputProjection != nil {
		if err := r.InputProjection(b.InputProjection(service)); err != nil {
			return err
		}
	}
	if b.Guidance != nil {
		if err := r.Guidance(b.Guidance(service)); err != nil {
			return err
		}
	}
	if b.SessionNaming != nil {
		if err := r.SessionNaming(b.SessionNaming(service)); err != nil {
			return err
		}
	}
	if b.ShellApproval != nil {
		if err := r.ShellApproval(b.ShellApproval(service)); err != nil {
			return err
		}
	}
	if b.Observation != nil {
		if err := r.Observation(b.Observation(service)); err != nil {
			return err
		}
	}
	return nil
}

func (b Bindings[S]) Reference() module.BindingReference {
	r := module.BindingReference{Requires: slices.Clone(b.Requires)}
	for _, connection := range []struct {
		mechanism capability.Mechanism
		declared  bool
	}{
		{capability.ExecutableFrontend, b.Frontends},
		{capability.Rollout, b.Rollout != nil},
		{capability.InputProjection, b.InputProjection != nil},
		{capability.GuidanceProjection, b.Guidance != nil},
		{capability.ThreadNaming, b.SessionNaming != nil},
		{capability.ShellApproval, b.ShellApproval != nil},
		{capability.Observation, b.Observation != nil},
		{capability.MCPTools, b.MCP != nil},
	} {
		if connection.declared {
			r.Mechanisms = append(r.Mechanisms, connection.mechanism)
		}
	}
	return r
}
