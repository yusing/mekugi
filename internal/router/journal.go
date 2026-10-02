package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/yusing/mekugi/capturer"
)

const (
	maxJournalItems     = 512
	maxJournalItemBytes = 16 << 10
	// Terminal delivery is independent of the live progress budget. The extra
	// space covers indentation of every content line, Q&A labels, item IDs,
	// and the bounded canonical agent name.
	maxJournalFlushBytes = maxJournalItems*(3*maxJournalItemBytes+128) + maxJournalItemBytes
	// Lists are a subset of the persisted record, encoded with the same escaping.
	// Reserve a small allowance for the response envelope instead of estimating
	// expanded item text or repeating a second author/content capacity model.
	maxJournalPublicationResponseBytes = maxReplayRecordBytes + 1024
)

// errJournalUnchanged skips publication after the locked durable read.
var errJournalUnchanged = errors.New("journal unchanged")
var errJournalEventLimit = errors.New("journal event capacity reached; retained notes and transitions cannot be discarded")
var errJournalItemLimit = errors.New("journal item limit reached")

type journalMutation struct {
	Agent            string           `json:"agent,omitempty"`
	P                string           `json:"p,omitempty"`
	Under            string           `json:"under,omitempty"`
	Kind             string           `json:"kind,omitempty"`
	Title            *string          `json:"title,omitempty"`
	Body             *string          `json:"body,omitempty"`
	State            *string          `json:"state,omitempty"`
	Reason           *string          `json:"reason,omitempty"`
	Before           string           `json:"before,omitempty"`
	Reset            string           `json:"reset,omitempty"`
	Tasks            []jsontext.Value `json:"tasks,omitempty"`
	Op               string           `json:"op"`
	ID               string           `json:"id,omitempty"`
	Text             *string          `json:"text,omitempty"`
	Answer           *bool            `json:"answer,omitempty"`
	inferredQuestion string
	ReportNow        bool `json:"report_now,omitzero"`
}

type journalItem struct {
	Path         string        `json:"path,omitempty"`
	Kind         string        `json:"kind,omitempty"`
	Title        string        `json:"title,omitempty"`
	Body         string        `json:"body,omitempty"`
	State        string        `json:"state,omitempty"`
	Reason       string        `json:"reason,omitempty"`
	Agent        string        `json:"agent,omitempty"`
	CreatedAt    string        `json:"created_at,omitempty"`
	UpdatedAt    string        `json:"updated_at,omitempty"`
	Started      *journalStamp `json:"started,omitempty"`
	Finished     *journalStamp `json:"finished,omitempty"`
	TerminalOnly bool          `json:"terminal_only,omitzero"`
	Turns        int           `json:"turns,omitzero"` // Mounted agents only; view-only.
	ID           string        `json:"id"`
	Text         string        `json:"text"`
	Question     string        `json:"question,omitempty"`
	Author       string        `json:"author"`
	Created      uint64        `json:"created"`
	Updated      uint64        `json:"updated"`
	ReportNow    bool          `json:"report_now"`
	Reported     bool          `json:"reported"`
	Flushed      bool          `json:"flushed"`
	// A later silent edit does not erase the fact that the user saw this ID.
	EverReported     bool `json:"ever_reported,omitzero"`
	RootEverReported bool `json:"root_ever_reported,omitzero"`
}

type journalReceipt struct {
	Digest string   `json:"digest"`
	IDs    []string `json:"ids"`
}

type journalRetraction struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
}
type journalSpawnRole struct {
	Role       string `json:"role,omitempty"`
	Conflicted bool   `json:"conflicted,omitzero"`
}

