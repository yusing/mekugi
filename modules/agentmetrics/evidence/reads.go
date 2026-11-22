// Source: capturer/ax.go@fdb889f8.
package evidence

import (
	"bufio"
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// AXReadOutputEnvironment opts executor-side readers into a local log.
const AXReadOutputEnvironment = "MEKUGI_AX_OUTPUT"

const maxAXEvidenceBytes = 64 << 20

// AXReadContext contains identities only, never command text or paths.
type AXReadContext struct {
	CallID  string `json:"call_id,omitempty"`
	ShellID string `json:"shell_id,omitempty"`
}

type axReadEvent struct {
	AXReadContext
	FailureClass string    `json:"failure_class,omitempty"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Schema       string    `json:"schema"`
	ID           string    `json:"id"`
	ThreadID     string    `json:"thread_id"`
	Tool         string    `json:"tool"`
	Phase        string    `json:"phase"`
	At           time.Time `json:"at"`
	DurationNS   *int64    `json:"duration_ns,omitempty"`
	Succeeded    *bool     `json:"succeeded,omitempty"`
}

func validAXReader(tool string) bool {
	return tool == "mcat" || tool == "msymbol" || tool == "mfinspect" || tool == "inspect_file"
}

// PrepareReadLog validates the destination before the executor starts.
func (service *Service) PrepareReadLog(path string) error {
	file, err := openAXLog(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func openAXLog(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("AX log path must be absolute")
	}
	flags := os.O_WRONLY | os.O_APPEND | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	file, err := os.OpenFile(path, flags|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(path, flags, 0)
	} else if err == nil {
		// Normalize umask only for a file we created; existing unsafe files
		// must still be rejected without changing their permissions.
		if err := file.Chmod(0600); err != nil {
			file.Close()
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() >= maxAXEvidenceBytes {
		file.Close()
		return nil, errors.New("AX log must be a private regular file below 64 MiB")
	}
	return file, nil
}

func validAXFailureClass(value string) bool {
	switch value {
	case "unknown", "invalid_arguments", "not_found", "permission_denied", "not_regular",
		"invalid_source", "reader_error", "search_error", "resolver_error", "resolver_timeout", "dependency_unavailable",
		"no_editable_location", "output_limit", "retained_file", "execution_error",
		"output_write", "canceled", "deadline_exceeded":
		return true
	}
	return false
}

// ValidAXIdentity accepts opaque identifiers, not arbitrary attributes or paths.
func ValidAXIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == ':')
	}) == -1
}

func validAXReadContext(identity AXReadContext) bool {
	return (identity.CallID == "" || ValidAXIdentity(identity.CallID)) &&
		(identity.ShellID == "" || ValidAXIdentity(identity.ShellID))
}

// AXReadFailure joins a bounded failure sample back to the log and rollout.
type AXReadFailure struct {
	AXReadContext
	ID         string    `json:"id"`
	Tool       string    `json:"tool"`
	At         time.Time `json:"at"`
	DurationNS int64     `json:"duration_ns"`
	Class      string    `json:"class"`
	ExitCode   *int      `json:"exit_code,omitempty"`
}

// AXReadMetrics counts only log-observed invocations, not external reads.
type AXReadMetrics struct {
	State               string            `json:"state"`
	Started             uint64            `json:"started"`
	Completed           uint64            `json:"completed"`
	Succeeded           uint64            `json:"succeeded"`
	Failed              uint64            `json:"failed"`
	Incomplete          uint64            `json:"incomplete"`
	DurationNS          uint64            `json:"duration_ns"`
	ByTool              map[string]uint64 `json:"by_tool"`
	FailuresByClass     map[string]uint64 `json:"failures_by_class"`
	Failures            []AXReadFailure   `json:"failures"`
	DroppedFailures     uint64            `json:"dropped_failure_details"`
	OtherThreadStarted  uint64            `json:"other_thread_started"`
	UnattributedStarted uint64            `json:"unattributed_started"`
	Coverage            string            `json:"coverage"`
}

func unavailableAXReads() AXReadMetrics {
	return AXReadMetrics{State: "unavailable", ByTool: map[string]uint64{},
		FailuresByClass: map[string]uint64{}, Failures: []AXReadFailure{},
		Coverage: "instrumented private-reader invocations only; external reads and necessity are not measured"}
}

// AXReadLog validates the complete log before exposing any attribution.
// An empty thread key represents explicitly unattributed runtime evidence.
type AXReadLog struct {
	Threads map[string]AXReadMetrics
}

func (log AXReadLog) ForThread(threadID string) AXReadMetrics {
	result := unavailableAXReads()
	if threadID != "" {
		if observed, ok := log.Threads[threadID]; ok {
			result = observed
		}
	}
	for id, reads := range log.Threads {
		if id == "" {
			result.UnattributedStarted += reads.Started
		} else if id != threadID {
			result.OtherThreadStarted += reads.Started
		}
	}
	return result
}

// ReadReads validates the log and reports the selected thread with explicit exclusions.
func (service *Service) ReadReads(ctx context.Context, path, threadID string) (AXReadMetrics, error) {
	log, err := service.ReadLog(ctx, path)
	if err != nil {
		return unavailableAXReads(), err
	}
	return log.ForThread(threadID), nil
}

func (service *Service) ReadLog(ctx context.Context, path string) (AXReadLog, error) {
	result := AXReadLog{Threads: map[string]AXReadMetrics{}}
	invalid := func(err error) (AXReadLog, error) { return AXReadLog{}, err }
	if path == "" {
		return result, nil
	}
	file, err := openAXEvidence(path, maxAXEvidenceBytes)
	if err != nil {
		return invalid(err)
	}
	defer file.Close()
	reader := &io.LimitedReader{R: file, N: maxAXEvidenceBytes + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4096)
	starts := make(map[string]axReadEvent)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return invalid(err)
		}
		if !utf8.Valid(scanner.Bytes()) {
			return invalid(errors.New("invalid UTF-8 in AX read event"))
		}
		var event axReadEvent
		decoder := jsontext.NewDecoder(bytes.NewReader(scanner.Bytes()), jsonv1.DefaultOptionsV1())
		if json.UnmarshalDecode(decoder, &event, jsonv1.DefaultOptionsV1(), json.RejectUnknownMembers(true)) != nil || !validAXReadEvent(event) {
			return invalid(errors.New("invalid AX read event"))
		}
		if json.UnmarshalDecode(decoder, new(any), jsonv1.DefaultOptionsV1()) != io.EOF {
			return invalid(errors.New("trailing AX read event data"))
		}
		reads, ok := result.Threads[event.ThreadID]
		if !ok {
			reads = unavailableAXReads()
			reads.State = "observed"
		}
		if event.Phase == "start" {
			if len(starts) >= 100000 {
				return invalid(errors.New("AX read evidence exceeds invocation limit"))
			}
			if _, exists := starts[event.ID]; exists {
				return invalid(errors.New("duplicate AX read start"))
			}
			starts[event.ID] = event
			reads.Started++
			reads.ByTool[event.Tool]++
			result.Threads[event.ThreadID] = reads
			continue
		}
		start, exists := starts[event.ID]
		// Wall timestamps may move backward; elapsed time is recorded monotonically.
		if !exists || start.Phase != "start" || start.ThreadID != event.ThreadID || start.Tool != event.Tool ||
			start.Schema != event.Schema || start.AXReadContext != event.AXReadContext {
			return invalid(errors.New("unpaired AX read finish"))
		}
		start.Phase = "finish"
		starts[event.ID] = start
		reads.Completed++
		if uint64(*event.DurationNS) > ^uint64(0)-reads.DurationNS {
			return invalid(errors.New("AX read durations overflow"))
		}
		reads.DurationNS += uint64(*event.DurationNS)
		if *event.Succeeded {
			reads.Succeeded++
		} else {
			reads.Failed++
			class := event.FailureClass
			reads.FailuresByClass[class]++
			if len(reads.Failures) < 256 {
				reads.Failures = append(reads.Failures, AXReadFailure{AXReadContext: event.AXReadContext,
					ID: event.ID, Tool: event.Tool, At: event.At, DurationNS: *event.DurationNS,
					Class: class, ExitCode: event.ExitCode})
			} else {
				reads.DroppedFailures++
			}
		}
		result.Threads[event.ThreadID] = reads
	}
	if err := scanner.Err(); err != nil {
		return invalid(errors.New("AX read log is unreadable or contains oversized events"))
	}
	if reader.N == 0 {
		return invalid(errors.New("AX read log exceeds 64 MiB"))
	}
	for id, reads := range result.Threads {
		reads.Incomplete = reads.Started - reads.Completed
		result.Threads[id] = reads
	}
	return result, nil
}

func validAXReadEvent(event axReadEvent) bool {
	if event.Schema != "mekugi.ax.read.v2" || event.ID == "" ||
		!validAXReader(event.Tool) || event.At.IsZero() || !validAXReadContext(event.AXReadContext) ||
		len(event.ThreadID) > 128 || strings.ContainsAny(event.ThreadID, "\r\n\x00") {
		return false
	}
	if event.Phase == "start" {
		return event.DurationNS == nil && event.Succeeded == nil && event.FailureClass == "" && event.ExitCode == nil
	}
	if event.Phase != "finish" || event.DurationNS == nil || *event.DurationNS < 0 || event.Succeeded == nil {
		return false
	}
	if event.ExitCode != nil && (*event.ExitCode < 0 || *event.ExitCode > 255 || *event.Succeeded && *event.ExitCode != 0) {
		return false
	}
	return *event.Succeeded && event.FailureClass == "" || !*event.Succeeded && validAXFailureClass(event.FailureClass)
}

func openAXEvidence(path string, limit int64) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		file.Close()
		return nil, errors.New("evidence must be a bounded regular file")
	}
	return file, nil
}
