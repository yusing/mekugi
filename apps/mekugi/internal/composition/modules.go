package composition

import (
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/modules/conversation"

	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/livepreview"

	"github.com/yusing/mekugi/modules/notifications"

	"github.com/yusing/mekugi/modules/sessionpreferences"

	"github.com/yusing/mekugi/modules/diagnostics"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/agentmetrics"
	"github.com/yusing/mekugi/modules/boundedcommand"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/commandsegments"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/modules/outputdeduplication"
	"github.com/yusing/mekugi/modules/outputrecovery"
	"github.com/yusing/mekugi/modules/remotewriteapproval"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessioninspection"
	"github.com/yusing/mekugi/modules/sessionnaming"
	"github.com/yusing/mekugi/modules/sessionreplay"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/modules/sudoapproval"
	"github.com/yusing/mekugi/modules/symbolquery"
	"github.com/yusing/mekugi/modules/threadcontrol"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/modules/transportdiagnostics"
	"github.com/yusing/mekugi/modules/usage"
)

// Frontends composes feature-owned commands and records for the authenticated
// frontend host, including retained host facts and Activity snapshots.
func Frontends(ctx context.Context, liveReports, childReplySummary bool, captureDirectory string, threads *control.Service) (*Runtime, error) {
	c, bind := frontendDeclarations(liveReports, childReplySummary, captureDirectory, threads)
	runtime, err := c.Activate(ctx, new(binding.Registry))
	if err != nil {
		return nil, err
	}
	bind(runtime)
	return runtime, nil
}

// NativeRenderer borrows only statically selected painters. It constructs no
// history, capture, host, provider or guard owner.
func NativeRenderer(childReplySummary bool) (terminalsession.Views, error) {
	c, _ := frontendDeclarations(false, childReplySummary, "", nil)
	views, err := c.RendererViews()
	views.ChildReplySummary = childReplySummary
	return views, err
}

func frontendDeclarations(liveReports, childReplySummary bool, captureDirectory string, threads *control.Service) (*Composition, func(*Runtime)) {
	var c Composition
	conversation := c.Add(conversation.Module, func() conversation.Deps { return conversation.Deps{} })
	activity := c.Add(activity.Module, func() activity.Deps { return activity.Deps{} })
	notifications := c.Add(notifications.Module, func() notifications.Deps { return notifications.Deps{} })
	transport := c.Add(transportdiagnostics.Module, func() transportdiagnostics.Deps { return transportdiagnostics.Deps{} })
	metrics := c.Add(agentmetrics.Module, func() agentmetrics.Deps { return agentmetrics.Deps{} })
	history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	thread := c.Add(threadcontrol.Module, func() threadcontrol.Deps { return threadcontrol.Deps{History: history.Value(), Shared: threads} })
	replay := c.Add(sessionreplay.Module, func() sessionreplay.Deps {
		return sessionreplay.Deps{History: history.Value()}
	})
	prefs := c.Add(sessionpreferences.Module, func() sessionpreferences.Deps { return sessionpreferences.Deps{History: history.Value()} })
	usage := c.Add(usage.Module, func() usage.Deps { return usage.Deps{History: history.Value()} })
	changes := c.Add(changehistory.Module, func() changehistory.Deps {
		return changehistory.Deps{History: history.Value(), Directory: captureDirectory}
	})
	live := c.Add(livepreview.Module, func() livepreview.Deps { return livepreview.Deps{ReadEndpoint: capture.ReadFile} })
	segments := c.Add(commandsegments.Module, func() commandsegments.Deps { return commandsegments.Deps{History: history.Value(), Live: liveReports} })
	recovery := c.Add(outputrecovery.Module, func() outputrecovery.Deps { return outputrecovery.Deps{History: history.Value()} })
	c.Add(boundedcommand.Module, func() boundedcommand.Deps { return boundedcommand.Deps{} })
	c.Add(sourceoutline.Module, func() sourceoutline.Deps { return sourceoutline.Deps{} })
	c.Add(fileread.Module, func() fileread.Deps { return fileread.Deps{} })
	c.Add(symbolquery.Module, func() symbolquery.Deps { return symbolquery.Deps{} })
	c.Add(outputdeduplication.Module, func() outputdeduplication.Deps { return outputdeduplication.Deps{} })
	c.Add(agentguidance.Module, func() agentguidance.Deps { return agentguidance.Deps{ChildReplySummary: childReplySummary} })
	c.Add(sessionnaming.Module, func() sessionnaming.Deps { return sessionnaming.Deps{} })
	c.Add(remotewriteapproval.Module, func() remotewriteapproval.Deps { return remotewriteapproval.Deps{} })
	c.Add(sudoapproval.Module, func() sudoapproval.Deps { return sudoapproval.Deps{} })
	inspection := c.Add(sessioninspection.Module, func() sessioninspection.Deps { return sessioninspection.Deps{} })
	diagnosis := c.Add(diagnostics.Module, func() diagnostics.Deps {
		return diagnostics.Deps{History: history.Value(), Inspection: inspection.Value()}
	})
	return &c, func(runtime *Runtime) {
		runtime.Threads = thread.Value()
		runtime.Terminal.ChildReplySummary = childReplySummary
		runtime.Replay = replay.Value()
		runtime.Inspection = inspection.Value()
		runtime.TransportDiagnostics = transport.Value()
		runtime.Diagnostics = diagnosis.Value()
		runtime.AgentMetrics = metrics.Value()
		runtime.Usage = usage.Value()
		runtime.Changes = changes.Value()
		runtime.History = history.Value()
		runtime.Preferences = prefs.Value()
		runtime.Notifications = notifications.Value()
		runtime.Activity = activity.Value()
		runtime.Conversation = conversation.Value()
		runtime.LivePreview = live.Value()
		runtime.Recovery = recovery.Value()
		runtime.Segments = segments.Value()
	}
}
