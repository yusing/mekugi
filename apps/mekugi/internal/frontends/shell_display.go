// Source: apps/mekugi/internal/router/subagent_tool_display.go@[d6415ec1].
package frontends

import (
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

// ShellDisplaySource removes segment-tracking shell envelopes so Activity
// receives the original visible command source.
func ShellDisplaySource(script string) string {
	return shellinstrumentation.ShOriginal(script)
}
