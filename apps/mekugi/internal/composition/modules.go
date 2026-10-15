package composition

import (
	"context"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/internal/router"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/modules/boundedcommand"
	"github.com/yusing/mekugi/modules/commandsegments"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/modules/outputrecovery"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/modules/symbolquery"
)

// Frontends is the application composition entry. Command implementations remain
// in the migration source until their feature extraction; the same registrations
// now reach the retained authenticated router through the Codex binding.
func Frontends(ctx context.Context) (*Runtime, error) {
	c := Composition{LegacyCommands: toolplugin.Builtins(), LegacyRecordKinds: router.RetainedRecordKinds(), LegacyReferences: router.ChangeReferences()}
	history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	segments := c.Add(commandsegments.Module, func() commandsegments.Deps { return commandsegments.Deps{History: history.Value()} })
	recovery := c.Add(outputrecovery.Module, func() outputrecovery.Deps { return outputrecovery.Deps{History: history.Value()} })
	c.Add(boundedcommand.Module, func() boundedcommand.Deps { return boundedcommand.Deps{} })
	c.Add(sourceoutline.Module, func() sourceoutline.Deps { return sourceoutline.Deps{} })
	c.Add(fileread.Module, func() fileread.Deps { return fileread.Deps{} })
	c.Add(symbolquery.Module, func() symbolquery.Deps { return symbolquery.Deps{} })
	binder := new(binding.Registry)
	runtime, err := c.Activate(ctx, binder)
	if err != nil {
		return nil, err
	}
	runtime.History = history.Value()
	runtime.Recovery = recovery.Value()
	runtime.Segments = segments.Value()
	return runtime, nil
}
