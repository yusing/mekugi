package router

// Source: session.go:11:100 bounded session identity selection.

import (
	"net/http"
	"strings"
)

const (
	sessionIDHeader       = "session-id"
	threadIDHeader        = "thread-id"
	clientRequestIDHeader = "x-client-request-id"
	codexWindowIDHeader   = "x-codex-window-id"

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
	for _, value := range headers.Values(clientRequestIDHeader) {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func codexThreadID(headers http.Header) string {
	for _, value := range headers.Values(threadIDHeader) {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
