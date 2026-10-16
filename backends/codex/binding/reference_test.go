package binding

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
)

func TestBindingsReference(t *testing.T) {
	bindings := Bindings[struct{}]{
		Requires:        []capability.Requirement{{Mechanism: capability.Rollout, Required: true}},
		Frontends:       []command.Spec{{Name: "reader"}, {Name: "writer"}},
		Rollout:         func(struct{}, RolloutSource) error { panic("Rollout executed") },
		InputProjection: func(struct{}) InputProjection { panic("InputProjection executed") },
		Guidance:        func(struct{}) Guidance { panic("Guidance executed") },
		SessionNaming:   func(struct{}) SessionNaming { panic("SessionNaming executed") },
		ShellApproval:   func(struct{}) ShellApproval { panic("ShellApproval executed") },
		Observation:     func(struct{}) Observation { panic("Observation executed") },
	}
	want := module.BindingReference{
		Requires: []capability.Requirement{{Mechanism: capability.Rollout, Required: true}},
		Mechanisms: []capability.Mechanism{
			capability.ExecutableFrontend, capability.Rollout, capability.InputProjection,
			capability.GuidanceProjection, capability.ThreadNaming, capability.ShellApproval, capability.Observation,
		},
	}
	got := bindings.Reference()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reference() = %+v, want %+v", got, want)
	}
	got.Requires[0].Required = false
	got.Mechanisms[0] = "changed"
	if next := bindings.Reference(); !reflect.DeepEqual(next, want) {
		t.Fatalf("reference mutation changed subsequent snapshot: %+v", next)
	}
	snapshot := bindings.Reference()
	bindings.Requires[0].Required = false
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("declaration mutation changed existing snapshot: %+v", snapshot)
	}

	for _, tc := range []struct {
		name     string
		bindings Bindings[struct{}]
		want     module.BindingReference
	}{
		{name: "empty"},
		{name: "requirements only", bindings: Bindings[struct{}]{Requires: want.Requires}, want: module.BindingReference{Requires: want.Requires}},
		{name: "frontend", bindings: Bindings[struct{}]{Frontends: bindings.Frontends}, want: module.BindingReference{Mechanisms: []capability.Mechanism{capability.ExecutableFrontend}}},
		{name: "rollout", bindings: Bindings[struct{}]{Rollout: bindings.Rollout}, want: module.BindingReference{Mechanisms: []capability.Mechanism{capability.Rollout}}},
		{name: "projection", bindings: Bindings[struct{}]{InputProjection: bindings.InputProjection}, want: module.BindingReference{Mechanisms: []capability.Mechanism{capability.InputProjection}}},
		{name: "guidance", bindings: Bindings[struct{}]{Guidance: bindings.Guidance}, want: module.BindingReference{Mechanisms: []capability.Mechanism{capability.GuidanceProjection}}},
		{name: "naming", bindings: Bindings[struct{}]{SessionNaming: bindings.SessionNaming}, want: module.BindingReference{Mechanisms: []capability.Mechanism{capability.ThreadNaming}}},
		{name: "observation", bindings: Bindings[struct{}]{Observation: bindings.Observation}, want: module.BindingReference{Mechanisms: []capability.Mechanism{capability.Observation}}},
		{name: "approval", bindings: Bindings[struct{}]{ShellApproval: bindings.ShellApproval}, want: module.BindingReference{Mechanisms: []capability.Mechanism{capability.ShellApproval}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.bindings.Reference(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Reference() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBindingsRegister(t *testing.T) {
	wantCalls := []capability.Mechanism{
		capability.ExecutableFrontend, capability.ExecutableFrontend, capability.Rollout,
		capability.InputProjection, capability.GuidanceProjection, capability.ThreadNaming, capability.ShellApproval, capability.Observation,
	}
	for failAt := 0; failAt <= len(wantCalls); failAt++ {
		name := "success"
		if failAt > 0 {
			name = string(wantCalls[failAt-1])
		}
		t.Run(name, func(t *testing.T) {
			service := new(int)
			var factories []capability.Mechanism
			checkService := func(got *int, mechanism capability.Mechanism) {
				t.Helper()
				if got != service {
					t.Fatalf("%s factory received service %p, want %p", mechanism, got, service)
				}
				factories = append(factories, mechanism)
			}
			bindings := Bindings[*int]{
				Frontends: []command.Spec{{Name: "reader", Description: "Read"}, {Name: "writer", Description: "Write"}},
				Rollout: func(got *int, source RolloutSource) error {
					checkService(got, capability.Rollout)
					identity, err := source.Identity("fixture")
					if err != nil || identity != "fixture identity" {
						t.Fatalf("rollout source identity = %q, %v", identity, err)
					}
					return nil
				},
				InputProjection: func(got *int) InputProjection {
					checkService(got, capability.InputProjection)
					return InputProjection{Name: "projection"}
				},
				Guidance: func(got *int) Guidance {
					checkService(got, capability.GuidanceProjection)
					return Guidance{Frontends: func([]command.Spec) (string, error) { return "guide", nil }}
				},
				SessionNaming: func(got *int) SessionNaming {
					checkService(got, capability.ThreadNaming)
					return SessionNaming{NewThread: func(string, bool) ThreadNaming { return nil }}
				},
				Observation: func(got *int) Observation {
					checkService(got, capability.Observation)
					return Observation{NewTranscript: func(*scope.Store) observation.Transcript { return nil }}
				},
				ShellApproval: func(got *int) ShellApproval {
					checkService(got, capability.ShellApproval)
					return ShellApproval{Kind: Sudo}
				},
			}
			registrar := &referenceRegistrar{failAt: failAt, failure: errors.New("registration failed")}
			err := bindings.Register(registrar, service)
			if failAt != 0 {
				if !errors.Is(err, registrar.failure) {
					t.Fatalf("Register() = %v, want %v", err, registrar.failure)
				}
				if !slices.Equal(registrar.calls, wantCalls[:failAt]) {
					t.Fatalf("calls after failure = %v, want %v", registrar.calls, wantCalls[:failAt])
				}
				var wantFactories []capability.Mechanism
				if failAt > 3 {
					wantFactories = wantCalls[2:failAt]
				}
				if !slices.Equal(factories, wantFactories) {
					t.Fatalf("factories after failure = %v, want %v", factories, wantFactories)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(registrar.calls, wantCalls) || !slices.Equal(factories, wantCalls[2:]) {
				t.Fatalf("registration calls = %v, factories = %v", registrar.calls, factories)
			}
			if got := slices.Compact(slices.Clone(registrar.calls)); !slices.Equal(got, bindings.Reference().Mechanisms) {
				t.Fatalf("registered mechanisms = %v, reference = %v", got, bindings.Reference().Mechanisms)
			}
			if !slices.Equal(registrar.frontends, bindings.Frontends) || registrar.projection.Name != "projection" || registrar.naming.NewThread == nil || registrar.approval.Kind != Sudo {
				t.Fatalf("registered values differ: %+v", registrar)
			}
			if registrar.guidance.Frontends == nil {
				t.Fatal("guidance factory value was not registered")
			}
			if guide, err := registrar.guidance.Frontends(nil); err != nil || guide != "guide" {
				t.Fatalf("registered guidance = %q, %v", guide, err)
			}
		})
	}
}

func TestEmptyBindingsRegister(t *testing.T) {
	registrar := new(referenceRegistrar)
	if err := (Bindings[struct{}]{}).Register(registrar, struct{}{}); err != nil || len(registrar.calls) != 0 {
		t.Fatalf("empty Register() = %v, calls = %v", err, registrar.calls)
	}
}

func TestBindingsRegisterRolloutError(t *testing.T) {
	failure := errors.New("rollout attachment failed")
	bindings := Bindings[struct{}]{
		Rollout:         func(struct{}, RolloutSource) error { return failure },
		InputProjection: func(struct{}) InputProjection { panic("registered after rollout failure") },
	}
	registrar := new(referenceRegistrar)
	if err := bindings.Register(registrar, struct{}{}); !errors.Is(err, failure) {
		t.Fatalf("Register() = %v, want %v", err, failure)
	}
	if !slices.Equal(registrar.calls, []capability.Mechanism{capability.Rollout}) {
		t.Fatalf("registrations after rollout failure = %v", registrar.calls)
	}
}

type referenceRegistrar struct {
	calls      []capability.Mechanism
	failAt     int
	failure    error
	frontends  []command.Spec
	projection InputProjection
	guidance   Guidance
	naming     SessionNaming
	approval   ShellApproval
}

func (r *referenceRegistrar) record(mechanism capability.Mechanism) error {
	r.calls = append(r.calls, mechanism)
	if len(r.calls) == r.failAt {
		return r.failure
	}
	return nil
}

func (r *referenceRegistrar) Frontend(spec command.Spec) error {
	r.frontends = append(r.frontends, spec)
	return r.record(capability.ExecutableFrontend)
}

func (r *referenceRegistrar) Rollout(attach func(RolloutSource) error) error {
	if err := r.record(capability.Rollout); err != nil {
		return err
	}
	return attach(RolloutSource{Identity: func(path string) (string, error) { return path + " identity", nil }})
}

func (r *referenceRegistrar) InputProjection(projection InputProjection) error {
	r.projection = projection
	return r.record(capability.InputProjection)
}

func (r *referenceRegistrar) Guidance(guidance Guidance) error {
	r.guidance = guidance
	return r.record(capability.GuidanceProjection)
}

func (r *referenceRegistrar) SessionNaming(naming SessionNaming) error {
	r.naming = naming
	return r.record(capability.ThreadNaming)
}

func (r *referenceRegistrar) ShellApproval(approval ShellApproval) error {
	r.approval = approval
	return r.record(capability.ShellApproval)
}

func (r *referenceRegistrar) Observation(Observation) error { return r.record(capability.Observation) }
