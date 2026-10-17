package responses

import (
	"net/http"
	"strings"
)

// Source: apps/mekugi/internal/router/session.go:12:40@b9679c93.
const (
	RoutingSessionHeader = "session-id"
)

func RoutingSessionID(headers http.Header, request Request) string {
	for _, value := range headers.Values(RoutingSessionHeader) {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if key := request.PromptCacheKey(); key != "" {
		return key
	}
	for _, value := range headers.Values(ClientRequestIDHeader) {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ThreadID(headers http.Header) string {
	for _, value := range headers.Values(ThreadIDHeader) {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
