package composition

import (
	"context"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/internal/router"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/agentmetrics"
	"github.com/yusing/mekugi/modules/boundedcommand"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/commandsegments"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/modules/outputdeduplication"
	"github.com/yusing/mekugi/modules/outputrecovery"
	"github.com/yusing/mekugi/modules/remotewriteapproval"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionnaming"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/modules/sudoapproval"
	"github.com/yusing/mekugi/modules/symbolquery"
	"github.com/yusing/mekugi/modules/transportdiagnostics"
	"github.com/yusing/mekugi/modules/usage"
)

// Frontends composes feature-owned commands and records for the authenticated
// frontend host. Host observation records remain application contributions.
func Frontends(ctx context.Context, liveReports bool) (*Runtime, error) {
	c := Composition{LegacyRecordKinds: router.RetainedRecordKinds()}
	diagnostics := c.Add(transportdiagnostics.Module, func() transportdiagnostics.Deps { return transportdiagnostics.Deps{} })
	metrics := c.Add(agentmetrics.Module, func() agentmetrics.Deps { return agentmetrics.Deps{} })
	history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	usage := c.Add(usage.Module, func() usage.Deps { return usage.Deps{History: history.Value()} })
	changes := c.Add(changehistory.Module, func() changehistory.Deps { return changehistory.Deps{History: history.Value()} })
	segments := c.Add(commandsegments.Module, func() commandsegments.Deps { return commandsegments.Deps{History: history.Value(), Live: liveReports} })
	recovery := c.Add(outputrecovery.Module, func() outputrecovery.Deps { return outputrecovery.Deps{History: history.Value()} })
	c.Add(boundedcommand.Module, func() boundedcommand.Deps { return boundedcommand.Deps{} })
	c.Add(sourceoutline.Module, func() sourceoutline.Deps { return sourceoutline.Deps{} })
	c.Add(fileread.Module, func() fileread.Deps { return fileread.Deps{} })
	c.Add(symbolquery.Module, func() symbolquery.Deps { return symbolquery.Deps{} })
	c.Add(outputdeduplication.Module, func() outputdeduplication.Deps { return outputdeduplication.Deps{} })
	c.Add(agentguidance.Module, func() agentguidance.Deps { return agentguidance.Deps{} })
	c.Add(sessionnaming.Module, func() sessionnaming.Deps { return sessionnaming.Deps{} })
	c.Add(remotewriteapproval.Module, func() remotewriteapproval.Deps { return remotewriteapproval.Deps{} })
	c.Add(sudoapproval.Module, func() sudoapproval.Deps { return sudoapproval.Deps{} })
	binder := new(binding.Registry)
	runtime, err := c.Activate(ctx, binder)
	if err != nil {
		return nil, err
	}
	runtime.TransportDiagnostics = diagnostics.Value()
	runtime.AgentMetrics = metrics.Value()
	runtime.Usage = usage.Value()
	runtime.Changes = changes.Value()
	runtime.History = history.Value()
	runtime.Recovery = recovery.Value()
	runtime.Segments = segments.Value()
	return runtime, nil
}