type threadJournal struct {
	Counters             *capturer.JournalMetrics    `json:"counters,omitempty"`
	CounterReceipts      []string                    `json:"counter_receipts,omitempty"`
	TurnID               string                      `json:"turn_id,omitempty"`
	Turns                int                         `json:"turns,omitzero"` // Distinct host turn IDs seen.
	TurnStartSeq         uint64                      `json:"turn_start_seq,omitzero"`
	ResetHandledTurn     string                      `json:"reset_handled_turn,omitempty"`
	ResetIntent          *journalResetIntent         `json:"reset_intent,omitempty"`
	EvidenceKnown        bool                        `json:"evidence_known,omitzero"`
	EvidenceChangeSeq    uint64                      `json:"evidence_change_seq,omitzero"`
	EvidenceCaptureOrder uint64                      `json:"evidence_capture_order,omitzero"`
	mountUnavailable     string                      // View-only diagnostic; never persisted.
	LifecycleState       string                      `json:"lifecycle_state,omitempty"`
	LifecycleReason      string                      `json:"lifecycle_reason,omitempty"`
	LifecycleAt          string                      `json:"lifecycle_at,omitempty"`
	LegacyLive           map[uint64]bool             `json:"legacy_live,omitempty"`
	LegacyFlush          map[uint64]bool             `json:"legacy_flush,omitempty"`
	TreeAuthored         bool                        `json:"tree_authored,omitzero"`
	Events               []journalEvent              `json:"events,omitempty"`
	NextOrdinal          map[string]uint64           `json:"next_ordinal,omitempty"`
	SliceParents         map[string]bool             `json:"slice_parents,omitempty"`
	LiveSeq              uint64                      `json:"live_seq,omitzero"`
	RootLiveSeq          uint64                      `json:"root_live_seq,omitzero"`
	FlushSeq             uint64                      `json:"flush_seq,omitzero"`
	Parent               string                      `json:"parent,omitempty"`
	IdentityKnown        bool                        `json:"identity_known,omitzero"`
	IdentityConflicted   bool                        `json:"identity_conflicted,omitzero"`
	Version              int                         `json:"version"`
	Workspace            string                      `json:"workspace"`
	Thread               string                      `json:"thread"`
	SpawnBaselineKnown   bool                        `json:"spawn_baseline_known,omitzero"`
	SpawnBaseline        map[string]bool             `json:"spawn_baseline,omitempty"`
	SpawnRoles           map[string]journalSpawnRole `json:"spawn_roles,omitempty"`
	SpawnRole            string                      `json:"-"`
	Author               string                      `json:"author"`
	Sequence             uint64                      `json:"sequence"`
	ResultSeq            uint64                      `json:"result_seq,omitzero"`
	ResultChangeSeq      uint64                      `json:"result_change_seq,omitzero"`
	ResultCount          uint64                      `json:"result_count,omitzero"`
	NextID               uint64                      `json:"next_id"`
	Items                []journalItem               `json:"items"`
	Retractions          []journalRetraction         `json:"retractions,omitempty"`
	Receipts             map[string]journalReceipt   `json:"receipts"`
}

func (j threadJournal) clone() threadJournal {
	j.Counters = j.Counters.Clone()
	j.CounterReceipts = slices.Clone(j.CounterReceipts)
	if j.ResetIntent != nil {
		intent := *j.ResetIntent
		j.ResetIntent = &intent
	}
	j.LegacyLive = maps.Clone(j.LegacyLive)
	j.LegacyFlush = maps.Clone(j.LegacyFlush)
	j.Events = slices.Clone(j.Events)
	j.NextOrdinal = maps.Clone(j.NextOrdinal)
	j.SliceParents = maps.Clone(j.SliceParents)
	j.Retractions = slices.Clone(j.Retractions)
	j.Items = slices.Clone(j.Items)
	j.Receipts = maps.Clone(j.Receipts)
	j.SpawnBaseline = maps.Clone(j.SpawnBaseline)
	j.SpawnRoles = maps.Clone(j.SpawnRoles)
	return j
}

// journalStore owns mutable journal state, separately from immutable tool replay.
// The replay store's filesystem lock serializes journal transactions across router
// processes too. Journal files share the managed session storage budget.
type journalStore struct {
	deliveryMu    sync.Mutex
	deliveryGates map[string]*journalDeliveryGate
	stateGate     chan struct{}
	memory        map[string]threadJournal
	nativeMu      sync.Mutex
	native        map[string]*nativeJournalSink
}

type journalDeliveryGate struct {
	gate  chan struct{}
	users int
}

func newJournalStore() *journalStore {
	return &journalStore{memory: make(map[string]threadJournal), deliveryGates: make(map[string]*journalDeliveryGate), stateGate: make(chan struct{}, 1)}
}

