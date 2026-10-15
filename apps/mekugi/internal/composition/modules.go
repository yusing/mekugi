package composition

import (
	"context"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/internal/router"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/modules/boundedcommand"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sourceoutline"
)

// Frontends is the application composition entry. Command implementations remain
// in the migration source until their feature extraction; the same registrations
// now reach the retained authenticated router through the Codex binding.
func Frontends(ctx context.Context) (*Runtime, error) {
	c := Composition{LegacyCommands: toolplugin.Builtins(), LegacyRecordKinds: router.RetainedRecordKinds()}
	history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	c.Add(boundedcommand.Module, func() boundedcommand.Deps { return boundedcommand.Deps{} })
	c.Add(sourceoutline.Module, func() sourceoutline.Deps { return sourceoutline.Deps{} })
	binder := new(binding.Registry)
	runtime, err := c.Activate(ctx, binder)
	if err != nil {
		return nil, err
	}
	runtime.History = history.Value()
	return runtime, nil
}
