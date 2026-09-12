package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yusing/mekugi/capturer"
)

// Debug output is separate from sanitized capture. Only the instruction dump
// contains prompt text; diagnostics never serialize arbitrary errors or headers.
type debugOutput struct {
	mu               sync.Mutex
	log              *os.File
	dump             *os.File
	paths            []string
	axThreads        map[string]bool
	axDroppedThreads bool
	err              error
}

type debugContextKey struct{}

func openDebugOutput(flags routerFlags) (*debugOutput, error) {
	if !*flags.debug {
		return nil, nil
	}
	directory, err := os.MkdirTemp("", "mekugi-debug-")
	if err != nil {
		return nil, fmt.Errorf("create debug directory: %w", err)
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if *flags.captureOutput == "" {
		*flags.captureOutput = filepath.Join(directory, "capture.jsonl")
	}
	if *flags.metricsOutput == "" {
		*flags.metricsOutput = filepath.Join(directory, "metrics.json")
	}
	capture, err := filepath.Abs(*flags.captureOutput)
	if err != nil {
		return nil, err
	}
	metrics, err := filepath.Abs(*flags.metricsOutput)
	if err != nil {
		return nil, err
	}
	readLog := os.Getenv(capturer.AXReadOutputEnvironment)
	if readLog == "" {
		readLog = filepath.Join(directory, "reads.jsonl")
	}
	if err := capturer.PrepareAXReadJournal(readLog); err != nil {
		return nil, fmt.Errorf("initialize AX read journal: %w", err)
	}
	if err := validateAXOutputAliases(readLog, capture, metrics); err != nil {
		return nil, err
	}
	d := &debugOutput{
		paths: []string{filepath.Join(directory, "router.jsonl"), capture, metrics,
			filepath.Join(directory, "instructions.jsonl"), readLog, filepath.Join(directory, "ax.json")},
		axThreads: make(map[string]bool),
	}
	d.log, err = os.OpenFile(d.paths[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		d.dump, err = os.OpenFile(d.paths[3], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize debug artifacts in %s: %w", directory, errors.Join(err, d.close()))
	}
	d.event(map[string]any{
		"event": "router_start", "feature_usage_schema": 1,
		"feature_usage_features": []string{"journal", "commentary"},
	})

	return d, nil
}

// Check before opening mutable outputs, including when instrumentation is manual.
func validateAXOutputAliases(readLog string, outputs ...string) error {
	if readLog == "" {
		return nil
	}
	journalPath, err := filepath.Abs(readLog)
	if err != nil {
		return err
	}
	journalInfo, err := os.Stat(journalPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, output := range outputs {
		if output == "" {
			continue
		}
		outputPath, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		info, err := os.Stat(outputPath)
		if outputPath == journalPath || err == nil && journalInfo != nil && os.SameFile(journalInfo, info) {
			return errors.New("AX journal, capture-output, and metrics-output must use different files")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (d *debugOutput) handler(next http.Handler) http.Handler {
	if d == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.observeAXThread(codexThreadID(r.Header))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), debugContextKey{}, d)))
	})
}

func (d *debugOutput) write(file *os.File, value any) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err == nil {
		d.err = json.NewEncoder(file).Encode(value)
	}
}

func (d *debugOutput) event(fields map[string]any) {
	if d == nil {
		return
	}
	fields["timestamp"] = time.Now().UTC()
	d.write(d.log, fields)
}

func (d *debugOutput) instructions(body, wire []byte, headers http.Header, sessionID, requestID string, cachedInput int) {
	if d == nil {
		return
	}
	d.observeAXThread(codexThreadID(headers))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		d.mu.Lock()
		d.err = errors.Join(d.err, err)
		d.mu.Unlock()
		return
	}
	// Keep the local projection and the instruction-bearing wire subset distinct.
	// A reconstructed prefix is not itself evidence of provider delivery.
	var input []json.RawMessage
	_ = json.Unmarshal(fields["input"], &input)
	developers := []json.RawMessage{}
	additional := []json.RawMessage{}
	for _, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if jsonString(item, "role") == "developer" {
			developers = append(developers, raw)
		}
		if jsonString(item, "type") == "additional_tools" {
			additional = append(additional, raw)
		}
	}
	var wireFields map[string]json.RawMessage
	var wireInput []json.RawMessage
	_ = json.Unmarshal(wire, &wireFields)
	_ = json.Unmarshal(wireFields["input"], &wireInput)
	wireDevelopers := []json.RawMessage{}
	wireAdditional := []json.RawMessage{}
	for _, raw := range wireInput {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if jsonString(item, "role") == "developer" {
			wireDevelopers = append(wireDevelopers, raw)
		}
		if jsonString(item, "type") == "additional_tools" {
			wireAdditional = append(wireAdditional, raw)
		}
	}
	d.write(d.dump, map[string]any{
		"timestamp": time.Now().UTC(), "request_id": requestID,
		"client_request_id": headers.Get("x-client-request-id"),
		"thread_id":         codexThreadID(headers), "session_id": sessionID,
		"scope": "projected_responses_request", "cached_input_items": cachedInput,
		"wire_request_present": len(wire) != 0, "wire_input_items": len(wireInput),
		"wire_developer_messages": wireDevelopers, "wire_additional_tools": wireAdditional,
		"wire_previous_response_id": wireFields["previous_response_id"],
		"cache_rebased":             len(wire) != 0 && jsonString(fields, "previous_response_id") != "" && jsonString(wireFields, "previous_response_id") == "",
		"model":                     fields["model"], "previous_response_id": fields["previous_response_id"],
		"instructions": fields["instructions"], "developer_messages": developers,
		"tools": fields["tools"], "additional_tools": additional,
	})
}

func debugRequest(ctx context.Context) (*debugOutput, string) {
	d, _ := ctx.Value(debugContextKey{}).(*debugOutput)
	if d == nil {
		return nil, ""
	}
	if captureID, _ := capturer.RequestCorrelation(ctx); captureID != "" {
		return d, captureID
	}
	return d, rand.Text()
}

func (d *debugOutput) close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var closeErr error
	for _, file := range []*os.File{d.log, d.dump} {
		if file != nil {
			closeErr = errors.Join(closeErr, file.Close())
		}
	}
	return errors.Join(d.err, closeErr, d.writeAXReport())
}
