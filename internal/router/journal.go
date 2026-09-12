package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
)

const (
	maxJournalThreads   = 256
	maxJournalItems     = 256
	maxJournalItemBytes = 16 << 10
	// Terminal delivery is independent of the live progress budget. The extra
	// space covers indentation of every content line, Q&A labels, item IDs,
	// and the bounded canonical agent name.
	maxJournalFlushBytes = maxJournalItems*(3*maxJournalItemBytes+128) + maxJournalItemBytes
	maxJournalReceipts   = 16384
)

var errJournalThreadCapacity = errors.New("journal thread capacity reached")

type journalMutation struct {
	Op               string  `json:"op"`
	ID               string  `json:"id,omitempty"`
	Text             *string `json:"text,omitempty"`
	Answer           *bool   `json:"answer,omitempty"`
	inferredQuestion string
	ReportNow        bool `json:"report_now,omitzero"`
}

type journalItem struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Question  string `json:"question,omitempty"`
	Author    string `json:"author"`
	Created   uint64 `json:"created"`
	Updated   uint64 `json:"updated"`
	ReportNow bool   `json:"report_now"`
	Reported  bool   `json:"reported"`
	Flushed   bool   `json:"flushed"`
	// A later silent edit does not erase the fact that the user saw this ID.
	EverReported bool `json:"ever_reported,omitzero"`
}

type journalReceipt struct {
	Digest string   `json:"digest"`
	IDs    []string `json:"ids"`
}

type journalRetraction struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
}

type threadJournal struct {
	Parent             string                    `json:"parent,omitempty"`
	IdentityKnown      bool                      `json:"identity_known,omitzero"`
	IdentityConflicted bool                      `json:"identity_conflicted,omitzero"`
	Version            int                       `json:"version"`
	Workspace          string                    `json:"workspace"`
	Thread             string                    `json:"thread"`
	Author             string                    `json:"author"`
	Sequence           uint64                    `json:"sequence"`
	NextID             uint64                    `json:"next_id"`
	Items              []journalItem             `json:"items"`
	Retractions        []journalRetraction       `json:"retractions,omitempty"`
	Receipts           map[string]journalReceipt `json:"receipts"`
}

func (j threadJournal) clone() threadJournal {
	j.Retractions = slices.Clone(j.Retractions)
	j.Items = slices.Clone(j.Items)
	j.Receipts = maps.Clone(j.Receipts)
	return j
}

// journalStore owns mutable journal state, separately from immutable tool replay.
// The replay store's filesystem lock serializes journal transactions across router
// processes too; journal files never count against or evict executable history.
type journalStore struct {
	deliveryGate chan struct{}
	stateGate    chan struct{}
	memory       map[string]threadJournal
}

func newJournalStore() *journalStore {
	return &journalStore{memory: make(map[string]threadJournal), deliveryGate: make(chan struct{}, 1), stateGate: make(chan struct{}, 1)}
}

// Keep the complete state transaction serialized without trapping canceled callers
// behind another request's replay-lock wait.
func (s *journalStore) lockState(ctx context.Context) (func(), error) {
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
// notice and lose the required retraction. Waiting remains request-cancellable.
func (s *journalStore) lockDelivery(ctx context.Context, store *mekugiReplayStore) (func(), error) {
	select {
	case s.deliveryGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-s.deliveryGate }
	if store == nil {
		return release, nil
	}
	// Keep a separate lock from replay transactions: delivery must be able to
	// retain provenance and acknowledge revisions while excluding mutations
	// from other router processes.
	path := filepath.Join(store.directory, "journal-delivery.lock")
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
		journal.Version != 1 || journal.Thread == "" || filepath.Base(path) != journalFilename(journal.Workspace, journal.Thread) {
		return threadJournal{}, false, errors.New("corrupt journal record")
	}
	if len(journal.Items) > maxJournalItems || journal.Receipts == nil || len(journal.Receipts) > maxJournalReceipts {
		// A fully decoded, filename-verified identity can establish whose
		// content failed validation, without trusting partially decoded JSON.
		return journal, true, errors.New("corrupt journal contents")
	}
	return journal, true, nil
}

func writeThreadJournal(store *mekugiReplayStore, journal threadJournal) error {
	data, err := marshalProtocolJSON(journal)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return errors.New("journal record capacity reached")
	}
	name := journalFilename(journal.Workspace, journal.Thread)
	file, err := os.CreateTemp(store.directory, "journal-pending-")
	if err != nil {
		return err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), filepath.Join(store.directory, name)); err != nil {
		return err
	}
	return syncReplayDirectory(store.directory)
}

