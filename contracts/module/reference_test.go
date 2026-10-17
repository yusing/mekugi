package module

import (
	"context"
	"encoding/json/jsontext"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/contracts/presentation"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/contracts/reference"
	"github.com/yusing/mekugi/contracts/replaytiming"
	"github.com/yusing/mekugi/contracts/scope"
)

func TestDescriptorReference(t *testing.T) {
	descriptor := Descriptor[struct{}, struct{}, NoBindings]{
		ID: "example", Summary: "Declared metadata", Requires: []ID{"dependency"},
		Lifetime: Invocation, Resources: []string{"retained store"},
		HostsCommands: true, HostsGuidance: true, HostsRecords: true, HostsReferences: true,
		HostsInspection: true, HostsDiagnosticSections: true, HostsReplayTiming: true,
		HostsActivityDetails: true,
		New:                  func(struct{}, Slots) (struct{}, error) { panic("New executed") },
		Start:                func(context.Context, struct{}) error { panic("Start executed") },
		Stop:                 func(context.Context, struct{}) error { panic("Stop executed") },
		Contributes: Contributions[struct{}]{
			Presentation: []presentation.Status{{ID: "example.status", Placements: []presentation.Placement{presentation.ComposerCaption}, Text: func(presentation.Thread) string { panic("status projection executed") }}},
			ActivityDetails: []activitydetail.Definition[struct{}]{{
				Name: "detail",
				Resolve: func(struct{}, *scope.Store, activitydetail.Request) activitydetail.Result {
					panic("ActivityDetails.Resolve executed")
				},
			}},
			Commands: []command.Definition[struct{}]{
				{Spec: command.Spec{Name: "reader", Description: "Read source"},
					Run: func(struct{}, context.Context, command.Request) (command.Output, error) { panic("Run executed") }},
				{Spec: command.Spec{Name: "writer", Description: "Write source"}},
			},
			Guidance: []guidance.Section{{Command: "reader", Text: "Private guidance prose"}},
			RecordKinds: []recordkind.Spec{
				{ID: "changes", Match: func(string) bool { panic("Match executed") },
					Dependencies: func(recordkind.Record) ([]string, error) { panic("record Dependencies executed") },
					Retirement: func(context.Context, recordkind.Record) (recordkind.RetirementPlanner, error) {
						panic("Retirement executed")
					}},
				{ID: "cursor"}, {ID: "output"},
			},
			References: []reference.Resolver{{
				Field:        "changes",
				Canonicalize: func([]byte, bool) ([]byte, error) { panic("Canonicalize executed") },
				Resolve: func(context.Context, reference.View, []byte) (command.OmittedOutput, error) {
					panic("Resolve executed")
				},
				Dependencies: func(scope.Retained, []byte) ([]string, error) { panic("reference Dependencies executed") },
			}},
			Inspection: []inspection.Definition[struct{}]{{
				NewAX: func(struct{}) inspection.AXCollector { panic("NewAX executed") },
				ProviderUsage: func(struct{}, context.Context, string, inspection.UsageFilter) (map[string]inspection.Usage, error) {
					panic("ProviderUsage executed")
				},
			}},
			DiagnosticSections: []diagnosticsection.Definition[struct{}]{{
				Transport: &diagnosticsection.Transport{
					Record:  func(map[string]jsontext.Value) diagnosticsection.Record { panic("Record executed") },
					Metrics: func(map[string]jsontext.Value) map[string]jsontext.Value { panic("Metrics executed") },
				},
				PrepareReads: func(struct{}, string, ...string) error { panic("PrepareReads executed") },
				ReadSnapshot: func(struct{}, context.Context, string) (diagnosticsection.Reads, error) {
					panic("ReadSnapshot executed")
				},
			}},
			ReplayTiming: []replaytiming.Definition[struct{}]{{
				Read: func(struct{}, context.Context, string, map[string]bool, func(replaytiming.Interval) error) error {
					panic("Read executed")
				},
			}},
		},
	}
	want := Reference{
		ID: "example", Summary: "Declared metadata", Requires: []ID{"dependency"},
		Lifetime: Invocation, Resources: []string{"retained store"},
		Contributes: []string{"commands", "guidance", "record-kinds", "references", "activity-details", "inspection", "diagnostic-sections", "replay-timing", "presentation"},
		Hosts:       []string{"commands", "guidance", "record-kinds", "references", "activity-details", "inspection", "diagnostic-sections", "replay-timing"},
		Commands:    []string{"reader", "writer"}, Records: []string{"changes", "cursor", "output"}, References: []string{"changes"},
	}
	got := descriptor.Reference()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reference() = %+v, want %+v", got, want)
	}
	got.Requires[0] = "changed"
	for _, values := range [][]string{got.Resources, got.Contributes, got.Hosts, got.Commands, got.Records, got.References} {
		values[0] = "changed"
	}
	if next := descriptor.Reference(); !reflect.DeepEqual(next, want) {
		t.Fatalf("reference mutation changed subsequent snapshot: %+v", next)
	}

	snapshot := descriptor.Reference()
	descriptor.Requires[0] = "different"
	descriptor.Resources[0] = "different"
	descriptor.Contributes.Commands[0].Name = "different"
	descriptor.Contributes.RecordKinds[0].ID = "different"
	descriptor.Contributes.References[0].Field = "different"
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("declaration mutation changed existing snapshot: %+v", snapshot)
	}
}

func TestDescriptorReferenceSlotsAreIndependent(t *testing.T) {
	descriptor := Descriptor[struct{}, struct{}, NoBindings]{
		HostsCommands: true,
		Contributes:   Contributions[struct{}]{Guidance: []guidance.Section{{Command: "reader"}}},
	}
	want := Reference{Contributes: []string{"guidance"}, Hosts: []string{"commands"}}
	if got := descriptor.Reference(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Reference() = %+v, want %+v", got, want)
	}

	descriptor = Descriptor[struct{}, struct{}, NoBindings]{HostsActivityDetails: true}
	want = Reference{Hosts: []string{"activity-details"}}
	if got := descriptor.Reference(); !reflect.DeepEqual(got, want) {
		t.Fatalf("host-only Reference() = %+v, want %+v", got, want)
	}
	descriptor = Descriptor[struct{}, struct{}, NoBindings]{
		Contributes: Contributions[struct{}]{ActivityDetails: []activitydetail.Definition[struct{}]{{Name: "detail"}}},
	}
	want = Reference{Contributes: []string{"activity-details"}}
	if got := descriptor.Reference(); !reflect.DeepEqual(got, want) {
		t.Fatalf("contributor-only Reference() = %+v, want %+v", got, want)
	}
}

func TestEmptyReferences(t *testing.T) {
	if got := (Descriptor[struct{}, struct{}, NoBindings]{}).Reference(); !reflect.DeepEqual(got, Reference{}) {
		t.Fatalf("zero descriptor Reference() = %+v", got)
	}
	if got := (NoBindings{}).Reference(); !reflect.DeepEqual(got, BindingReference{}) {
		t.Fatalf("NoBindings.Reference() = %+v", got)
	}
}
