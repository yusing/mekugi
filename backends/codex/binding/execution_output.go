package binding

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strconv"
	"strings"
)

// ExecutionResult describes the host envelope, not the success of any nested
// tool. Consumers join native trace evidence before assigning nested outcomes.
type ExecutionResult struct {
	Terminal     bool
	Succeeded    bool
	ExitCode     *int
	Text         string
	ResumeHandle string
}

// DecodeExecutionResult reads retained stock tool output without interpreting
// program output as a continuation or replacing the original host result.
func DecodeExecutionResult(toolName string, raw jsonv1.RawMessage) ExecutionResult {
	var result ExecutionResult
	var success *bool
	if json.Unmarshal(raw, &result.Text, jsonv1.DefaultOptionsV1()) != nil {
		var object struct {
			Content jsonv1.RawMessage `json:"content"`
			Success *bool             `json:"success"`
		}
		if json.Unmarshal(raw, &object, jsonv1.DefaultOptionsV1()) == nil && (len(object.Content) != 0 || object.Success != nil) {
			result.Text, success = strings.Join(ExecutionOutputTexts(object.Content), "\n"), object.Success
		} else {
			result.Text = strings.Join(ExecutionOutputTexts(raw), "\n")
		}
	}
	if toolName == "apply_patch" {
		result.Terminal = true
		result.Succeeded = strings.HasPrefix(strings.TrimSpace(result.Text), "Success.")
		if success != nil {
			result.Succeeded = *success
		}
		return result
	}
	if toolName != "exec_command" && toolName != "write_stdin" {
		state, cell, _ := CodeModeExecutionHeader(result.Text)
		switch state {
		case "running":
			result.ResumeHandle = "cell:" + cell
		case "Script completed":
			result.Terminal, result.Succeeded = true, true
		case "Script failed", "Script terminated":
			result.Terminal = true
		}
		return result
	}
	state, _ := NativeExecutionHeader(result.Text)
	if session := NativeExecutionSession(result.Text); session != 0 {
		result.ResumeHandle = "session:" + strconv.FormatInt(session, 10)
		return result
	}
	if code, ok := strings.CutPrefix(state, "Process exited with code "); ok {
		if value, err := strconv.Atoi(code); err == nil {
			result.Terminal, result.ExitCode, result.Succeeded = true, new(value), value == 0
			return result
		}
	}
	// A failed write, such as one to closed stdin, leaves the process running;
	// only its exit or disappearance ends the call. Other rejected or aborted
	// native commands have no live session.
	result.Terminal = toolName != "write_stdin" || strings.Contains(result.Text, "Unknown process id")
	return result
}

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
	var result map[string]jsonv1.RawMessage
	if jsonv1.Unmarshal([]byte(text), &result) != nil {
		return 0
	}
	if batch, exists := result["results"]; exists {
		var results []map[string]jsonv1.RawMessage
		if jsonv1.Unmarshal(batch, &results) != nil || len(results) == 0 {
			return 0
		}
		// Completed prefixes have no live session. Only the current final
		// program can yield when a sequential carrier exits on a host error.
		result = results[len(results)-1]
	}
	var output string
	var id int64
	if jsonv1.Unmarshal(result["output"], &output) != nil ||
		jsonv1.Unmarshal(result["session_id"], &id) != nil || id <= 0 {
		return 0
	}
	if exit, exists := result["exit_code"]; exists && string(exit) != "null" {
		return 0
	}
	return id
}
