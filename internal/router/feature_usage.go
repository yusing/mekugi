package router

import (
	"strings"
	"sync"
)

// Feature observations are operational debug evidence, not capture metrics.
// Keep categories allowlisted and identities bounded; never accept payload text,
// publisher capabilities, arbitrary attributes, or errors through this seam.
type featureUsageTrace struct {
	summary   *featureUsageSummary
	debug     *debugOutput
	requestID string
	threadID  string
	sessionID string
}

func (trace featureUsageTrace) record(feature, source, stage, outcome, callID, messageID string) {
	if trace.debug == nil || !validFeatureUsage(feature, source, stage, outcome) {
		return
	}
	fields := map[string]any{
		"event": "feature_usage", "schema_version": 1,
		"feature": feature, "source": source, "stage": stage, "outcome": outcome,
	}
	for key, value := range map[string]string{
		"request_id": trace.requestID, "thread_id": trace.threadID,
		"session_id": trace.sessionID, "call_id": callID, "message_id": messageID,
	} {
		if safeFeatureIdentity(value) {
			fields[key] = value
		}
	}
	if trace.summary != nil {
		trace.summary.mu.Lock()
		trace.summary.counts[source+"."+stage+"."+outcome]++
		trace.summary.mu.Unlock()
	}
	trace.debug.event(fields)
}

func validFeatureUsage(feature, source, stage, outcome string) bool {
	if feature == "journal" {
		switch stage {
		case "mutation":
			return (source == "tool_field" || source == "tool" || source == "shell" || source == "code_mode") &&
				(outcome == "accepted" || outcome == "prepared")
		case "lowering":
			return source == "code_mode" && (outcome == "prepared" || outcome == "unavailable")
		case "render":
			return (source == "report_now" || source == "terminal_flush" || source == "tokens") &&
				(outcome == "prepared" || outcome == "suppressed")
		}
		return false
	}
	switch feature {
	case "commentary":
		switch stage {
		case "authored":
			return (source == "tool_field" || source == "provider_message") && outcome == "observed"
		case "publication":
			return (source == "shell" || source == "code_mode") &&
				(outcome == "accepted" || outcome == "blank" || outcome == "oversized" || outcome == "capacity")
		case "render":
			return (source == "tool_field" || source == "shell" || source == "code_mode" || source == "router_activity") &&
				(outcome == "prepared" || outcome == "suppressed")
		}
	}
	return false
}

func safeFeatureIdentity(value string) bool {
	return value != "" && len(value) <= 256 && !strings.Contains(value, "://") && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':' || r == '/')
	}) == -1
}

// Counts describe branch observations, not unique messages or delivery to a UI.
// Publications without a request owner remain separate thread/call events.
type featureUsageSummary struct {
	mu     sync.Mutex
	counts map[string]uint64
}

func (trace featureUsageTrace) finish(observed, complete bool) {
	if trace.debug == nil || trace.summary == nil {
		return
	}
	state := "unavailable"
	if observed {
		state = "incomplete"
		if complete {
			state = "observed"
		}
	}
	trace.summary.mu.Lock()
	defer trace.summary.mu.Unlock()
	fields := map[string]any{"event": "feature_coverage", "schema_version": 1,
		"feature": "journal", "state": state, "observations": trace.summary.counts}
	for key, value := range map[string]string{"request_id": trace.requestID, "thread_id": trace.threadID, "session_id": trace.sessionID} {
		if safeFeatureIdentity(value) {
			fields[key] = value
		}
	}
	trace.debug.event(fields)
}

func (trace featureUsageTrace) toolCall(callID, tool string) {
	if trace.debug == nil || !safeFeatureIdentity(callID) || !safeFeatureIdentity(tool) {
		return
	}
	fields := map[string]any{"event": "tool_observation", "schema_version": 1, "call_id": callID, "tool": tool}
	for key, value := range map[string]string{"request_id": trace.requestID, "thread_id": trace.threadID, "session_id": trace.sessionID} {
		if safeFeatureIdentity(value) {
			fields[key] = value
		}
	}
	trace.debug.event(fields)
}
