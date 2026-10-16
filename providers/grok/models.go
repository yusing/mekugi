package grok

import "strings"

// Source: apps/mekugi/internal/router/grok_catalog.go:15:15@[8877886e].
// Models returns supported inference IDs in catalog priority order.
// The caller owns the returned slice.
func Models() []string {
	return []string{"grok-4.7", "grok-4.6", "grok-4.5", "grok-4.7-build-fast"}
}

// Source: apps/mekugi/internal/router/grok_client.go:92:94@[8877886e].
// IsModel recognizes Grok namespaces, including unsupported model IDs.
func IsModel(model string) bool {
	return strings.HasPrefix(model, "grok:") || strings.HasPrefix(model, "grok-")
}
