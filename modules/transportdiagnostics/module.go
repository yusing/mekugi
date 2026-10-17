package transportdiagnostics

import (
	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/replaytiming"
	"github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

var Module = module.Descriptor[Deps, *capture.Service, module.NoBindings, module.NoViews]{
	ID: "transportdiagnostics", Summary: "Observe transport bytes, timing, capture health and provider-response evidence",
	Lifetime: module.Invocation, New: newService,
	Contributes: module.Contributions[*capture.Service]{ReplayTiming: []replaytiming.Definition[*capture.Service]{{Read: (*capture.Service).ReplayTiming}}, DiagnosticSections: []diagnosticsection.Definition[*capture.Service]{{Transport: &diagnosticsection.Transport{Record: capture.DiagnosticRecord, Metrics: capture.DiagnosticMetrics}}}, Inspection: []inspection.Definition[*capture.Service]{{ProviderUsage: (*capture.Service).InspectUsage}}},
}
