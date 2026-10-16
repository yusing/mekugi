package router

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

type debugInspectionArtifact struct {
	Bytes   *int64         `json:"bytes,omitempty"`
	Name    string         `json:"name"`
	Path    string         `json:"path"`
	State   string         `json:"state"`
	Records int            `json:"records,omitzero"`
	Counts  map[string]int `json:"counts,omitempty"`
}

type debugInspectionEvidence struct {
	Source   string                    `json:"source"`
	Line     int                       `json:"line"`
	Metadata map[string]jsontext.Value `json:"metadata"`
	Text     map[string]inspectedText  `json:"text,omitempty"`
}

type debugSessionInspection struct {
	Schema        string                    `json:"schema"`
	Directory     string                    `json:"directory"`
	RequestID     string                    `json:"request_id,omitempty"`
	Artifacts     []debugInspectionArtifact `json:"artifacts"`
	Metrics       map[string]jsontext.Value `json:"metrics,omitempty"`
	AX            map[string]jsontext.Value `json:"ax,omitempty"`
	TotalRequests int                       `json:"total_requests"`
	Requests      []debugInspectionEvidence `json:"requests"`
	TotalEvidence int                       `json:"total_evidence"`
	Offset        int                       `json:"offset"`
	NextOffset    *int                      `json:"next_offset,omitempty"`
	Evidence      []debugInspectionEvidence `json:"evidence"`
}

// inspectDebugSession reads the selected bundle only. Each artifact is independent:
// startup failures can leave useful diagnostics alongside missing final reports.
func inspectDebugSession(agentMetrics *evidence.Service, diagnostics *transportcapture.Service, ctx context.Context, directory, requestID, field string, offset, limit, textBytes int, stdout, stderr io.Writer) int {
	directory, err := filepath.Abs(directory)
	if err != nil {
		fmt.Fprintln(stderr, "mekugi inspect-session:", err)
		return 1
	}
	result := debugSessionInspection{Schema: "mekugi.session.debug.v1", Directory: directory, RequestID: requestID, Offset: offset}
	code := 0
	reportError := func(path string, err error) {
		fmt.Fprintf(stderr, "mekugi inspect-session: %s: %v\n", path, err)
		code = 1
	}
	addEvidence := func(name string, line int, row map[string]jsontext.Value, keys []string, textKeys []string) {
		index := result.TotalEvidence
		result.TotalEvidence++
		if index < offset || len(result.Evidence) >= limit {
			return
		}
		entry := debugInspectionEvidence{Source: name, Line: line, Metadata: debugInspectionFields(row, keys)}
		for _, key := range textKeys {
			if field == "" || field == "diagnostic" && key != "error" {
				continue
			}
			value, ok := row[key]
			if !ok {
				continue
			}
			text := string(value)
			if value.Kind() == '"' {
				if err := json.Unmarshal(value, &text); err != nil {
					continue
				}
			}
			if entry.Text == nil {
				entry.Text = make(map[string]inspectedText)
			}
			entry.Text[key] = inspectText(text, true, textBytes)
		}
		result.Evidence = append(result.Evidence, entry)
	}
	identityKeys := []string{"timestamp", "request_id", "capture_id", "thread_id", "session_id"}
	metadataKeys := map[string][]string{
		"router.jsonl":       append(append([]string{}, identityKeys...), "event", "outcome", "failed", "source", "call_id", "message_id", "phase", "upstream_status", "duration_ms", "diagnostic_code", "diagnostic_reference", "response_stream", "cancellation_cause"),
		"capture.jsonl":      append(append([]string{}, identityKeys...), "captured_at", "boundary", "control_direction", "request_sequence", "provider_attempt", "request_model", "status_code", "response_complete", "response_status", "capture_error", "duration_ms", "provider_response", "usage", "tool_calls"),
		"instructions.jsonl": append(append([]string{}, identityKeys...), "scope", "model", "cached_input_items", "projected_input_bytes", "wire_input_bytes", "cache_rebased", "wire_request_present"),
	}
	for _, name := range []string{"router.jsonl", "capture.jsonl", "instructions.jsonl"} {
		if name == "capture.jsonl" && diagnostics == nil {
			continue
		}
		path := filepath.Join(directory, name)
		keys := metadataKeys[name]
		artifact := debugInspectionArtifact{Name: name, Path: path, State: "observed", Counts: make(map[string]int)}
		var instructions debugInstructionDecoder
		var instructionErr error
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			artifact.Bytes = new(info.Size())
		}
		err := readDebugInspectionFile(ctx, path, true, func(line int, row map[string]jsontext.Value) {
			artifact.Records++
			if name == "instructions.jsonl" {
				if err := instructions.resolve(row); err != nil {
					if instructionErr == nil {
						instructionErr = fmt.Errorf("line %d: %w", line, err)
					}
					return
				}
			}
			kind := debugInspectionString(row, "event")
			if name == "capture.jsonl" {
				kind = debugInspectionString(row, "boundary") + "/" + debugInspectionString(row, "response_status")
			}
			if name == "instructions.jsonl" {
				kind = debugInspectionString(row, "model")
			}
			if kind != "" {
				artifact.Counts[kind]++
			}
			if name == "router.jsonl" && kind == "request_complete" {
				if outcome := debugInspectionString(row, "outcome"); outcome != "" {
					artifact.Counts["outcome/"+outcome]++
				}
			}
			if requestID != "" && debugInspectionString(row, "request_id") != requestID && debugInspectionString(row, "capture_id") != requestID {
				return
			}
			switch name {
			case "router.jsonl":
				if kind == "request_complete" {
					index := result.TotalRequests
					result.TotalRequests++
					if index >= offset && len(result.Requests) < limit {
						result.Requests = append(result.Requests, debugInspectionEvidence{Source: name, Line: line, Metadata: debugInspectionFields(row, keys)})
					}
				}
				outcome := debugInspectionString(row, "outcome")
				nonSuccess := kind == "request_complete" && outcome != "" && outcome != "completed"
				if requestID == "" && len(row["error"]) == 0 && !nonSuccess && string(row["failed"]) != "true" {
					return
				}
				addEvidence(name, line, row, keys, []string{"error"})
			case "capture.jsonl":
				var status int
				_ = json.Unmarshal(row["status_code"], &status)
				responseStatus := debugInspectionString(row, "response_status")
				boundary := debugInspectionString(row, "boundary")
				incomplete := (boundary == "codex" || boundary == "provider") && string(row["response_complete"]) == "false"
				nonSuccess := incomplete || status >= 400
				switch responseStatus {
				case "failed", "incomplete", "cancelled", "error", "http_error":
					nonSuccess = true
				}
				if requestID == "" && !nonSuccess && debugInspectionString(row, "capture_error") == "" {
					return
				}
				addEvidence(name, line, row, keys, nil)
			case "instructions.jsonl":
				if requestID == "" {
					return
				}
				addEvidence(name, line, row, keys, []string{"instructions", "developer_messages", "tools", "additional_tools", "wire_developer_messages", "wire_additional_tools"})
			}
		})
		err = errors.Join(err, instructionErr)
		if err != nil {
			artifact.State = "invalid_or_unavailable"
			reportError(path, err)
		}
		result.Artifacts = append(result.Artifacts, artifact)
	}
	readLog := filepath.Join(directory, "reads.jsonl")
	var reports []string
	if diagnostics != nil {
		reports = append(reports, "metrics.json")
	}
	if agentMetrics != nil {
		reports = append(reports, "ax.json")
	}
	for _, name := range reports {
		path := filepath.Join(directory, name)
		artifact := debugInspectionArtifact{Name: name, Path: path, State: "observed"}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			artifact.Bytes = new(info.Size())
		}
		err := readDebugInspectionFile(ctx, path, false, func(_ int, row map[string]jsontext.Value) {
			artifact.Records++
			if name == "metrics.json" {
				result.Metrics = debugInspectionFields(row, []string{"schema", "mode", "requests", "usage", "cache", "transport", "capture", "storage_writes"})
			} else {
				result.AX = debugInspectionFields(row, []string{"schema", "scope", "log_state", "dropped_threads", "threads", "log_only_threads", "dropped_log_threads", "unattributed_reads"})
				if selected := debugInspectionString(row, "read_log"); filepath.IsAbs(selected) {
					readLog = selected
				}
			}
		})
		if err != nil {
			artifact.State = "invalid_or_unavailable"
			reportError(path, err)
		}
		result.Artifacts = append(result.Artifacts, artifact)
	}
	if agentMetrics != nil {
		reads := debugInspectionArtifact{Name: "reads.jsonl", Path: readLog, State: "observed"}
		if info, err := os.Stat(readLog); err == nil && info.Mode().IsRegular() {
			reads.Bytes = new(info.Size())
		}
		if _, err := agentMetrics.ReadLog(ctx, readLog); err != nil {
			reads.State = "invalid_or_unavailable"
			reportError(readLog, err)
		}
		result.Artifacts = append(result.Artifacts, reads)
	}
	if next := offset + max(len(result.Evidence), len(result.Requests)); next < max(result.TotalEvidence, result.TotalRequests) {
		result.NextOffset = &next
	}
	if requestID != "" && result.TotalEvidence == 0 {
		reportError(directory, fmt.Errorf("request %q is unavailable in this bundle", requestID))
	}
	if err := json.MarshalEncode(jsontext.NewEncoder(stdout), &result, json.Deterministic(true)); err != nil {
		reportError(directory, err)
	}
	return code
}