// Keep the complete state transaction serialized without trapping canceled callers
// behind another request's replay-lock wait.
func (s *journalStore) lockState(ctx context.Context) (func(), error) {
	if latency := journalLatencyFor(ctx); latency != nil {
		started := time.Now()
		defer func() { latency.stateWait += time.Since(started) }()
	}
	select {
	case s.stateGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.stateGate
			return nil, err
		}
		return func() { <-s.stateGate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Serialize mutations with in-flight delivery. The transport holds this lease
// from its snapshot through write confirmation, so deletion cannot overtake a
// notice and lose the required retraction. Workspace ancestry shares a lease;
// unrelated workspaces do not wait for each other's downstream writes.
// Waiting remains request-cancellable.
func (s *journalStore) lockDelivery(ctx context.Context, store *mekugiReplayStore, workspace string) (func(), error) {
	if latency := journalLatencyFor(ctx); latency != nil {
		started := time.Now()
		defer func() { latency.deliveryWait += time.Since(started) }()
	}
	s.deliveryMu.Lock()
	gate := s.deliveryGates[workspace]
	if gate == nil {
		gate = &journalDeliveryGate{gate: make(chan struct{}, 1)}
		s.deliveryGates[workspace] = gate
	}
	gate.users++
	s.deliveryMu.Unlock()
	drop := func() {
		s.deliveryMu.Lock()
		gate.users--
		if gate.users == 0 {
			delete(s.deliveryGates, workspace)
		}
		s.deliveryMu.Unlock()
	}
	select {
	case gate.gate <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
	release := func() { <-gate.gate; drop() }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if store == nil {
		return release, nil
	}
	// Keep a separate lock from replay transactions: delivery must be able to
	// retain provenance and acknowledge revisions while excluding mutations
	// from other router processes.
	path := filepath.Join(store.directory, fmt.Sprintf("journal-delivery-%x.lock", sha256.Sum256([]byte(workspace))))
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		release()
		return nil, errors.New("journal delivery lock is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		release()
		return nil, err
	}
	lock := flock.New(path, flock.SetPermissions(0600))
	ok, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil || !ok {
		release()
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	return func() {
		_ = lock.Unlock()
		release()
	}, nil
}

func journalKey(workspace, thread string) string { return workspace + "\x00" + thread }

func journalFilename(workspace, thread string) string {
	return fmt.Sprintf("journal-%x.json", sha256.Sum256([]byte(journalKey(workspace, thread))))
}

func readThreadJournal(store *mekugiReplayStore, workspace, thread string) (threadJournal, bool, error) {
	journal, exists, err := readJournalRecord(filepath.Join(store.directory, journalFilename(workspace, thread)))
	if err == nil && exists && (journal.Workspace != workspace || journal.Thread != thread) {
		return threadJournal{}, false, errors.New("journal identity mismatch")
	}
	return journal, exists, err
}

func readJournalRecord(path string) (threadJournal, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return threadJournal{}, false, nil
	}
	if err != nil {
		return threadJournal{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxReplayRecordBytes {
		return threadJournal{}, false, errors.New("invalid journal record")
	}
	file, err := os.Open(path)
	if err != nil {
		return threadJournal{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReplayRecordBytes+1))
	if err != nil {
		return threadJournal{}, false, err
	}
	var journal threadJournal
	if len(data) > maxReplayRecordBytes || json.Unmarshal(data, &journal) != nil ||
		(journal.Version != 1 && journal.Version != 2) || journal.Thread == "" || filepath.Base(path) != journalFilename(journal.Workspace, journal.Thread) {
		return threadJournal{}, false, errors.New("corrupt journal record")
	}
	if len(journal.Items) > maxJournalItems || journal.Receipts == nil {
		// A fully decoded, filename-verified identity can establish whose
		// content failed validation, without trusting partially decoded JSON.
		return journal, true, errors.New("corrupt journal contents")
	}
	journal.ensureTree()
	return journal, true, nil
}

func writeThreadJournal(store *mekugiReplayStore, journal threadJournal) error {
	data, err := marshalProtocolJSON(journal)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return storageCapacityError("journal record", int64(len(data)), maxReplayRecordBytes, "Delete obsolete journal items or continue in a new chat.")
	}
	return store.writeManagedFile(journalFilename(journal.Workspace, journal.Thread), "journal-pending-", data)
}

func (s *journalStore) transaction(ctx context.Context, store *mekugiReplayStore, workspace, thread string, mutate func(*threadJournal, bool) error) error {
	if strings.TrimSpace(thread) == "" {
		return errors.New("journal requires a stable thread ID")
	}
	store = store.scoped(ctx)
	release, err := s.lockState(ctx)
	if err != nil {
		return err
	}
	defer release()
	latency := journalLatencyFor(ctx)
	run := func() error {
		key := journalKey(workspace, thread)
		current, exists := s.memory[key]
		if store != nil {
			var err error
			current, exists, err = readThreadJournal(store, workspace, thread)
			if err != nil {
				return err
			}
		}
		if store != nil && exists {
			if err := store.retainJournalDependencies(current); err != nil {
				return err
			}
		}
		next := current.clone()
		if err := mutate(&next, exists); err != nil {
			if errors.Is(err, errJournalUnchanged) {
				if store == nil {
					s.memory[key] = current
				}
				return nil
			}
			return err
		}
		if store != nil {
			if next.Sequence != current.Sequence {
				// Recovery evidence is auxiliary: an unreadable boundary widens
				// the next summary instead of blocking the journal write.
				next.EvidenceKnown, next.EvidenceChangeSeq, next.EvidenceCaptureOrder = false, 0, 0
				evidence := *store
				namespace, err := store.namespaceForThread(thread)
				if err == nil {
					evidence.session.Namespace = namespace
					index, indexErr := evidence.readChangeIndex(workspace)
					order, orderErr := store.currentCaptureOrder()
					if indexErr == nil && orderErr == nil {
						next.EvidenceKnown, next.EvidenceChangeSeq, next.EvidenceCaptureOrder = true, index.Sequence, order
					}
				}
			}
			writeStarted := time.Now()
			err := writeThreadJournal(store, next)
			if latency != nil {
				latency.persistWrite += time.Since(writeStarted)
			}
			if err != nil {
				return err
			}
		}
		if store == nil {
			s.memory[key] = next
		}
		if sink := s.nativeSink(workspace, thread); sink != nil {
			sink.publish(next, false)
		}
		if mountedViewChanged(current, next) {
			s.publishMountedViews(store, workspace, thread)
		}
		return nil
	}
	if store != nil {
		return store.locked(ctx, run)
	}
	return run()
}

// Initialize ordinary forks exactly once from the source's latest committed
// journal. Subagent starts do not import their parent's journal.
func (s *journalStore) initialize(ctx context.Context, store *mekugiReplayStore, workspace, thread, author, fork string) error {
	store = store.scoped(ctx)
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if exists {
			return errJournalUnchanged
		}
		*j = threadJournal{Version: 2, Workspace: workspace, Thread: thread, Author: author, Items: []journalItem{}, Receipts: make(map[string]journalReceipt)}
		if fork == "" {
			return nil
		}
		if fork == thread {
			return errors.New("journal fork cannot refer to itself")
		}
		source, ok := s.memory[journalKey(workspace, fork)]
		if store != nil {
			var err error
			source, ok, err = readThreadJournal(store, workspace, fork)
			if err != nil {
				return err
			}
		}
		if !ok {
			return nil
		}
		if err := store.retainJournalDependencies(source); err != nil {
			return err
		}
		j.LegacyLive, j.LegacyFlush = maps.Clone(source.LegacyLive), maps.Clone(source.LegacyFlush)
		j.LiveSeq, j.FlushSeq = source.LiveSeq, source.FlushSeq
		j.TreeAuthored = source.TreeAuthored
		j.Events = slices.Clone(source.Events)
		for i := range j.Events {
			j.Events[i].RootRetraction = false
		}
		j.NextOrdinal = maps.Clone(source.NextOrdinal)
		j.SliceParents = maps.Clone(source.SliceParents)
		j.Items = slices.Clone(source.Items)
		// A fork copies facts and task states, not authority over children of
		// another parent. Historical bindings remain in the copied event log.
		for i := range j.Items {
			j.Items[i].Agent = ""
			j.Items[i].RootEverReported = false
		}
		j.Sequence, j.NextID = source.Sequence, source.NextID
		return nil
	})
}

// Retain accepted ancestry with the journal, so main can flush children after a
// router restart without depending on the live activity collector.
func (s *journalStore) bindIdentity(ctx context.Context, store *mekugiReplayStore, workspace, thread, parent, author string, valid bool) error {
	release, err := s.lockDelivery(ctx, store, workspace)
	if err != nil {
		return err
	}
	defer release()
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("journal identity requires initialization")
		}
		if j.IdentityKnown && j.Parent == parent && (j.IdentityConflicted || valid && j.Author == author) {
			return errJournalUnchanged
		}
		if !valid || j.IdentityKnown && (j.Parent != parent || j.Author != author) {
			j.IdentityConflicted = true
		}
		j.IdentityKnown = true
		j.Parent = parent
		return nil
	})
}

