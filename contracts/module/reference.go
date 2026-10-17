package module

import (
	"slices"

	"github.com/yusing/mekugi/contracts/capability"
)

// Reference contains declaration metadata without executable callbacks.
type Reference struct {
	ID          ID
	Summary     string
	Requires    []ID
	Lifetime    Lifetime
	Resources   []string
	Contributes []string
	Hosts       []string
	Commands    []string
	Records     []string
	References  []string
}

// BindingReference keeps declared connections separate from prerequisites.
// Neither establishes availability in an installed host.
type BindingReference struct {
	Requires   []capability.Requirement
	Mechanisms []capability.Mechanism
}

func (NoBindings) Reference() BindingReference { return BindingReference{} }

// Reference reads declaration values without constructing a service or invoking
// lifecycle, contribution or binding callbacks.
func (d Descriptor[D, S, B]) Reference() Reference {
	r := Reference{
		ID: d.ID, Summary: d.Summary, Requires: slices.Clone(d.Requires),
		Lifetime: d.Lifetime, Resources: slices.Clone(d.Resources),
	}
	for _, slot := range []struct {
		name  string
		count int
		host  bool
	}{
		{"commands", len(d.Contributes.Commands), d.HostsCommands},
		{"guidance", len(d.Contributes.Guidance), d.HostsGuidance},
		{"record-kinds", len(d.Contributes.RecordKinds), d.HostsRecords},
		{"references", len(d.Contributes.References), d.HostsReferences},
		{"activity-details", len(d.Contributes.ActivityDetails), d.HostsActivityDetails},
		{"inspection", len(d.Contributes.Inspection), d.HostsInspection},
		{"diagnostic-sections", len(d.Contributes.DiagnosticSections), d.HostsDiagnosticSections},
		{"replay-timing", len(d.Contributes.ReplayTiming), d.HostsReplayTiming},
	} {
		if slot.count != 0 {
			r.Contributes = append(r.Contributes, slot.name)
		}
		if slot.host {
			r.Hosts = append(r.Hosts, slot.name)
		}
	}
	for _, definition := range d.Contributes.Commands {
		r.Commands = append(r.Commands, definition.Name)
	}
	for _, definition := range d.Contributes.RecordKinds {
		r.Records = append(r.Records, definition.ID)
	}
	for _, definition := range d.Contributes.References {
		r.References = append(r.References, definition.Field)
	}
	return r
}
