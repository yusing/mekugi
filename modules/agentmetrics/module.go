package agentmetrics

import (
	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

var Module = module.Descriptor[Deps, *evidence.Service, module.NoBindings, module.NoViews]{
	ID: "agentmetrics", Summary: "Measure reader invocations, observed edits and recorded host completion",
	Lifetime: module.Stateless, New: newService,
	Contributes: module.Contributions[*evidence.Service]{DiagnosticSections: []diagnosticsection.Definition[*evidence.Service]{{PrepareReads: (*evidence.Service).PrepareDiagnosticReads, ReadSnapshot: (*evidence.Service).DiagnosticReads}}, Inspection: []inspection.Definition[*evidence.Service]{{NewAX: func(service *evidence.Service) inspection.AXCollector { return service.NewInspection(nil) }}}},
}
