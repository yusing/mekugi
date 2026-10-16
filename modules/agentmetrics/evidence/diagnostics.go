// Source: internal/router/debug_ax.go:47:123 and debug.go:126:156@[ac8511fa] read snapshot projection and output alias checks.
package evidence

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/contracts/inspection"
)

type diagnosticReads struct {
	log   AXReadLog
	valid bool
}

// DiagnosticReads returns a usable unavailable snapshot even when validation
// fails, so bundle export can report the failure without partial attribution.
func (s *Service) DiagnosticReads(ctx context.Context, path string) (diagnosticsection.Reads, error) {
	log, err := s.ReadLog(ctx, path)
	return diagnosticReads{log: log, valid: err == nil}, err
}

func (r diagnosticReads) Threads() []string { return slices.Collect(maps.Keys(r.log.Threads)) }

func (r diagnosticReads) Anonymous() (jsontext.Value, error) {
	reads, found := r.log.Threads[""]
	if !found {
		return nil, nil
	}
	return json.Marshal(&reads, jsonv1.DefaultOptionsV1())
}

func (r diagnosticReads) Select(thread string) (jsontext.Value, inspection.AXCollector, error) {
	reads := r.log.ForThread(thread)
	if !r.valid {
		reads.State = "invalid_or_unavailable"
	}
	raw, err := json.Marshal(&reads, jsonv1.DefaultOptionsV1())
	return raw, new(Service).NewInspection(&reads), err
}

func (s *Service) PrepareDiagnosticReads(path string, outputs ...string) error {
	if err := s.PrepareReadLog(path); err != nil {
		return err
	}
	return ValidateOutputAliases(path, outputs...)
}

// Check before opening mutable outputs, including when instrumentation is manual.
func ValidateOutputAliases(readLog string, outputs ...string) error {
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
