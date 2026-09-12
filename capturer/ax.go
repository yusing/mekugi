package capturer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// AXReadOutputEnvironment opts executor-side private readers into a local journal.
const AXReadOutputEnvironment = "MEKUGI_AX_OUTPUT"

// AXCallIDEnvironment joins an executor observation to its logical tool call.
const AXCallIDEnvironment = "MEKUGI_AX_CALL_ID"

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

// AXReadObservation records one actual invocation, not a parsed source command.
// It retains no command, file path, source text or output in its journal.
type AXReadObservation struct {
	file    *os.File
	event   axReadEvent
	started time.Time
}

func validAXReader(tool string) bool {
	return tool == "hcat" || tool == "hgrep" || tool == "hsymbol" || tool == "inspect_file"
}

// StartAXRead is auxiliary to execution. Callers report failures separately and
// keep the original command outcome. Each event is one O_APPEND write.
func StartAXRead(path, threadID, tool string) (*AXReadObservation, error) {
	return StartAXReadWithContext(path, threadID, tool, AXReadContext{})
}

func StartAXReadWithContext(path, threadID, tool string, identity AXReadContext) (*AXReadObservation, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) || !validAXReader(tool) || len(threadID) > 128 ||
		strings.ContainsAny(threadID, "\r\n\x00") || !validAXReadContext(identity) {
		return nil, errors.New("invalid AX journal configuration")
	}
	file, err := openAXJournal(path)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		file.Close()
		return nil, err
	}
	now := time.Now()
	observation := &AXReadObservation{
		file: file, started: now,
		event: axReadEvent{AXReadContext: identity, Schema: "mekugi.ax.read.v2", ID: hex.EncodeToString(id),
			ThreadID: threadID, Tool: tool, Phase: "start", At: now.UTC()},
	}
	if err := observation.write(); err != nil {
		file.Close()
		return nil, err
	}
	return observation, nil
}

