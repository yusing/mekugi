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

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/persistence"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// Debug output is separate from sanitized capture. Failure events include the
// complete error string, which can contain request content or credentials.
type debugOutput struct {
	agentMetrics             *evidence.Service
	axReadPath, axReportPath string
	history                  *history.Service
	instructionContent       map[string]bool
	writes                   *persistence.Counter
	release                  func() error
	mu                       sync.Mutex
	log                      *os.File
	dump                     *os.File
	paths                    []string
	capturePath, metricsPath string
	axThreads                map[string]bool
	axDroppedThreads         bool
	err                      error
}

type debugContextKey struct{}

func openDebugOutput(service *history.Service, agentMetrics *evidence.Service, diagnostics *transportcapture.Service, enabled bool, capturePath string) (*debugOutput, error) {
	if !enabled {
		return nil, nil
	}
	directory, release, err := createDebugBundle()
	if err != nil {
		return nil, fmt.Errorf("create debug directory: %w", err)
	}
	success := false
	var d *debugOutput
	defer func() {
		if !success {
			if d == nil || d.release != nil {
				_ = release()
			}
		}
	}()
	capture, metrics := "", ""
	if diagnostics != nil {
		if capturePath == "" {
			capturePath = filepath.Join(directory, "capture.jsonl")
		}
		capture, err = filepath.Abs(capturePath)
		if err != nil {
			return nil, err
		}
		metrics = filepath.Join(directory, "metrics.json")
	}
	readLog := ""
	if agentMetrics != nil {
		readLog = os.Getenv(evidence.AXReadOutputEnvironment)
		if readLog == "" {
			readLog = filepath.Join(directory, "reads.jsonl")
		}
		if err := agentMetrics.PrepareReadLog(readLog); err != nil {
			return nil, fmt.Errorf("initialize AX read log: %w", err)
		}
		if err := validateAXOutputAliases(readLog, capture, metrics); err != nil {
			return nil, err
		}
	}
	// Replay consumes the standard bundle path even when capture output was
	// explicitly selected elsewhere. Keep one capture writer and one record.
	if bundleCapture := filepath.Join(directory, "capture.jsonl"); capture != "" && capture != bundleCapture {
		if err := os.Symlink(capture, bundleCapture); err != nil {
			return nil, fmt.Errorf("link debug capture: %w", err)
		}
	}
	instructionsPath := filepath.Join(directory, "instructions.jsonl")
	paths := []string{filepath.Join(directory, "router.jsonl")}
	if diagnostics != nil {
		paths = append(paths, capture, metrics)
	}
	paths = append(paths, instructionsPath)
	d = &debugOutput{history: service, agentMetrics: agentMetrics,
		writes: new(persistence.Counter), release: release, paths: paths,
		capturePath: capture, metricsPath: metrics,
	}
	if agentMetrics != nil {
		d.axReadPath, d.axReportPath = readLog, filepath.Join(directory, "ax.json")
		d.paths = append(d.paths, d.axReadPath, d.axReportPath)
		d.axThreads = make(map[string]bool)
	}
	d.log, err = os.OpenFile(d.paths[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		d.dump, err = os.OpenFile(instructionsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize debug artifacts in %s: %w", directory, errors.Join(err, d.close()))
	}
	d.event(map[string]any{
		"event": "router_start",
	})
	if d.err != nil {
		return nil, fmt.Errorf("initialize debug artifacts in %s: %w", directory, d.close())
	}

	success = true
	return d, nil
}

// Check before opening mutable outputs, including when instrumentation is manual.
func validateAXOutputAliases(readLog string, outputs ...string) error {
	if readLog == "" {
		return nil
	}
	logPath, err := filepath.Abs(readLog)
	if err != nil {
		return err
	}
	logInfo, err := os.Stat(logPath)
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
		if outputPath == logPath || err == nil && logInfo != nil && os.SameFile(logInfo, info) {
			return errors.New("AX log and output files must use different files")
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
		d.err = json.NewEncoder(d.writes.Writer(file)).Encode(value)
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
		if responses.StringField(item, "role") == "developer" {
			developers = append(developers, raw)
		}
		if responses.StringField(item, "type") == "additional_tools" {
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
		if responses.StringField(item, "role") == "developer" {
			wireDevelopers = append(wireDevelopers, raw)
		}
		if responses.StringField(item, "type") == "additional_tools" {
			wireAdditional = append(wireAdditional, raw)
		}
	}
	d.writeInstructions(map[string]any{
		"timestamp": time.Now().UTC(), "request_id": requestID,
		"client_request_id": headers.Get("x-client-request-id"),
		"thread_id":         codexThreadID(headers), "session_id": sessionID,
		"scope": "projected_responses_request", "cached_input_items": cachedInput,
		"wire_request_present": len(wire) != 0, "wire_input_items": len(wireInput),
		"projected_input_bytes": len(fields["input"]), "wire_input_bytes": len(wireFields["input"]),
		"wire_developer_messages": wireDevelopers, "wire_additional_tools": wireAdditional,
		"wire_previous_response_id": wireFields["previous_response_id"],
		"cache_rebased":             len(wire) != 0 && responses.StringField(fields, "previous_response_id") != "" && responses.StringField(wireFields, "previous_response_id") == "",
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
	if captureID, _ := transportcapture.RequestCorrelation(ctx); captureID != "" {
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
	axErr := d.writeAXReport()
	var releaseErr error
	if d.release != nil {
		releaseErr = d.release()
		d.release = nil
	}
	return errors.Join(d.err, closeErr, axErr, releaseErr)
}
