package router

// Source: session.go:11:100 bounded session identity selection.

import (
	"net/http"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

const (
	sessionIDHeader = "session-id"
)

func routingSessionID(headers http.Header, request codexresponses.Request) string {
	for _, value := range headers.Values(sessionIDHeader) {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if key := request.PromptCacheKey(); key != "" {
		return key
	}
	for _, value := range headers.Values(codexresponses.ClientRequestIDHeader) {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func codexThreadID(headers http.Header) string {
	for _, value := range headers.Values(codexresponses.ThreadIDHeader) {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
