// Source: internal/router/tool_observation.go:17:23@b5162407.
package responses

import "strings"

// SafeDiagnosticIdentity accepts bounded operational identities, excluding URLs.
func SafeDiagnosticIdentity(value string) bool {
	return value != "" && len(value) <= 256 && !strings.Contains(value, "://") && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':' || r == '/')
	}) == -1
}