func (s *journalStore) transaction(ctx context.Context, store *mekugiReplayStore, workspace, thread string, mutate func(*threadJournal, bool) error) error {
	if strings.TrimSpace(thread) == "" {
		return errors.New("journal requires a stable thread ID")
	}
	release, err := s.lockState(ctx)
	if err != nil {
		return err
	}
	defer release()
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
		next := current.clone()
		if err := mutate(&next, exists); err != nil {
			return err
		}
		if !exists && len(s.memory) >= maxJournalThreads {
			return errJournalThreadCapacity
		}
		if store != nil {
			if !exists {
				entries, err := os.ReadDir(store.directory)
				if err != nil {
					return err
				}
				count := 0
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "journal-") && strings.HasSuffix(entry.Name(), ".json") {
						count++
					}
				}
				if count >= maxJournalThreads {
					return errJournalThreadCapacity
				}
			}
			if err := writeThreadJournal(store, next); err != nil {
				return err
			}
		}
		s.memory[key] = next
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
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if exists {
			return nil
		}
		*j = threadJournal{Version: 1, Workspace: workspace, Thread: thread, Author: author, Items: []journalItem{}, Receipts: make(map[string]journalReceipt)}
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
		j.Items = slices.Clone(source.Items)
		j.Sequence, j.NextID = source.Sequence, source.NextID
		return nil
	})
}

// Retain accepted ancestry with the journal, so main can flush children after a
// router restart without depending on the live activity collector.
func (s *journalStore) bindIdentity(ctx context.Context, store *mekugiReplayStore, workspace, thread, parent, author string, valid bool) error {
	release, err := s.lockDelivery(ctx, store)
	if err != nil {
		return err
	}
	defer release()
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("journal identity requires initialization")
		}
		if !valid || j.IdentityKnown && (j.Parent != parent || j.Author != author) {
			j.IdentityConflicted = true
		}
		j.IdentityKnown = true
		j.Parent = parent
		return nil
	})
}

// Called under the delivery lease, journal mutex, and replay lock. Only complete,
// unambiguous parent chains in this workspace can join a main terminal flush.
func (s *journalStore) descendants(store *mekugiReplayStore, workspace, root string) ([]threadJournal, error) {
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
			return nil, err
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

// Pin inferred source without changing the model-authored receipt digest.
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
	if err := decoder.Decode(&mutations); err != nil || mutations == nil || len(mutations) > maxJournalItems {
		return nil, errors.New("journal must be an array of at most 256 mutations")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid journal payload")
	}
	return mutations, nil
}

func (s *journalStore) apply(ctx context.Context, store *mekugiReplayStore, workspace, thread, receiptID string, mutations []journalMutation) ([]string, error) {
	release, err := s.lockDelivery(ctx, store)
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
	err = s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("journal is not initialized")
		}
		if receipt, ok := j.Receipts[receiptID]; receiptID != "" && ok {
			if receipt.Digest != digest {
				return errors.New("journal call changed its mutations")
			}
			ids = slices.Clone(receipt.IDs)
			return nil
		}
		if receiptID != "" && len(j.Receipts) >= maxJournalReceipts {
			return errors.New("journal receipt capacity reached")
		}
		for _, mutation := range mutations {
			question := ""
			index := slices.IndexFunc(j.Items, func(item journalItem) bool { return item.ID == mutation.ID })
			if mutation.Op == "edit" && index >= 0 {
				question = j.Items[index].Question
			}
			if mutation.Answer != nil {
				question = ""
				if *mutation.Answer {
					question = mutation.inferredQuestion
					if strings.TrimSpace(question) == "" || !utf8.ValidString(question) {
						return errors.New("journal answer requires a nonblank UTF-8 user question")
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
					return errors.New("journal add requires no ID and available item capacity")
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
			switch mutation.Op {
			case "add":
				j.NextID++
				item := journalItem{ID: fmt.Sprintf("j%d", j.NextID), Text: *mutation.Text, Question: question, Author: j.Author, Created: j.Sequence, Updated: j.Sequence, ReportNow: mutation.ReportNow}
				j.Items = append(j.Items, item)
				ids = append(ids, item.ID)
			case "edit":
				j.Items[index].Question = question
				j.Items[index].Text = *mutation.Text
				j.Items[index].Updated = j.Sequence
				j.Items[index].ReportNow = mutation.ReportNow
				j.Items[index].Reported = false
				j.Items[index].Flushed = false
				ids = append(ids, mutation.ID)
			case "delete":
				if mutation.ReportNow && j.Items[index].EverReported {
					if len(j.Retractions) >= maxJournalItems {
						return errors.New("journal retraction capacity reached")
					}
					j.Retractions = append(j.Retractions, journalRetraction{ID: mutation.ID, Sequence: j.Sequence})
				}
				j.Items = slices.Delete(j.Items, index, index+1)
				ids = append(ids, mutation.ID)
			}
		}
		if receiptID != "" {
			j.Receipts[receiptID] = journalReceipt{Digest: digest, IDs: slices.Clone(ids)}
		}
		return nil
	})
	return ids, err
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
			return errors.New("journal is not initialized")
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

// Delivery acknowledgements refer to the exact revision rendered. An edit
// arriving while a message is being written must remain eligible for delivery.
func (s *journalStore) acknowledge(ctx context.Context, store *mekugiReplayStore, workspace, thread string, revisions map[string]uint64, terminal bool) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("journal is not initialized")
		}
		for index := range j.Items {
			if revision, ok := revisions[j.Items[index].ID]; ok {
				j.Items[index].EverReported = true
				if revision == j.Items[index].Updated {
					j.Items[index].Reported = true
					if terminal {
						j.Items[index].Flushed = true
					}
				}
			}
		}
		j.Retractions = slices.DeleteFunc(j.Retractions, func(retraction journalRetraction) bool {
			return revisions[retraction.ID] == retraction.Sequence
		})
		return nil
	})
}
