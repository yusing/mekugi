// Source: internal/router/debug.go@[ac8511fa] private bundle lifecycle.
package diagnostic

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yusing/mekugi/libraries/persistence"
)

// Debug output is separate from sanitized capture. Failure events include the
// complete error string, which can contain request content or credentials.
type Bundle struct {
	axReadPath, axReportPath string
	service                  *Service
	replayDirectory          string
	discover                 func(context.Context, []string) (map[string][]string, error)
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

type BundleOptions struct {
	Root, ReplayDirectory, CapturePath, ReadLogPath string
	Discover                                        func(context.Context, []string) (map[string][]string, error)
}

func (d *Bundle) Paths() []string              { return slices.Clone(d.paths) }
func (d *Bundle) CapturePath() string          { return d.capturePath }
func (d *Bundle) MetricsPath() string          { return d.metricsPath }
func (d *Bundle) ReadLogPath() string          { return d.axReadPath }
func (d *Bundle) Writes() *persistence.Counter { return d.writes }
func (d *Bundle) Fail(err error)               { d.mu.Lock(); defer d.mu.Unlock(); d.err = errors.Join(d.err, err) }
func (s *Service) OpenBundle(options BundleOptions) (*Bundle, error) {
	if s.sections.ReadSnapshot != nil && options.Discover == nil {
		return nil, errors.New("AX export requires rollout discovery")
	}
	capturePath := options.CapturePath
	directory, release, err := createBundle(options.Root)
	if err != nil {
		return nil, fmt.Errorf("create debug directory: %w", err)
	}
	success := false
	var d *Bundle
	defer func() {
		if !success {
			if d == nil || d.release != nil {
				_ = release()
			}
		}
	}()
	capture, metrics := "", ""
	if s.sections.Transport != nil {
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
	if s.sections.PrepareReads != nil {
		readLog = options.ReadLogPath
		if readLog == "" {
			readLog = filepath.Join(directory, "reads.jsonl")
		}
		if err := s.sections.PrepareReads(readLog, capture, metrics); err != nil {
			return nil, fmt.Errorf("initialize AX read log: %w", err)
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
	if s.sections.Transport != nil {
		paths = append(paths, capture, metrics)
	}
	paths = append(paths, instructionsPath)
	d = &Bundle{service: s, replayDirectory: options.ReplayDirectory, discover: options.Discover,
		writes: new(persistence.Counter), release: release, paths: paths,
		capturePath: capture, metricsPath: metrics,
	}
	if s.sections.PrepareReads != nil {
		d.axReadPath, d.axReportPath = readLog, filepath.Join(directory, "ax.json")
		d.paths = append(d.paths, d.axReadPath, d.axReportPath)
		d.axThreads = make(map[string]bool)
	}
	d.log, err = os.OpenFile(d.paths[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		d.dump, err = os.OpenFile(instructionsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize debug artifacts in %s: %w", directory, errors.Join(err, d.Close()))
	}
	d.Event(map[string]any{
		"event": "router_start",
	})
	if d.err != nil {
		return nil, fmt.Errorf("initialize debug artifacts in %s: %w", directory, d.Close())
	}

	success = true
	return d, nil
}

func (d *Bundle) write(file *os.File, value any) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err == nil {
		d.err = json.MarshalEncode(jsontext.NewEncoder(d.writes.Writer(file)), value, jsonv1.DefaultOptionsV1())
	}
}

func (d *Bundle) Event(fields map[string]any) {
	if d == nil {
		return
	}
	fields["timestamp"] = time.Now().UTC()
	d.write(d.log, fields)
}

func (d *Bundle) Close() error {
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