func (s *journalStore) forkSpawnBaseline(ctx context.Context, store *mekugiReplayStore, workspace, thread string, initialize bool, callIDs map[string]bool) (map[string]bool, error) {
	var baseline map[string]bool
	err := s.transaction(ctx, store.scoped(ctx), workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("spawn baseline requires journal initialization")
		}
		if !j.SpawnBaselineKnown && initialize {
			j.SpawnBaselineKnown = true
			j.SpawnBaseline = maps.Clone(callIDs)
			baseline = maps.Clone(j.SpawnBaseline)
			return nil
		}
		baseline = maps.Clone(j.SpawnBaseline)
		return errJournalUnchanged
	})
	return baseline, err
}

func (s *journalStore) bindSpawnRoles(ctx context.Context, store *mekugiReplayStore, workspace, thread string, roles map[string]journalSpawnRole) error {
	if len(roles) == 0 {
		return nil
	}
	return s.transaction(ctx, store.scoped(ctx), workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("spawn roles require journal initialization")
		}
		if j.SpawnRoles == nil {
			j.SpawnRoles = make(map[string]journalSpawnRole)
		}
		changed := false
		for author, evidence := range roles {
			current, found := j.SpawnRoles[author]
			if found && (current.Conflicted || !evidence.Conflicted && current.Role == evidence.Role) {
				continue
			}
			if evidence.Conflicted || found && current.Role != evidence.Role {
				evidence = journalSpawnRole{Conflicted: true}
			}
			j.SpawnRoles[author] = evidence
			changed = true
		}
		if !changed {
			return errJournalUnchanged
		}
		return nil
	})
}

