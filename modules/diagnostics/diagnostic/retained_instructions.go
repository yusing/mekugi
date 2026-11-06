package diagnostic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

const (
	instructionIndexVersion = 1
	// Index and revision records grow linearly with uses and items.
	instructionRecordBytes = 4 << 20
	// Each cache entry is fixed-size; evicting one costs a later locked read.
	instructionCacheThreads = 4096
)

// An index lists a thread's revisions and uses. A revision record holds one
// revision's content digests; content records hold single JSON values. Revision
// and content records are content-addressed and shared across threads.
var (
	InstructionIndexRecord    = recordkind.Spec{ID: "instructions", Match: recordkind.HashedName("instructions-", ".json"), Dependencies: instructionIndexDependencies, MaxRecordBytes: instructionRecordBytes}
	InstructionRevisionRecord = recordkind.Spec{ID: "instruction-revision", Match: recordkind.HashedName("instruction-revision-", ".json"), Dependencies: instructionRevisionDependencies, MaxRecordBytes: instructionRecordBytes}
	InstructionContentRecord  = recordkind.Spec{ID: "instruction-content", Match: recordkind.HashedName("instruction-content-", ".json")}
)

// RecordKinds returns the retained record kinds this module owns.
func RecordKinds() []recordkind.Spec {
	return []recordkind.Spec{Failures, InstructionIndexRecord, InstructionRevisionRecord, InstructionContentRecord}
}

// InstructionField classifies one model-visible value.
type InstructionField uint8

const (
	InstructionText InstructionField = iota
	DeveloperMessage
	ToolDescription
	AdditionalTool
)

// InstructionVisitor receives values borrowed for the duration of Visit.
type InstructionVisitor interface {
	Visit(InstructionField, jsontext.Value)
}

// InstructionRequest identifies one forwarded request. Content visits its
// model-visible values in request order and may run more than once; on error,
// visited values are discarded and nothing is published.
type InstructionRequest struct {
	Thread, Model, TurnID, RequestID string
	Time                             time.Time
	RequestSequence                  uint64
	Content                          func(InstructionVisitor) error
}

// InstructionRevision references content digests in request order.
type InstructionRevision struct {
	Instructions      string   `json:"instructions,omitempty"`
	DeveloperMessages []string `json:"developer_messages,omitempty"`
	Tools             []string `json:"tools,omitempty"`
	AdditionalTools   []string `json:"additional_tools,omitempty"`
}

