// Source: internal/router/session_inspect_debug.go@[ac8511fa] independent artifact inspection.
package diagnostic

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

	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"

	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

const maxSessionInspectionBytes = 64 << 20

type Artifact struct {
	Bytes   *int64         `json:"bytes,omitempty"`
	Name    string         `json:"name"`
	Path    string         `json:"path"`
	State   string         `json:"state"`
	Records int            `json:"records,omitzero"`
	Counts  map[string]int `json:"counts,omitempty"`
}

type Evidence struct {
	Source   string                    `json:"source"`
	Line     int                       `json:"line"`
	Metadata map[string]jsontext.Value `json:"metadata"`
	Text     map[string]query.Text     `json:"text,omitempty"`
}

type Report struct {
	Schema        string                    `json:"schema"`
	Directory     string                    `json:"directory"`
	RequestID     string                    `json:"request_id,omitempty"`
	Artifacts     []Artifact                `json:"artifacts"`
	Metrics       map[string]jsontext.Value `json:"metrics,omitempty"`
	AX            map[string]jsontext.Value `json:"ax,omitempty"`
	TotalRequests int                       `json:"total_requests"`
	Requests      []Evidence                `json:"requests"`
	TotalEvidence int                       `json:"total_evidence"`
	Offset        int                       `json:"offset"`
	NextOffset    *int                      `json:"next_offset,omitempty"`
	Evidence      []Evidence                `json:"evidence"`
}

type InspectOptions struct {
	Directory, RequestID, Field string
	Offset, Limit, TextBytes    int
}
type ArtifactError struct {
	Path string
	Err  error
}

