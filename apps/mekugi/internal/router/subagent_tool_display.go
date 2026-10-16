package router

import (
	"path/filepath"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

// toolActivityShellSource removes only authenticated invocation instrumentation.
// Activity receives the original visible source and never consults host registries.
func toolActivityShellSource(script string) string {
	return shellinstrumentation.DisplayScript(shellinstrumentation.ShOriginal(script), func(directory string) bool {
		if !filepath.IsAbs(directory) || !shellinstrumentation.IsGuardDirectory(directory) {
			return false
		}
		_, ok := frontends.RegistryIDFromDirectory(filepath.Dir(directory))
		return ok
	})
}
