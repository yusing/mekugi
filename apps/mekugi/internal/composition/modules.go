package composition

import (
	"context"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/internal/router/toolplugin"
)

// Frontends is the application composition entry. Command implementations remain
// in the migration source until their feature extraction; the same registrations
// now reach the retained authenticated router through the Codex binding.
func Frontends(ctx context.Context) (*Runtime, error) {
	c := Composition{LegacyCommands: toolplugin.Builtins()}
	binder := new(binding.Registry)
	runtime, err := c.Activate(ctx, binder)
	if err != nil {
		return nil, err
	}
	runtime.Commands = binder.Commands()
	return runtime, nil
}