// InstructionUse records the first request that used a revision after a
// different one. Later requests use it until the next use entry.
type InstructionUse struct {
	Revision        int       `json:"revision"`
	Time            time.Time `json:"time"`
	Model           string    `json:"model,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	RequestID       string    `json:"request_id,omitempty"`
	RequestSequence uint64    `json:"request_sequence,omitzero"`
}

type instructionIndex struct {
	Version   int              `json:"version"`
	Thread    string           `json:"thread"`
	Revisions []string         `json:"revisions"`
	Uses      []InstructionUse `json:"uses"`
}

func instructionIndexName(thread string) string {
	return fmt.Sprintf("instructions-%x.json", sha256.Sum256([]byte(thread)))
}

func instructionRevisionName(digest string) string {
	return "instruction-revision-" + digest + ".json"
}

func instructionContentName(digest string) string {
	return "instruction-content-" + digest + ".json"
}

func (r InstructionRevision) digests() []string {
	var digests []string
	if r.Instructions != "" {
		digests = append(digests, r.Instructions)
	}
	for _, values := range [][]string{r.DeveloperMessages, r.Tools, r.AdditionalTools} {
		digests = append(digests, values...)
	}
	slices.Sort(digests)
	return slices.Compact(digests)
}

func validDigest(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && err == nil && strings.ToLower(value) == value
}

func decodeInstructionIndex(data []byte) (instructionIndex, error) {
	var index instructionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return index, fmt.Errorf("invalid retained instruction index: %w", err)
	}
	if index.Version != instructionIndexVersion || index.Thread == "" || len(index.Revisions) == 0 || len(index.Uses) == 0 ||
		slices.ContainsFunc(index.Revisions, func(digest string) bool { return !validDigest(digest) }) {
		return index, errors.New("invalid retained instruction index")
	}
	for _, use := range index.Uses {
		if use.Revision < 1 || use.Revision > len(index.Revisions) || use.Time.IsZero() {
			return index, errors.New("invalid retained instruction use")
		}
	}
	return index, nil
}

func decodeInstructionRevision(data []byte) (InstructionRevision, error) {
	var revision InstructionRevision
	if err := json.Unmarshal(data, &revision); err != nil {
		return revision, fmt.Errorf("invalid retained instruction revision: %w", err)
	}
	if slices.ContainsFunc(revision.digests(), func(digest string) bool { return !validDigest(digest) }) {
		return revision, errors.New("invalid retained instruction content reference")
	}
	return revision, nil
}

func instructionIndexDependencies(record recordkind.Record) ([]string, error) {
	index, err := decodeInstructionIndex(record.Data)
	if err != nil {
		return nil, err
	}
	if record.Name != instructionIndexName(index.Thread) {
		return nil, errors.New("retained instruction index does not match its thread")
	}
	names := make([]string, 0, len(index.Revisions))
	for _, digest := range index.Revisions {
		names = append(names, instructionRevisionName(digest))
	}
	return names, nil
}

func instructionRevisionDependencies(record recordkind.Record) ([]string, error) {
	revision, err := decodeInstructionRevision(record.Data)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, digest := range revision.digests() {
		names = append(names, instructionContentName(digest))
	}
	return names, nil
}

// readInstructionIndex returns found=false only when the thread has no index.
func readInstructionIndex(directory, thread string) (instructionIndex, bool, error) {
	data, err := history.ReadRecord(filepath.Join(directory, instructionIndexName(thread)))
	if errors.Is(err, os.ErrNotExist) {
		return instructionIndex{}, false, nil
	}
	if err != nil {
		return instructionIndex{}, false, err
	}
	index, err := decodeInstructionIndex(data)
	if err == nil && index.Thread != thread {
		err = errors.New("retained instruction index does not match its thread")
	}
	return index, err == nil, err
}

// readAddressed reads a content-addressed record and verifies its digest.
func readAddressed(directory, name, digest string) ([]byte, error) {
	// Content may preserve admitted JSON with invalid UTF-8, so the ordinary
	// UTF-8-only history reader cannot read every value retention publishes.
	file, err := persistence.OpenRetained(filepath.Join(directory, name), history.MaxRecordBytes)
	if err != nil {
		return nil, fmt.Errorf("retained instruction record %s: %w", name, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, history.MaxRecordBytes+1))
	if err != nil {
		return nil, fmt.Errorf("retained instruction record %s: %w", name, err)
	}
	if len(data) > history.MaxRecordBytes || instructionDigest(jsontext.Value(data)) != digest ||
		!jsontext.Value(data).IsValid(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)) {
		return nil, fmt.Errorf("retained instruction record %s does not match its digest", name)
	}
	return data, nil
}

// canonicalInstruction copies value into buffer as RFC 8785 canonical JSON, so
// member order and formatting do not change a value's digest. A value that
// cannot be canonicalized, such as one with duplicate names or invalid UTF-8,
// keeps its exact bytes.
func canonicalInstruction(buffer jsontext.Value, value jsontext.Value) jsontext.Value {
	buffer = append(buffer[:0], value...)
	_ = buffer.Canonicalize()
	return buffer
}

// fingerprint hashes per-value canonical digests through reused buffers, so
// unchanged requests do not allocate per value for copies or digests.
type fingerprint struct {
	hash      hash.Hash
	scratch   [1 + sha256.Size]byte
	canonical jsontext.Value
}

func (f *fingerprint) Visit(field InstructionField, value jsontext.Value) {
	f.scratch[0] = byte(field)
	f.canonical = canonicalInstruction(f.canonical, value)
	digest := sha256.Sum256(f.canonical)
	copy(f.scratch[1:], digest[:])
	f.hash.Write(f.scratch[:])
}

// revisionBuilder copies borrowed values only when a revision is published.
type revisionBuilder struct {
	revision InstructionRevision
	content  map[string]jsontext.Value
}

func (b *revisionBuilder) Visit(field InstructionField, value jsontext.Value) {
	canonical := canonicalInstruction(nil, value)
	digest := instructionDigest(canonical)
	b.content[digest] = canonical
	switch field {
	case InstructionText:
		b.revision.Instructions = digest
	case DeveloperMessage:
		b.revision.DeveloperMessages = append(b.revision.DeveloperMessages, digest)
	case ToolDescription:
		b.revision.Tools = append(b.revision.Tools, digest)
	case AdditionalTool:
		b.revision.AdditionalTools = append(b.revision.AdditionalTools, digest)
	}
}

type activeInstructions struct {
	fingerprint [sha256.Size]byte
	path        string
	index       os.FileInfo
}

// InstructionRetention publishes changed projected instructions into one
// retained-session store. Per thread it remembers the active content's
// fingerprint and the index file that recorded it, so unchanged requests do
// no locked reads or writes until that index is replaced or removed.
type InstructionRetention struct {
	store   *history.Store
	writes  *persistence.Counter
	limits  history.Limits
	mu      sync.Mutex
	current map[string]activeInstructions
}

// RetainInstructions borrows the composed retained-session store.
func (*Service) RetainInstructions(store *history.Store, writes *persistence.Counter, limits history.Limits) *InstructionRetention {
	if store == nil {
		return nil
	}
	return &InstructionRetention{store: store, writes: writes, limits: limits, current: make(map[string]activeInstructions)}
}

// Observe retains request content when it differs from the thread's latest
// retained revision. Failed publication updates nothing, so the next request
// publishes its own content.
func (r *InstructionRetention) Observe(ctx context.Context, request InstructionRequest) error {
	if r == nil || request.Thread == "" || request.Content == nil {
		return nil
	}
	f := &fingerprint{hash: sha256.New()}
	if err := request.Content(f); err != nil {
		return err
	}
	var sum [sha256.Size]byte
	f.hash.Sum(sum[:0])
	r.mu.Lock()
	active, known := r.current[request.Thread]
	r.mu.Unlock()
	if known && active.fingerprint == sum {
		// Publication replaces the index file, so its identity detects peer
		// publication, reclamation and expiry.
		if info, err := os.Lstat(active.path); err == nil && os.SameFile(info, active.index) {
			return nil
		}
	}
	return r.store.Transaction(ctx, r.writes, func(tx *history.Transaction) error {
		return r.publish(tx, request, sum)
	})
}

func (r *InstructionRetention) publish(tx *history.Transaction, request InstructionRequest, sum [sha256.Size]byte) error {
	builder := revisionBuilder{content: make(map[string]jsontext.Value)}
	if err := request.Content(&builder); err != nil {
		return err
	}
	revisionData, err := json.Marshal(&builder.revision, json.Deterministic(true))
	if err != nil {
		return err
	}
	revisionDigest := instructionDigest(revisionData)
	directory := r.store.Directory()
	index, found, err := readInstructionIndex(directory, request.Thread)
	if err != nil {
		return err
	}
	if !found {
		index = instructionIndex{Version: instructionIndexVersion, Thread: request.Thread}
	}
	number := slices.Index(index.Revisions, revisionDigest) + 1
	if number == 0 {
		index.Revisions = append(index.Revisions, revisionDigest)
		number = len(index.Revisions)
	}
	name := instructionIndexName(request.Thread)
	if len(index.Uses) == 0 || index.Uses[len(index.Uses)-1].Revision != number {
		requestTime := request.Time
		if requestTime.IsZero() {
			requestTime = time.Now()
		}
		index.Uses = append(index.Uses, InstructionUse{Revision: number, Time: requestTime.UTC(), Model: request.Model,
			TurnID: request.TurnID, RequestID: request.RequestID, RequestSequence: request.RequestSequence})
		if err := r.write(tx, request.Thread, name, index, builder, revisionDigest, revisionData); err != nil {
			return err
		}
	}
	// Recording the active revision under publication serialization keeps
	// concurrent requests on one thread in publication order.
	path := filepath.Join(directory, name)
	if info, err := os.Lstat(path); err == nil {
		r.mu.Lock()
		if _, known := r.current[request.Thread]; !known && len(r.current) >= instructionCacheThreads {
			for thread := range r.current {
				delete(r.current, thread)
				break
			}
		}
		r.current[request.Thread] = activeInstructions{fingerprint: sum, path: path, index: info}
		r.mu.Unlock()
	}
	return nil
}

func (r *InstructionRetention) write(tx *history.Transaction, thread, name string, index instructionIndex, builder revisionBuilder, revisionDigest string, revisionData []byte) error {
	data, err := json.Marshal(&index, json.Deterministic(true))
	if err != nil {
		return err
	}
	digests := builder.revision.digests()
	names := make([]string, 0, len(digests)+2)
	for _, digest := range digests {
		names = append(names, instructionContentName(digest))
	}
	revisionName := instructionRevisionName(revisionDigest)
	// Ownership precedes publication; dependencies precede their referrers.
	if err := tx.Retain(thread, r.limits, append(names, revisionName, name)...); err != nil {
		return err
	}
	directory := r.store.Directory()
	writeAbsent := func(name, pattern string, value []byte) error {
		if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := tx.Admit(r.limits, name, int64(len(value))); err != nil {
			return err
		}
		return tx.Write(name, pattern, value)
	}
	for _, digest := range digests {
		if err := writeAbsent(instructionContentName(digest), "instruction-content-pending-", builder.content[digest]); err != nil {
			return err
		}
	}
	if err := writeAbsent(revisionName, "instruction-revision-pending-", revisionData); err != nil {
		return err
	}
	if err := tx.Admit(r.limits, name, int64(len(data))); err != nil {
		return err
	}
	return tx.Write(name, "instructions-pending-", data)
}

// InstructionReport describes one thread's retained projected instructions.
// Revisions reference Content by digest so shared text appears once.
type InstructionReport struct {
	Status    string                      `json:"status"`
	Reason    string                      `json:"reason,omitempty"`
	Revisions []InstructionRevisionReport `json:"revisions,omitempty"`
	Uses      []InstructionUse            `json:"uses,omitempty"`
	Content   map[string]query.Text       `json:"content,omitempty"`
}

type InstructionRevisionReport struct {
	Revision            int `json:"revision"`
	InstructionRevision `json:",inline"`
}

const (
	InstructionsAvailable   = "available"
	InstructionsUnavailable = "unavailable"
	InstructionsInvalid     = "invalid"
)

var instructionsUnavailable = fmt.Sprintf("not retained for this thread: recorded only for requests Mekugi forwarded since retention began; removed after %d inactive days or under storage pressure",
	int(history.SessionRetention/(24*time.Hour)))

// ReadInstructions reads a thread's retained instructions without effects.
// A missing index is unavailable evidence; invalid or incomplete records fail.
func (*Service) ReadInstructions(ctx context.Context, directory, thread string, includeText bool, textBytes int) (InstructionReport, error) {
	if err := ctx.Err(); err != nil {
		return InstructionReport{}, err
	}
	index, found, err := readInstructionIndex(directory, thread)
	if err != nil {
		return InstructionReport{}, err
	}
	if !found {
		return InstructionReport{Status: InstructionsUnavailable, Reason: instructionsUnavailable}, nil
	}
	report := InstructionReport{Status: InstructionsAvailable, Uses: index.Uses, Content: make(map[string]query.Text)}
	for number, revisionDigest := range index.Revisions {
		if err := ctx.Err(); err != nil {
			return InstructionReport{}, err
		}
		data, err := readAddressed(directory, instructionRevisionName(revisionDigest), revisionDigest)
		if err != nil {
			return InstructionReport{}, err
		}
		revision, err := decodeInstructionRevision(data)
		if err != nil {
			return InstructionReport{}, err
		}
		report.Revisions = append(report.Revisions, InstructionRevisionReport{Revision: number + 1, InstructionRevision: revision})
		for _, digest := range revision.digests() {
			if _, done := report.Content[digest]; done {
				continue
			}
			data, err := readAddressed(directory, instructionContentName(digest), digest)
			if err != nil {
				return InstructionReport{}, err
			}
			// JSON strings display as their decoded text; other values as stored JSON.
			text := strings.ToValidUTF8(string(data), "\ufffd")
			var decoded string
			if json.Unmarshal(data, &decoded, jsontext.AllowInvalidUTF8(true)) == nil {
				text = decoded
			}
			report.Content[digest] = query.TextValue(text, includeText, textBytes)
		}
	}
	return report, nil
}
