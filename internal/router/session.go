package router

// Source: session.go:11:100 bounded session identity selection.

import (
	"net/http"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

const (
	sessionIDHeader = "session-id"

	maxSessionHistories = 256
	maxSessionTurns     = 128
)

func routingSessionID(headers http.Header, request parsedResponsesRequest) string {
	for _, value := range headers.Values(sessionIDHeader) {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if key := request.promptCacheKey(); key != "" {
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
