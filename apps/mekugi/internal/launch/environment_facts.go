package launch

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/agentguidance/environment"
)

// Each native process establishes its own informational capability. Inherited
// copies, including those from a tool shell or another backend, are discarded.
func codexEnvironmentCapability(inherited []string, installed bool) []string {
	result := slices.DeleteFunc(slices.Clone(inherited), func(entry string) bool {
		return strings.HasPrefix(entry, environment.Capability+"=")
	})
	if installed {
		result = append(result, environment.Capability+"=1")
	}
	return result
}