// InspectBundle retains successful artifacts when another artifact fails.
func (s *Service) InspectBundle(ctx context.Context, options InspectOptions) (Report, []ArtifactError) {
	directory, requestID, field := options.Directory, options.RequestID, options.Field
	offset, limit, textBytes := options.Offset, options.Limit, options.TextBytes
	directory, err := filepath.Abs(directory)
	if err != nil {
		return Report{}, []ArtifactError{{Path: options.Directory, Err: err}}
	}
	result := Report{Schema: "mekugi.session.debug.v1", Directory: directory, RequestID: requestID, Offset: offset}
	var failures []ArtifactError
	reportError := func(path string, err error) { failures = append(failures, ArtifactError{Path: path, Err: err}) }
	addEvidence := func(name string, line int, row map[string]jsontext.Value, metadata map[string]jsontext.Value, textKeys []string) {
		index := result.TotalEvidence
		result.TotalEvidence++
		if index < offset || len(result.Evidence) >= limit {
			return
		}
		entry := Evidence{Source: name, Line: line, Metadata: metadata}
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
				entry.Text = make(map[string]query.Text)
			}
			entry.Text[key] = query.TextValue(text, true, textBytes)
		}
		result.Evidence = append(result.Evidence, entry)
	}
	identityKeys := []string{"timestamp", "request_id", "capture_id", "thread_id", "session_id"}
	metadataKeys := map[string][]string{
		"router.jsonl":       append(append([]string{}, identityKeys...), "event", "outcome", "failed", "source", "call_id", "message_id", "phase", "upstream_status", "duration_ms", "diagnostic_code", "diagnostic_reference", "response_stream", "cancellation_cause"),
		"instructions.jsonl": append(append([]string{}, identityKeys...), "scope", "model", "cached_input_items", "projected_input_bytes", "wire_input_bytes", "cache_rebased", "wire_request_present"),
	}
	for _, name := range []string{"router.jsonl", "capture.jsonl", "instructions.jsonl"} {
		if name == "capture.jsonl" && s.sections.Transport == nil {
			continue
		}
		path := filepath.Join(directory, name)
		keys := metadataKeys[name]
		artifact := Artifact{Name: name, Path: path, State: "observed", Counts: make(map[string]int)}
		var instructions instructionDecoder
		var instructionErr error
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			artifact.Bytes = new(info.Size())
		}
		err := readArtifact(ctx, path, true, func(line int, row map[string]jsontext.Value) {
			artifact.Records++
			if name == "instructions.jsonl" {
				if err := instructions.resolve(row); err != nil {
					if instructionErr == nil {
						instructionErr = fmt.Errorf("line %d: %w", line, err)
					}
					return
				}
			}
			kind := recordString(row, "event")
			var capture diagnosticsection.Record
			if name == "capture.jsonl" {
				capture = s.sections.Transport.Record(row)
				kind = capture.Kind
			}
			if name == "instructions.jsonl" {
				kind = recordString(row, "model")
			}
			if kind != "" {
				artifact.Counts[kind]++
			}
			if name == "router.jsonl" && kind == "request_complete" {
				if outcome := recordString(row, "outcome"); outcome != "" {
					artifact.Counts["outcome/"+outcome]++
				}
			}
			if requestID != "" && recordString(row, "request_id") != requestID && recordString(row, "capture_id") != requestID {
				return
			}
			switch name {
			case "router.jsonl":
				if kind == "request_complete" {
					index := result.TotalRequests
					result.TotalRequests++
					if index >= offset && len(result.Requests) < limit {
						result.Requests = append(result.Requests, Evidence{Source: name, Line: line, Metadata: selectFields(row, keys)})
					}
				}
				outcome := recordString(row, "outcome")
				nonSuccess := kind == "request_complete" && outcome != "" && outcome != "completed"
				if requestID == "" && len(row["error"]) == 0 && !nonSuccess && string(row["failed"]) != "true" {
					return
				}
				addEvidence(name, line, row, selectFields(row, keys), []string{"error"})
			case "capture.jsonl":
				if requestID == "" && !capture.Failure {
					return
				}
				addEvidence(name, line, row, capture.Metadata, nil)
			case "instructions.jsonl":
				if requestID == "" {
					return
				}
				addEvidence(name, line, row, selectFields(row, keys), []string{"instructions", "developer_messages", "tools", "additional_tools", "wire_developer_messages", "wire_additional_tools"})
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
	if s.sections.Transport != nil {
		reports = append(reports, "metrics.json")
	}
	if s.sections.ReadSnapshot != nil {
		reports = append(reports, "ax.json")
	}
	for _, name := range reports {
		path := filepath.Join(directory, name)
		artifact := Artifact{Name: name, Path: path, State: "observed"}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			artifact.Bytes = new(info.Size())
		}
		err := readArtifact(ctx, path, false, func(_ int, row map[string]jsontext.Value) {
			artifact.Records++
			if name == "metrics.json" {
				result.Metrics = s.sections.Transport.Metrics(row)
			} else {
				result.AX = selectFields(row, []string{"schema", "scope", "log_state", "dropped_threads", "threads", "log_only_threads", "dropped_log_threads", "unattributed_reads"})
				if selected := recordString(row, "read_log"); filepath.IsAbs(selected) {
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
	if s.sections.ReadSnapshot != nil {
		reads := Artifact{Name: "reads.jsonl", Path: readLog, State: "observed"}
		if info, err := os.Stat(readLog); err == nil && info.Mode().IsRegular() {
			reads.Bytes = new(info.Size())
		}
		if _, err := s.sections.ReadSnapshot(ctx, readLog); err != nil {
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
	return result, failures
}

func recordString(row map[string]jsontext.Value, key string) string {
	var value string
	_ = json.Unmarshal(row[key], &value)
	return value
}

func selectFields(row map[string]jsontext.Value, keys []string) map[string]jsontext.Value {
	selected := make(map[string]jsontext.Value)
	for _, key := range keys {
		if value, ok := row[key]; ok {
			selected[key] = value
		}
	}
	return selected
}

func readArtifact(ctx context.Context, path string, lines bool, observe func(int, map[string]jsontext.Value)) error {
	file, err := sourceparse.Open(path)
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
	scanner.Buffer(make([]byte, 4096), 32<<20)
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