// Called under the journal mutex and replay lock. Delivery and list authorization
// share the same workspace-scoped durable identity records.
func (s *journalStore) workspaceJournals(store *mekugiReplayStore, workspace string) (map[string]threadJournal, map[string]error, error) {
	recordErrors := make(map[string]error)
	journals := make(map[string]threadJournal)
	if store == nil {
		for _, journal := range s.memory {
			if journal.Workspace == workspace {
				journals[journal.Thread] = journal.clone()
			}
		}
	} else {
		entries, err := os.ReadDir(store.directory)
		if err != nil {
			return nil, nil, err
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "journal-") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			journal, exists, err := readJournalRecord(filepath.Join(store.directory, entry.Name()))
			if exists && journal.Workspace == workspace {
				journals[journal.Thread] = journal
				recordErrors[journal.Thread] = err
			}
			// Unidentifiable records cannot prove ancestry and must not
			// block an unrelated workspace or tree.
		}
	}
	return journals, recordErrors, nil
}

// Called under the delivery lease, journal mutex, and replay lock. Only complete,
// unambiguous parent chains in this workspace can publish live updates to Main.
func (s *journalStore) descendants(store *mekugiReplayStore, workspace, root string) ([]threadJournal, error) {
	journals, recordErrors, err := s.workspaceJournals(store, workspace)
	if err != nil {
		return nil, err
	}
	var result []threadJournal
	for thread, journal := range journals {
		if thread == root {
			continue
		}
		var chainError error
		for range len(journals) {
			node, ok := journals[thread]
			if !ok || !node.IdentityKnown || node.IdentityConflicted {
				break
			}
			chainError = errors.Join(chainError, recordErrors[thread])
			if thread == root {
				if node.Parent == "" && chainError != nil {
					return nil, chainError
				}
				if node.Parent == "" {
					parent := journals[journal.Parent]
					if evidence, ok := parent.SpawnRoles[journal.Author]; ok && !evidence.Conflicted {
						journal.SpawnRole = evidence.Role
					}
					result = append(result, journal)
				}
				break
			}
			thread = node.Parent
		}
	}
	slices.SortFunc(result, func(a, b threadJournal) int {
		if order := strings.Compare(a.Author, b.Author); order != 0 {
			return order
		}
		return strings.Compare(a.Thread, b.Thread)
	})
	return result, nil
}

