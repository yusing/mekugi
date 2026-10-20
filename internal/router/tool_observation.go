package router

import (
	"strings"
)

// Tool observations are operational debug evidence, not capture metrics.
// Keep categories allowlisted and identities bounded; never accept payload text,
// credentials, arbitrary attributes, or errors through this seam.
type toolObservationTrace struct {
	debug     *debugOutput
	requestID string
	threadID  string
	sessionID string
}

func safeDiagnosticIdentity(value string) bool {
	return value != "" && len(value) <= 256 && !strings.Contains(value, "://") && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':' || r == '/')
	}) == -1
}

func (trace toolObservationTrace) toolCall(callID, tool string) {
	if trace.debug == nil || !safeDiagnosticIdentity(callID) || !safeDiagnosticIdentity(tool) {
		return
	}
	fields := map[string]any{"event": "tool_observation", "schema_version": 1, "call_id": callID, "tool": tool}
	for key, value := range map[string]string{"request_id": trace.requestID, "thread_id": trace.threadID, "session_id": trace.sessionID} {
		if safeDiagnosticIdentity(value) {
			fields[key] = value
		}
	}
	trace.debug.event(fields)
}
