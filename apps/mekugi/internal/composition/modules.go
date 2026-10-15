package composition

import (
	"context"

	"github.com/yusing/mekugi/modules/outputdeduplication"

	"github.com/yusing/mekugi/modules/changehistory"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/internal/router"
	"github.com/yusing/mekugi/modules/boundedcommand"
	"github.com/yusing/mekugi/modules/commandsegments"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/modules/outputrecovery"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/modules/symbolquery"
)

// Frontends composes feature-owned commands and records for the authenticated
// frontend host. Host observation records remain application contributions.
func Frontends(ctx context.Context, liveReports bool) (*Runtime, error) {
	c := Composition{LegacyRecordKinds: router.RetainedRecordKinds()}
	history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	changes := c.Add(changehistory.Module, func() changehistory.Deps { return changehistory.Deps{History: history.Value()} })
	segments := c.Add(commandsegments.Module, func() commandsegments.Deps { return commandsegments.Deps{History: history.Value(), Live: liveReports} })
	recovery := c.Add(outputrecovery.Module, func() outputrecovery.Deps { return outputrecovery.Deps{History: history.Value()} })
	c.Add(boundedcommand.Module, func() boundedcommand.Deps { return boundedcommand.Deps{} })
	c.Add(sourceoutline.Module, func() sourceoutline.Deps { return sourceoutline.Deps{} })
	c.Add(fileread.Module, func() fileread.Deps { return fileread.Deps{} })
	c.Add(symbolquery.Module, func() symbolquery.Deps { return symbolquery.Deps{} })
	c.Add(outputdeduplication.Module, func() outputdeduplication.Deps { return outputdeduplication.Deps{} })
	binder := new(binding.Registry)
	runtime, err := c.Activate(ctx, binder)
	if err != nil {
		return nil, err
	}
	runtime.Changes = changes.Value()
	runtime.History = history.Value()
	runtime.Recovery = recovery.Value()
	runtime.Segments = segments.Value()
	return runtime, nil
}
