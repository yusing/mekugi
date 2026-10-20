// Source: apps/mekugi/internal/router/subagent_tool_display.go@[d6415ec1].
package frontends

import (
	"path/filepath"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

// ShellDisplaySource removes only authenticated invocation instrumentation.
// Activity receives the original visible source and never consults host registries.
func ShellDisplaySource(script string) string {
	return shellinstrumentation.DisplayScript(shellinstrumentation.ShOriginal(script), func(directory string) bool {
		if !filepath.IsAbs(directory) || !shellinstrumentation.IsGuardDirectory(directory) {
			return false
		}
		_, ok := RegistryIDFromDirectory(filepath.Dir(directory))
		return ok
	})
}
