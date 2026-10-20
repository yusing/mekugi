// Source: apps/mekugi/internal/router/tool_observation.go:1:28@b9679c93.
package requestexecution

import (
	modelresponses "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Tool observations are operational debug evidence, not capture metrics.
// Keep categories allowlisted and identities bounded; never accept payload text,
// credentials, arbitrary attributes, or errors through this seam.
type Trace struct {
	debug     *Debug
	requestID string
	threadID  string
	sessionID string
}

func (trace Trace) ToolCall(callID, tool string) {
	if trace.debug == nil || !modelresponses.SafeDiagnosticIdentity(callID) || !modelresponses.SafeDiagnosticIdentity(tool) {
		return
	}
	fields := map[string]any{"event": "tool_observation", "schema_version": 1, "call_id": callID, "tool": tool}
	for key, value := range map[string]string{"request_id": trace.requestID, "thread_id": trace.threadID, "session_id": trace.sessionID} {
		if modelresponses.SafeDiagnosticIdentity(value) {
			fields[key] = value
		}
	}
	trace.debug.Event(fields)
}