func debugInspectionString(row map[string]jsontext.Value, key string) string {
	var value string
	_ = json.Unmarshal(row[key], &value)
	return value
}

func debugInspectionFields(row map[string]jsontext.Value, keys []string) map[string]jsontext.Value {
	selected := make(map[string]jsontext.Value)
	for _, key := range keys {
		if value, ok := row[key]; ok {
			selected[key] = value
		}
	}
	return selected
}

func readDebugInspectionFile(ctx context.Context, path string, lines bool, observe func(int, map[string]jsontext.Value)) error {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSessionInspectionBytes {
		return errors.New("debug artifact must be a regular file no larger than 64 MiB")
	}
	reader := &io.LimitedReader{R: file, N: maxSessionInspectionBytes + 1}
	if !lines {
		var row map[string]jsontext.Value
		if err := json.UnmarshalRead(reader, &row); err != nil {
			return err
		}
		if row == nil {
			return errors.New("expected a JSON object")
		}
		if reader.N == 0 {
			return errors.New("debug artifact exceeds 64 MiB")
		}
		observe(1, row)
		return ctx.Err()
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxReplayRecordBytes)
	for line := 1; scanner.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var row map[string]jsontext.Value
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if row == nil {
			return fmt.Errorf("line %d: expected a JSON object", line)
		}
		observe(line, row)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if reader.N == 0 {
		return errors.New("debug artifact exceeds 64 MiB")
	}
	return nil
}