// PrepareAXReadJournal enables a debug bundle before the executor starts.
func PrepareAXReadJournal(path string) error {
	file, err := openAXJournal(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func openAXJournal(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("AX journal path must be absolute")
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
		return nil, errors.New("AX journal must be a private regular file below 64 MiB")
	}
	return file, nil
}

func (observation *AXReadObservation) write() (writeErr error) {
	data, err := json.Marshal(observation.event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Coordinate the capacity check and append across workers. Acquisition never
	// blocks in the kernel and has a small bounded retry budget.
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		err := syscall.Flock(int(observation.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	defer func() {
		writeErr = errors.Join(writeErr, syscall.Flock(int(observation.file.Fd()), syscall.LOCK_UN))
	}()
	info, err := observation.file.Stat()
	if err != nil || info.Size()+int64(len(data)) > maxAXEvidenceBytes {
		return errors.New("AX journal exceeds 64 MiB")
	}
	n, err := observation.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

// Finish preserves the boolean API. Older callers cannot supply a failure reason.
func (observation *AXReadObservation) Finish(succeeded bool) error {
	return observation.FinishResult(succeeded, "", nil)
}

// FinishResult closes the observation even if classification or writing fails.
// Only an allowlisted class and a process exit status may enter the journal.
func (observation *AXReadObservation) FinishResult(succeeded bool, failureClass string, exitCode *int) error {
	if observation == nil {
		return nil
	}
	if !succeeded && failureClass == "" {
		failureClass = "unknown"
	}
	if (succeeded && failureClass != "") || (!succeeded && !validAXFailureClass(failureClass)) ||
		(exitCode != nil && (*exitCode < 0 || *exitCode > 255 || succeeded && *exitCode != 0)) {
		return errors.Join(errors.New("invalid AX read result"), observation.file.Close())
	}
	observation.event.Phase = "finish"
	observation.event.At = time.Now().UTC()
	observation.event.DurationNS = new(time.Since(observation.started).Nanoseconds())
	observation.event.Succeeded = new(succeeded)
	observation.event.FailureClass = failureClass
	observation.event.ExitCode = exitCode
	return errors.Join(observation.write(), observation.file.Close())
}

func validAXFailureClass(value string) bool {
	switch value {
	case "unknown", "invalid_arguments", "not_found", "permission_denied", "not_regular",
		"invalid_source", "reader_error", "search_error", "resolver_error", "dependency_unavailable",
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

// AXReadFailure joins a bounded failure sample back to the journal and rollout.
type AXReadFailure struct {
	AXReadContext
	ID         string    `json:"id"`
	Tool       string    `json:"tool"`
	At         time.Time `json:"at"`
	DurationNS int64     `json:"duration_ns"`
	Class      string    `json:"class"`
	ExitCode   *int      `json:"exit_code,omitempty"`
}

// AXReadMetrics counts only journal-observed invocations, not external reads.
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

// AXReadJournal validates the complete journal before exposing any attribution.
// An empty thread key represents explicitly unattributed runtime evidence.
type AXReadJournal struct {
	Threads map[string]AXReadMetrics
}

func (journal AXReadJournal) ForThread(threadID string) AXReadMetrics {
	result := unavailableAXReads()
	if threadID != "" {
		if observed, ok := journal.Threads[threadID]; ok {
			result = observed
		}
	}
	for id, reads := range journal.Threads {
		if id == "" {
			result.UnattributedStarted += reads.Started
		} else if id != threadID {
			result.OtherThreadStarted += reads.Started
		}
	}
	return result
}

// ReadAXReads retains the one-thread API, but no longer silently hides exclusions.
func ReadAXReads(ctx context.Context, path, threadID string) (AXReadMetrics, error) {
	journal, err := ReadAXReadJournal(ctx, path)
	if err != nil {
		return unavailableAXReads(), err
	}
	return journal.ForThread(threadID), nil
}

func ReadAXReadJournal(ctx context.Context, path string) (AXReadJournal, error) {
	result := AXReadJournal{Threads: map[string]AXReadMetrics{}}
	invalid := func(err error) (AXReadJournal, error) { return AXReadJournal{}, err }
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
	finished := make(map[string]bool)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return invalid(err)
		}
		if !utf8.Valid(scanner.Bytes()) {
			return invalid(errors.New("invalid UTF-8 in AX read event"))
		}
		var event axReadEvent
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || !validAXReadEvent(event) {
			return invalid(errors.New("invalid AX read event"))
		}
		if decoder.Decode(new(any)) != io.EOF {
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
		if !exists || finished[event.ID] || start.ThreadID != event.ThreadID || start.Tool != event.Tool ||
			start.Schema != event.Schema || start.AXReadContext != event.AXReadContext {
			return invalid(errors.New("unpaired AX read finish"))
		}
		finished[event.ID] = true
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
			if class == "" {
				class = "unknown"
			} // Legacy v1 evidence is not reclassified.
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
		return invalid(errors.New("AX read journal is unreadable or contains oversized events"))
	}
	if reader.N == 0 {
		return invalid(errors.New("AX read journal exceeds 64 MiB"))
	}
	for id, reads := range result.Threads {
		reads.Incomplete = reads.Started - reads.Completed
		result.Threads[id] = reads
	}
	return result, nil
}

func validAXReadEvent(event axReadEvent) bool {
	if (event.Schema != "mekugi.ax.read.v1" && event.Schema != "mekugi.ax.read.v2") || event.ID == "" ||
		!validAXReader(event.Tool) || event.At.IsZero() || !validAXReadContext(event.AXReadContext) ||
		len(event.ThreadID) > 128 || strings.ContainsAny(event.ThreadID, "\r\n\x00") {
		return false
	}
	if event.Schema == "mekugi.ax.read.v1" && (event.FailureClass != "" || event.ExitCode != nil || event.AXReadContext != (AXReadContext{})) {
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
	if event.Schema == "mekugi.ax.read.v1" {
		return true
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

// AXEditMetrics reports measured emissions separately from defect judgments.
type AXEditMetrics struct {
	Calls              uint64 `json:"calls"`
	RecoveryRetries    uint64 `json:"recovery_retries"`
	EmittedBytes       uint64 `json:"emitted_bytes"`
	ReEmittedLineBytes uint64 `json:"re_emitted_line_bytes"`
	Rejected           uint64 `json:"rejected"`
	Unconfirmed        uint64 `json:"unconfirmed"`
}

// AXEditAccumulator holds only the preceding original payload for exact,
// line-aligned repetition measurement, never a durable script history.
type AXEditAccumulator struct {
	Metrics  AXEditMetrics
	previous string
}

func (accumulator *AXEditAccumulator) Observe(script string, retry, rejected, unconfirmed bool) {
	accumulator.Metrics.Calls++
	accumulator.Metrics.EmittedBytes += uint64(len(script))
	if retry {
		accumulator.Metrics.RecoveryRetries++
	}
	if rejected {
		accumulator.Metrics.Rejected++
	}
	if unconfirmed {
		accumulator.Metrics.Unconfirmed++
	}
	previous := make(map[string]int)
	for line := range strings.Lines(accumulator.previous) {
		previous[line]++
	}
	for line := range strings.Lines(script) {
		if previous[line] > 0 {
			accumulator.Metrics.ReEmittedLineBytes += uint64(len(line))
			previous[line]--
		}
	}
	accumulator.previous = script
}

type AXCompletionMetrics struct {
	State          string `json:"state"`
	CompletedTurns uint64 `json:"completed_turns"`
	DurationMS     int64  `json:"duration_ms"`
	UnpairedEvents uint64 `json:"unpaired_events"`
}

// AXCompletionAccumulator pairs actual task/turn events by identity. It does not
// infer completion from silence, a tool exit, or an assistant's prose.
type AXCompletionAccumulator struct {
	Metrics   AXCompletionMetrics
	starts    map[string]axCompletionStart
	completed map[string]bool
}

type axCompletionStart struct {
	kind string
	at   time.Time
}

func (accumulator *AXCompletionAccumulator) Observe(kind, id string, at time.Time) {
	if accumulator.starts == nil {
		accumulator.completed = make(map[string]bool)
		accumulator.starts = make(map[string]axCompletionStart)
	}
	if id == "" || at.IsZero() {
		accumulator.Metrics.UnpairedEvents++
		return
	}
	if accumulator.completed[id] {
		accumulator.Metrics.UnpairedEvents++
		return
	}
	if kind == "task_started" || kind == "turn_started" {
		if _, exists := accumulator.starts[id]; exists {
			accumulator.Metrics.UnpairedEvents++
			return
		}
		accumulator.starts[id] = axCompletionStart{kind: kind, at: at}
		return
	}
	start, exists := accumulator.starts[id]
	matchingKind := start.kind == "task_started" && kind == "task_complete" ||
		start.kind == "turn_started" && kind == "turn_complete"
	if !exists || !matchingKind || at.Before(start.at) {
		accumulator.Metrics.UnpairedEvents++
		return
	}
	delete(accumulator.starts, id)
	accumulator.completed[id] = true
	accumulator.Metrics.CompletedTurns++
	durationMS := at.Sub(start.at).Milliseconds()
	if durationMS > math.MaxInt64-accumulator.Metrics.DurationMS {
		accumulator.Metrics.DurationMS = math.MaxInt64
	} else {
		accumulator.Metrics.DurationMS += durationMS
	}
}

func (accumulator *AXCompletionAccumulator) Result() AXCompletionMetrics {
	result := accumulator.Metrics
	result.UnpairedEvents += uint64(len(accumulator.starts))
	result.State = "unavailable"
	if result.CompletedTurns > 0 || result.UnpairedEvents > 0 {
		result.State = "observed"
	}
	return result
}

type AXDefectAssessment struct {
	CallID   string `json:"call_id"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
	SHA256   string `json:"sha256"`
}

type AXDefectMetrics struct {
	AssessedCalls uint64               `json:"assessed_calls"`
	Reported      uint64               `json:"reported_defects"`
	Unassessed    uint64               `json:"unassessed_calls"`
	Assessments   []AXDefectAssessment `json:"assessments"`
}

// ReadAXDefects requires a real evidence artifact for every explicit verdict.
// It records provenance, not a claim that a test failure proves edit causality.
func ReadAXDefects(path string, editCalls map[string]bool) (AXDefectMetrics, error) {
	result := AXDefectMetrics{Unassessed: uint64(len(editCalls)), Assessments: []AXDefectAssessment{}}
	if path == "" {
		return result, nil
	}
	file, err := openAXEvidence(path, 1<<20)
	if err != nil {
		return result, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (1<<20)+1))
	decoder.DisallowUnknownFields()
	var assessments []struct {
		CallID   string `json:"call_id"`
		Verdict  string `json:"verdict"`
		Evidence string `json:"evidence"`
	}
	if err := decoder.Decode(&assessments); err != nil {
		return result, errors.New("invalid defect assessment JSON")
	}
	if assessments == nil {
		return result, errors.New("defect assessments must be an array")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return result, errors.New("trailing defect assessment data")
	}
	seen := make(map[string]bool)
	for _, assessment := range assessments {
		if !editCalls[assessment.CallID] || seen[assessment.CallID] ||
			(assessment.Verdict != "defect" && assessment.Verdict != "no_defect") || assessment.Evidence == "" {
			return result, errors.New("defect assessment needs one known edit call, verdict, and evidence path")
		}
		evidencePath := assessment.Evidence
		if !filepath.IsAbs(evidencePath) {
			evidencePath = filepath.Join(filepath.Dir(path), evidencePath)
		}
		evidencePath, err = filepath.Abs(evidencePath)
		if err != nil {
			return result, err
		}
		evidence, err := openAXEvidence(evidencePath, 1<<20)
		if err != nil {
			return result, fmt.Errorf("defect evidence for call %q is unavailable", assessment.CallID)
		}
		data, readErr := io.ReadAll(io.LimitReader(evidence, (1<<20)+1))
		closeErr := evidence.Close()
		if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > 1<<20 {
			return result, errors.New("defect evidence must be nonempty and at most 1 MiB")
		}
		digest := sha256.Sum256(data)
		result.Assessments = append(result.Assessments, AXDefectAssessment{
			CallID: assessment.CallID, Verdict: assessment.Verdict, Evidence: evidencePath, SHA256: hex.EncodeToString(digest[:]),
		})
		seen[assessment.CallID] = true
		result.AssessedCalls++
		result.Unassessed--
		if assessment.Verdict == "defect" {
			result.Reported++
		}
	}
	return result, nil
}
