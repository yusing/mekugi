package launch

import (
	"slices"
	"strings"
)

// Each native process establishes its own informational capability. Inherited
// copies, including those from a tool shell or another backend, are discarded.
func codexEnvironmentCapability(inherited []string) []string {
	result := slices.DeleteFunc(slices.Clone(inherited), func(entry string) bool {
		return strings.HasPrefix(entry, "MEKUGI_ENVIRONMENT_FACTS=")
	})
	return append(result, "MEKUGI_ENVIRONMENT_FACTS=1")
}
