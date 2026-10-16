package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessioninspection/codexbinding"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
	"github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type sessionInspection struct {
	query.Result
	AX *evidence.InspectionReport `json:"ax,omitempty"`
}
type sessionCorpus = query.Corpus

func testInspector(metrics *evidence.Service, diagnostics *capture.Service) *query.Service {
	var c inspection.Contribution
	if metrics != nil {
		c.NewAX = func() inspection.AXCollector { return metrics.NewInspection(nil) }
	}
	if diagnostics != nil {
		c.ProviderUsage = diagnostics.InspectUsage
	}
	var contributions []inspection.Contribution
	if c.NewAX != nil || c.ProviderUsage != nil {
		contributions = append(contributions, c)
	}
	service, err := query.New(contributions)
	if err != nil {
		panic(err)
	}
	if err := codexbinding.Bind(new(binding.Registry), service); err != nil {
		panic(err)
	}
	return service
}

func testDiagnostics(metrics *evidence.Service, transport *capture.Service) *diagnostic.Service {
	var c diagnosticsection.Contribution
	if metrics != nil {
		c.PrepareReads = metrics.PrepareDiagnosticReads
		c.ReadSnapshot = metrics.DiagnosticReads
	}
	if transport != nil {
		c.Transport = &diagnosticsection.Transport{Record: capture.DiagnosticRecord, Metrics: capture.DiagnosticMetrics}
	}
	var sections []diagnosticsection.Contribution
	if c.PrepareReads != nil || c.Transport != nil {
		sections = append(sections, c)
	}
	service, err := diagnostic.New(testHistory(), testInspector(metrics, transport), sections)
	if err != nil {
		panic(err)
	}
	return service
}