// Pin a legacy answer source without changing the model-authored receipt digest.
func bindJournalAnswers(mutations []journalMutation, question string) []journalMutation {
	bound := slices.Clone(mutations)
	for index := range bound {
		bound[index].inferredQuestion = question
	}
	return bound
}

func decodeJournalMutations(raw []byte) ([]journalMutation, error) {
	var mutations []journalMutation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&mutations); err != nil {
		// The decoder names the offending field, such as an unknown "path".
		return nil, fmt.Errorf("journal must be an array of at most 512 mutations: %w", err)
	}
	if mutations == nil || len(mutations) > maxJournalItems {
		return nil, errors.New("journal must be an array of at most 512 mutations")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid journal payload")
	}
	return mutations, nil
}

func (s *journalStore) apply(ctx context.Context, store *mekugiReplayStore, workspace, thread, receiptID string, mutations []journalMutation) ([]string, error) {
	release, err := s.lockDelivery(ctx, store, workspace)
	if err != nil {
		return nil, err
	}
	defer release()
	encoded, err := marshalProtocolJSON(mutations)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	var ids []string
	var counters *capturer.JournalMetrics
	err = s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return fmt.Errorf("journal state is missing for thread %q in workspace %q; initialization did not complete or session data was cleaned up; retry the request to initialize it", thread, workspace)
		}
		counters = j.Counters.Clone()
		if receipt, ok := j.Receipts[receiptID]; receiptID != "" && ok {
			if receipt.Digest != digest {
				return errors.New("journal call changed its mutations")
			}
			ids = slices.Clone(receipt.IDs)
			return nil
		}
		j.ensureTree()
		treeMutated := false
		before := slices.Clone(j.Items)
		for position, mutation := range mutations {
			if mutation.Op == "plan" || mutation.Op == "set" || mutation.Op == "log" || mutation.Op == "remove" || mutation.Op == "add" && mutation.Title != nil {
				paths, err := j.applyTree(mutation)
				if err != nil && len(mutations) > 1 {
					// A batch rolls back atomically; name the operation to correct.
					err = fmt.Errorf("operation %d (%s): %w", position+1, mutation.Op, err)
				}
				if err != nil {
					return err
				}
				ids = append(ids, paths...)
				treeMutated = true
				continue
			}
			if strings.HasPrefix(mutation.ID, "/") {
				return errors.New("legacy journal IDs resolve only retained aliases; use p with set or remove")
			}
			if mutation.P != "" || mutation.Under != "" || mutation.Kind != "" || mutation.Body != nil || mutation.State != nil ||
				mutation.Reason != nil || mutation.Before != "" || mutation.Reset != "" || mutation.Tasks != nil || mutation.Agent != "" {
				return errors.New("tree fields require plan, set, log, remove, or add with title; text-only add is the retained milestone form")
			}
			question := ""
			index := slices.IndexFunc(j.Items, func(item journalItem) bool { return item.ID == mutation.ID })
			if mutation.Op == "edit" && index >= 0 {
				question = j.Items[index].Question
			}
			if mutation.Answer != nil {
				question = ""
				if *mutation.Answer {
					question = mutation.inferredQuestion
					if !utf8.ValidString(question) {
						return errors.New("journal question must be UTF-8")
					}
				}
			}
			switch mutation.Op {
			case "add", "edit":
				if mutation.Text == nil || strings.TrimSpace(*mutation.Text) == "" || !utf8.ValidString(*mutation.Text) ||
					len(*mutation.Text)+len(question) > maxJournalItemBytes {
					return errors.New("journal text must be nonblank UTF-8; text and question together must be at most 16 KiB")
				}
				if mutation.Op == "add" && (mutation.ID != "" || len(j.Items) >= maxJournalItems) {
					return fmt.Errorf("%w: journal add requires no ID and at most %d items; delete obsolete items before adding more", errJournalItemLimit, maxJournalItems)
				}
				if mutation.Op == "edit" && index < 0 {
					return errors.New("journal item not found")
				}
			case "delete":
				if index < 0 {
					return errors.New("journal item not found")
				}
				if mutation.Text != nil || mutation.Answer != nil {
					return errors.New("journal delete does not accept text or answer")
				}
			default:
				return errors.New("journal op must be add, edit, or delete")
			}
			if j.Sequence == ^uint64(0) || mutation.Op == "add" && j.NextID == ^uint64(0) {
				return errors.New("journal sequence exhausted")
			}
			j.Sequence++
			var removed journalItem
			if mutation.Op == "delete" {
				removed = j.Items[index]
			}

			switch mutation.Op {
			case "add":
				j.NextID++
				item := journalItem{TerminalOnly: mutation.Answer != nil && *mutation.Answer, ID: shortHandle(j.NextID - 1), Text: *mutation.Text, Question: question, Author: j.Author, Created: j.Sequence, Updated: j.Sequence, ReportNow: mutation.ReportNow}
				j.Items = append(j.Items, item)
				ids = append(ids, item.ID)
			case "edit":
				if mutation.Answer != nil {
					j.Items[index].TerminalOnly = *mutation.Answer
				}
				j.Items[index].Question = question
				j.Items[index].Text = *mutation.Text
				j.Items[index].Updated = j.Sequence
				j.Items[index].ReportNow = mutation.ReportNow
				j.Items[index].Reported = false
				j.Items[index].Flushed = false
				ids = append(ids, mutation.ID)
			case "delete":
				if s.nativeSink(workspace, thread) != nil || mutation.ReportNow && j.Items[index].EverReported {
					if len(j.Retractions) >= maxJournalItems {
						return fmt.Errorf("journal retraction queue limit is %d; deliver pending journal updates before requesting more visible deletions", maxJournalItems)
					}
					j.Retractions = append(j.Retractions, journalRetraction{ID: mutation.ID, Sequence: j.Sequence})
				}
				j.Items = slices.Delete(j.Items, index, index+1)
				ids = append(ids, mutation.ID)
			}
			j.ensureTree()
			event := journalEvent{Legacy: true, Seq: j.Sequence, At: time.Now().UTC().Format(time.RFC3339Nano), Author: j.Author, Op: mutation.Op}
			if mutation.Op == "delete" {
				event.Op, event.Path, event.Fields = "remove", removed.Path, removed.node()
				event.RootRetraction = mutation.ReportNow && removed.RootEverReported
			} else {
				current := slices.IndexFunc(j.Items, func(item journalItem) bool { return item.ID == ids[len(ids)-1] })
				node := &j.Items[current]
				setLegacyJournalContent(node)
				node.UpdatedAt = event.At
				if mutation.Op == "add" {
					node.CreatedAt = event.At
				}
				event.Path, event.Fields = node.Path, node.node()
				if event.Op == "edit" {
					event.Op = "set"
				}
			}
			if err := j.appendEvent(event); err != nil {
				return err
			}
		}
		j.ensureTree()
		if treeMutated {
			if err := j.validateTree(); err != nil {
				return err
			}
			if err := s.validateMountedCompletion(store, *j, before); err != nil {
				return err
			}
		}
		if receiptID != "" {
			j.Receipts[receiptID] = journalReceipt{Digest: digest, IDs: slices.Clone(ids)}
		}
		for _, mutation := range mutations {
			if mutation.Answer == nil || !*mutation.Answer {
				j.countJournalOperation(mutation.Op)
			}
		}
		counters = j.Counters.Clone()
		return nil
	})
	if err == nil {
		capturer.ObserveJournal(ctx, counters)
	}
	return ids, err
}

