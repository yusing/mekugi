package binding

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Source: internal/router/execution_continuation.go:200:309@b9a7b3fe.
// Match only the host's leading metadata, never strings printed by the program
// below Output:. Native session metadata inside exec needs a verified
// router carrier or transparent native-result projection as well.
func CodeModeExecutionHeader(text string) (status, cell string, body string) {
	first, rest, ok := strings.Cut(text, "\n")
	if !ok {
		return "", "", ""
	}
	switch {
	case strings.HasPrefix(first, "Script running with cell ID "):
		cell = strings.TrimPrefix(first, "Script running with cell ID ")
		if cell == "" || strings.ContainsAny(cell, " \t\r\n") {
			return "", "", ""
		}
		status = "running"
	case first == "Script completed" || first == "Script failed" || first == "Script terminated":
		status = first
	default:
		return "", "", ""
	}
	wall, rest, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(wall, "Wall time ") || !strings.HasSuffix(wall, " seconds") {
		return "", "", ""
	}
	duration := strings.TrimSuffix(strings.TrimPrefix(wall, "Wall time "), " seconds")
	if _, err := strconv.ParseFloat(duration, 64); err != nil {
		return "", "", ""
	}
	body, ok = strings.CutPrefix(rest, "Output:\n")
	if !ok {
		return "", "", ""
	}
	return status, cell, body
}

func NativeExecutionHeader(text string) (state, body string) {
	line, rest, ok := strings.Cut(text, "\n")
	if strings.HasPrefix(line, "Chunk ID: ") {
		line, rest, ok = strings.Cut(rest, "\n")
	}
	if !ok || !strings.HasPrefix(line, "Wall time: ") || !strings.HasSuffix(line, " seconds") {
		return "", ""
	}
	duration := strings.TrimSuffix(strings.TrimPrefix(line, "Wall time: "), " seconds")
	if _, err := strconv.ParseFloat(duration, 64); err != nil {
		return "", ""
	}
	state, rest, ok = strings.Cut(rest, "\n")
	if !ok {
		return "", ""
	}
	line, rest, ok = strings.Cut(rest, "\n")
	if after, ok0 := strings.CutPrefix(line, "Original token count: "); ok0 {
		if _, err := strconv.ParseUint(after, 10, 64); err != nil {
			return "", ""
		}
		line, rest, ok = strings.Cut(rest, "\n")
	}
	if !ok || (line != "Output:" && line != "Final output:") {
		return "", ""
	}
	return state, rest
}

func NativeExecutionSession(text string) int64 {
	state, _ := NativeExecutionHeader(text)
	id, running := strings.CutPrefix(state, "Process running with session ID ")
	if !running {
		return 0
	}
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func NativeJSONSession(text string) int64 {
	var result map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &result) != nil {
		return 0
	}
	if batch, exists := result["results"]; exists {
		var results []map[string]json.RawMessage
		if json.Unmarshal(batch, &results) != nil || len(results) == 0 {
			return 0
		}
		// Completed prefixes have no live session. Only the current final
		// program can yield when a sequential carrier exits on a host error.
		result = results[len(results)-1]
	}
	var output string
	var id int64
	if json.Unmarshal(result["output"], &output) != nil ||
		json.Unmarshal(result["session_id"], &id) != nil || id <= 0 {
		return 0
	}
	if exit, exists := result["exit_code"]; exists && string(exit) != "null" {
		return 0
	}
	return id
}
