// Source: internal/router/session_inspect_debug.go:128:171,205@[ac8511fa] capture classification and field selection.
package capture

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/contracts/diagnosticsection"
)

// DiagnosticRecord projects the capture-owned schema without exposing payloads.
func DiagnosticRecord(row map[string]jsontext.Value) diagnosticsection.Record {
	value := func(key string) string { var text string; _ = json.Unmarshal(row[key], &text); return text }
	boundary, status := value("boundary"), value("response_status")
	var statusCode int
	_ = json.Unmarshal(row["status_code"], &statusCode)
	failed := (boundary == "codex" || boundary == "provider") && string(row["response_complete"]) == "false" || statusCode >= 400 || value("capture_error") != ""
	switch status {
	case "failed", "incomplete", "cancelled", "error", "http_error":
		failed = true
	}
	keys := []string{"timestamp", "request_id", "capture_id", "thread_id", "session_id", "captured_at", "boundary", "control_direction", "request_sequence", "provider_attempt", "request_model", "status_code", "response_complete", "response_status", "capture_error", "duration_ms", "provider_response", "usage", "tool_calls"}
	metadata := make(map[string]jsontext.Value)
	for _, key := range keys {
		if value, exists := row[key]; exists {
			metadata[key] = value
		}
	}
	return diagnosticsection.Record{Kind: boundary + "/" + status, Failure: failed, Metadata: metadata}
}

func DiagnosticMetrics(row map[string]jsontext.Value) map[string]jsontext.Value {
	selected := make(map[string]jsontext.Value)
	for _, key := range []string{"schema", "mode", "requests", "usage", "cache", "transport", "capture", "storage_writes"} {
		if value, exists := row[key]; exists {
			selected[key] = value
		}
	}
	return selected
}