// listAgent resolves authorization and reads content in one locked snapshot.
// Names alone never establish ancestry, even when separate roots share /root.
func (s *journalStore) listAgent(ctx context.Context, store *mekugiReplayStore, workspace, caller, agent string) ([]journalItem, error) {
	release, err := s.lockState(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var items []journalItem
	read := func() error {
		journals, recordErrors, err := s.workspaceJournals(store, workspace)
		if err != nil {
			return err
		}
		target, err := journalReadTarget(journals, recordErrors, caller, agent)
		if err != nil {
			return err
		}
		items = slices.Clone(journals[target].Items)
		return nil
	}
	if store != nil {
		err = store.locked(ctx, read)
	} else {
		err = read()
	}
	return items, err
}

func (s *journalStore) list(ctx context.Context, store *mekugiReplayStore, workspace, thread string) ([]journalItem, error) {
	release, err := s.lockState(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var items []journalItem
	read := func() error {
		j, exists := s.memory[journalKey(workspace, thread)]
		if store != nil {
			var err error
			j, exists, err = readThreadJournal(store, workspace, thread)
			if err != nil {
				return err
			}
		}
		if !exists {
			return fmt.Errorf("journal state is missing for thread %q in workspace %q; initialization did not complete or session data was cleaned up; retry the request to initialize it", thread, workspace)
		}
		items = slices.Clone(j.Items)
		return nil
	}
	if store != nil {
		err := store.locked(ctx, read)
		return items, err
	}
	err = read()
	return items, err
}

func (s *journalStore) treeAuthored(ctx context.Context, store *mekugiReplayStore, workspace, thread string) (bool, error) {
	release, err := s.lockState(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if store == nil {
		return s.memory[journalKey(workspace, thread)].TreeAuthored, nil
	}
	var tree bool
	err = store.locked(ctx, func() error {
		j, _, err := readThreadJournal(store, workspace, thread)
		tree = j.TreeAuthored
		return err
	})
	return tree, err
}

// Delivery acknowledgements refer to the exact revision rendered. An edit
// arriving while a message is being written must remain eligible for delivery.
func (s *journalStore) acknowledge(ctx context.Context, store *mekugiReplayStore, workspace, thread string, revisions map[string]uint64, terminal bool) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return fmt.Errorf("journal state is missing for thread %q in workspace %q; initialization did not complete or session data was cleaned up; retry the request to initialize it", thread, workspace)
		}
		for index := range j.Items {
			if revision, ok := revisions[j.Items[index].ID]; ok {
				j.Items[index].EverReported = true
				j.acknowledgeLegacyPath(j.Items[index].Path, revision, terminal)
				if revision == j.Items[index].Updated {
					j.Items[index].Reported = true
					if terminal {
						j.Items[index].Flushed = true
					}
				}
			}
		}
		j.advanceLegacyCursors()
		j.Retractions = slices.DeleteFunc(j.Retractions, func(retraction journalRetraction) bool {
			if revisions[retraction.ID] != retraction.Sequence {
				return false
			}
			for _, event := range j.Events {
				if event.Seq == retraction.Sequence {
					j.acknowledgeLegacyPath(event.Path, event.Seq, terminal)
				}
			}
			return true
		})
		j.advanceLegacyCursors()
		return nil
	})
}

// Completion delivery acknowledges its prepared window, never a later snapshot.
func (s *journalStore) acknowledgeResult(ctx context.Context, store *mekugiReplayStore, workspace, thread string, sequence, changes, count uint64) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("journal result state is missing")
		}
		j.ResultSeq = max(j.ResultSeq, sequence)
		j.ResultChangeSeq = max(j.ResultChangeSeq, changes)
		j.ResultCount = max(j.ResultCount, count)
		return nil
	})
}

func (s *journalStore) acknowledgeTree(ctx context.Context, store *mekugiReplayStore, workspace, thread string, sequence uint64, terminal bool) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("journal cursor state is missing")
		}
		j.LiveSeq = max(j.LiveSeq, sequence)
		if terminal {
			j.FlushSeq = max(j.FlushSeq, sequence)
		}
		for i := range j.Items {
			if j.Items[i].Updated <= sequence {
				j.Items[i].Reported, j.Items[i].EverReported = true, true
				if terminal {
					j.Items[i].Flushed = true
				}
			}
		}
		j.advanceLegacyCursors()
		j.Retractions = slices.DeleteFunc(j.Retractions, func(retraction journalRetraction) bool { return retraction.Sequence <= sequence })
		return nil
	})
}
