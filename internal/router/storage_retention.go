package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const sessionRetention = 14 * 24 * time.Hour
const defaultReplayStorageBytes int64 = 4 << 30

type storageTurnKey struct{}

type storageSessionKey struct{}

type storageSessionIdentity struct {
	Thread     string
	Routing    string
	Namespace  string
	Parent     string
	Fork       string
	Initialize bool
}

type retainedSession struct {
	Version  int             `json:"version"`
	Thread   string          `json:"thread"`
	LastUsed time.Time       `json:"last_used"`
	Files    map[string]bool `json:"files"`
}

func storageSessionName(thread string) string {
	return fmt.Sprintf("session-%x.json", sha256.Sum256([]byte(thread)))
}

func (s *mekugiReplayStore) scoped(ctx context.Context) *mekugiReplayStore {
	if s == nil {
		return nil
	}
	copy := *s
	if identity, ok := ctx.Value(storageSessionKey{}).(storageSessionIdentity); ok {
		copy.session = identity
	}
	return &copy
}

// A shared process lease protects running requests and workers across routers.
// Lock files have stable inodes and are never unlinked while another process
// could be acquiring them. Their empty contents are not retained chat data.
func (s *mekugiReplayStore) beginSession(ctx context.Context, thread, routing string) (context.Context, func(), error) {
	if s == nil || thread == "" {
		return ctx, func() {}, nil
	}
	identity := storageSessionIdentity{Thread: thread, Routing: routing, Namespace: thread}
	if err := s.locked(ctx, func() error {
		var err error
		identity.Namespace, err = s.namespaceForThread(thread)
		return err
	}); err != nil {
		return ctx, nil, err
	}
	ctx = context.WithValue(ctx, storageSessionKey{}, identity)
	s = s.scoped(ctx)
	path := filepath.Join(s.directory, strings.TrimSuffix(storageSessionName(thread), ".json")+".lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return ctx, nil, errors.New("session storage lease is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ctx, nil, err
	}
	lease := flock.New(path, flock.SetPermissions(0600))
	ok, err := lease.TryRLockContext(ctx, 25*time.Millisecond)
	if err != nil || !ok {
		return ctx, nil, fmt.Errorf("acquire session storage lease: %w", errors.Join(err, ctx.Err()))
	}
	release := func() { _ = lease.Unlock() }
	err = s.locked(ctx, func() error { return s.retainFiles() })
	if err != nil {
		release()
		return ctx, nil, err
	}
	return ctx, release, nil
}

// A turn lease covers host handoff gaps and yielded tools. Only a delivered
// terminal with no pending tools releases it; shutdown also releases all leases.
func (p *mekugiProxy) beginStorageSession(ctx context.Context, thread, routing string) (context.Context, error) {
	ctx, release, err := p.replayStore.beginSession(ctx, thread, routing)
	if err != nil {
		return ctx, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		release()
		return ctx, errors.New("router closed before session storage could be leased")
	}
	if p.storageLeases == nil {
		p.storageLeases = make(map[string]func())
	}
	if p.storageLeases[thread] != nil {
		release()
	} else {
		p.storageLeases[thread] = release
	}
	p.storageSequence++
	if p.storageTurns == nil {
		p.storageTurns = make(map[string]uint64)
	}
	p.storageTurns[thread] = p.storageSequence
	return context.WithValue(ctx, storageTurnKey{}, p.storageSequence), nil
}

func (p *mekugiProxy) releaseIdleStorageSession(ctx context.Context, thread string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// An older delivered terminal must not release a newer host handoff.
	generation, ok := ctx.Value(storageTurnKey{}).(uint64)
	if !ok || generation != p.storageTurns[thread] {
		return
	}
	for session, active := range p.activeSessions {
		if active != 0 && strings.HasSuffix(session, "\x00"+thread) {
			return
		}
	}
	if release := p.storageLeases[thread]; release != nil {
		release()
		delete(p.storageLeases, thread)
		delete(p.storageTurns, thread)
	}
	if p.commentary != nil {
		p.commentary.retireThread(thread)
	}
}

func (s *mekugiReplayStore) readRetainedSession(name string) (retainedSession, error) {
	var session retainedSession
	data, err := readManagedOutputFile(filepath.Join(s.directory, name))
	if err != nil {
		return session, err
	}
	if json.Unmarshal(data, &session) != nil || session.Version != 1 || session.Thread == "" ||
		session.LastUsed.IsZero() || session.Files == nil || storageSessionName(session.Thread) != name {
		return session, errors.New("invalid session retention record")
	}
	for file := range session.Files {
		if !retainedDataName(file) {
			return session, errors.New("invalid session retention file identity")
		}
	}
	return session, nil
}

func retainedDataName(name string) bool {
	if validSnapshotName(name) {
		return true
	}
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return false
	}
	if id, ok := strings.CutPrefix(strings.TrimSuffix(name, ".json"), "output-"); ok {
		if validShellOutputID(id) {
			return true
		}
	}
	for _, prefix := range []string{"call-", "commentary-", "journal-", "compaction-", "failure-", "usage-", changeIndexPrefix, "cursor-", "output-", "wait-", "hooks-"} {
		if hash, ok := strings.CutPrefix(strings.TrimSuffix(name, ".json"), prefix); ok {
			if len(hash) != 64 {
				return false
			}
			for _, char := range hash {
				if !strings.ContainsRune("0123456789abcdef", char) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func storageCatalogName(name string) bool {
	tail, ok := strings.CutPrefix(name, "session-")
	return ok && retainedDataName("journal-"+tail)
}

// Called under store.lock. Ownership is recorded before publishing a capability.
// Reading inherited history adds another owner instead of transferring ownership.
func (s *mekugiReplayStore) retainFiles(names ...string) error {
	if s.session.Thread == "" {
		return nil
	}
	// A fork, retained change selection or recovered output adopts the call's
	// shared snapshots together with its envelope, under the same store lock.
	names = slices.Clone(names)
	for _, name := range slices.Clone(names) {
		dependencies, err := s.snapshotDependencies(name)
		if err != nil {
			return err
		}
		names = append(names, dependencies...)
	}
	name := storageSessionName(s.session.Thread)
	session, err := s.readRetainedSession(name)
	if errors.Is(err, os.ErrNotExist) {
		session = retainedSession{Version: 1, Thread: s.session.Thread, Files: make(map[string]bool)}
	} else if err != nil {
		return err
	}
	changed := session.LastUsed.IsZero()
	for _, file := range names {
		if !retainedDataName(file) {
			return errors.New("cannot retain an invalid storage file identity")
		}
		changed = changed || !session.Files[file]
		session.Files[file] = true
	}
	if !changed && time.Since(session.LastUsed) < time.Minute {
		return nil
	}
	session.LastUsed = time.Now().UTC()
	data, err := marshalProtocolJSON(session)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return storageCapacityError("session ownership catalog", int64(len(data)), maxReplayRecordBytes, "Continue in a new chat; the current session has too many retained references to fit in one catalog record.")
	}
	if len(names) > 0 {
		// Admission cannot prune. The store lock bridges validation to this
		// publication; failed catalog growth therefore needs no rollback.
		if err := s.maintainStorage(name, int64(len(data))); err != nil {
			return err
		}
	}
	return storageIOError(s.writeFile(name, "session-pending-", data))
}

var retainedReadReference = regexp.MustCompile(`(?:\b|\\[nr])mread(?:[ \t]|\\t)+([a-z]+[0-9]*)\b`)

// Retain the complete visible history in one locked catalog update. A fork
// shares immutable records with its source; removing either chat keeps facts
// referenced by the other. Invalid input never establishes an ownership claim.
func (s *mekugiReplayStore) retainInput(ctx context.Context, workspace string, raw json.RawMessage, histories map[string]mekugiHistory, releaseSnapshot func()) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	if s.session.Thread == "" {
		return nil
	}
	return s.locked(ctx, func() error {
		var names []string
		for call := range histories {
			names = append(names, replayRecordName(workspace, call, false))
		}
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(raw, &items)
		for _, item := range items {
			if jsonString(item, "type") == "message" {
				name := replayRecordName(workspace, jsonString(item, "id"), true)
				if info, err := os.Lstat(filepath.Join(s.directory, name)); err == nil && info.Mode().IsRegular() {
					names = append(names, name)
				}
			}
		}
		if err := s.initializeHandleScope(names, releaseSnapshot); err != nil {
			return err
		}
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		for _, history := range histories {
			if change, exists := index.Changes[history.ChangeID]; exists {
				names = append(names, changeIndexName(workspace, index.Namespace))
				for _, attempt := range change.Calls {
					names = append(names, replayRecordName(workspace, attempt.ID, false))
				}
			}
		}
		for _, match := range retainedReadReference.FindAllStringSubmatch(string(raw), -1) {
			id := match[1]
			if !validShellOutputID(id) {
				continue
			}
			name, err := s.outputName(id)
			if err != nil {
				continue // A textual reference alone cannot import another session.
			}
			data, err := readManagedOutputFile(filepath.Join(s.directory, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			var record shellOutputRecord
			if json.Unmarshal(data, &record) != nil || record.ID != id || validateReadRecord(record) != nil {
				return errors.New("invalid inherited read recovery record")
			}
			dependencies, err := s.readDependencyNames(record)
			if err != nil {
				return err
			}
			names = append(names, dependencies...)
		}
		if releaseSnapshot != nil {
			// store.lock now bridges validation to ownership publication;
			// cleanup inside retainFiles can safely acquire the snapshot lease.
			releaseSnapshot()
		}
		return s.retainFiles(names...)
	})
}

func (s *mekugiReplayStore) changeDependencyNames(workspace string, ids []string) ([]string, error) {
	index, err := s.readChangeIndex(workspace)
	if err != nil {
		return nil, err
	}
	names := []string{changeIndexName(workspace, index.Namespace)}
	for _, id := range ids {
		change, exists := index.Changes[id]
		if !exists {
			return nil, missingChangeError(index, id)
		}
		for _, call := range change.Calls {
			names = append(names, replayRecordName(workspace, call.ID, false))
		}
	}
	return names, nil
}

func (s *mekugiReplayStore) readDependencyNames(record shellOutputRecord) ([]string, error) {
	var names []string
	seen := make(map[string]bool)
	for {
		if seen[record.ID] {
			return nil, errors.New("cyclic read recovery reference")
		}
		seen[record.ID] = true
		owner, err := s.handleOwner(record.ID)
		if err != nil {
			return nil, err
		}
		if record.CursorDigest != "" {
			names = append(names, scopedCursorName(owner, record.CursorDigest))
		}
		names = append(names, scopedOutputName(owner, record.ID))
		if selection := record.Changes; selection != nil {
			if selection.Frozen {
				for _, id := range selection.IDs {
					for _, call := range selection.Selected[id].Calls {
						names = append(names, replayRecordName(selection.Workspace, call.ID, false))
					}
				}
			} else {
				dependencies, err := s.changeDependencyNames(selection.Workspace, selection.IDs)
				if err != nil {
					return nil, err
				}
				names = append(names, dependencies...)
			}
		}

		if record.Source == "" {
			return names, nil
		}
		id := record.Source
		name, err := s.outputName(id)
		if err != nil {
			return nil, err
		}
		data, err := readManagedOutputFile(filepath.Join(s.directory, name))
		if err != nil {
			return nil, fmt.Errorf("read recovery source is unavailable: %w", err)
		}
		record = shellOutputRecord{}
		if json.Unmarshal(data, &record) != nil || record.ID != id || validateReadRecord(record) != nil {
			return nil, errors.New("invalid read recovery source")
		}
	}
}

func (s *mekugiReplayStore) retainJournalDependencies(journal threadJournal) error {
	if s == nil || s.session.Thread == "" {
		return nil
	}
	names := []string{journalFilename(journal.Workspace, journal.Thread)}
	for receipt := range journal.Receipts {
		file := replayRecordName(journal.Workspace, strings.TrimSuffix(receipt, ":journal"), false)
		if info, err := os.Lstat(filepath.Join(s.directory, file)); err == nil && info.Mode().IsRegular() {
			names = append(names, file)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	index, err := s.readChangeIndex(journal.Workspace)
	if err != nil {
		return err
	}
	for position, stream := range index.Streams {
		if stream.Thread != journal.Thread {
			continue
		}
		names = append(names, changeIndexName(journal.Workspace, index.Namespace))
		for id, change := range index.Changes {
			owner, _, _ := parseChangeID(id)
			if owner == index.streamName(position) {
				for _, call := range change.Calls {
					names = append(names, replayRecordName(journal.Workspace, call.ID, false))
				}
			}
		}
	}
	return s.retainFiles(names...)
}

func (s *mekugiReplayStore) retainReadRecord(record shellOutputRecord) error {
	names, err := s.readDependencyNames(record)
	if err != nil {
		return err
	}
	return s.retainFiles(names...)
}

func storageCapacityError(resource string, required, limit int64, action string) error {
	message := fmt.Sprintf("Mekugi %s needs %d bytes; limit is %d bytes. %s", resource, required, limit, action)
	return criticalDiagnostic(errors.New(message), "storage_capacity", message, false)
}

// Only pressure queued for background maintenance is eligible for a persistence
// retry. Record-size limits and storage I/O errors cannot be cured by waiting.
type storagePressureError struct {
	error
	name string
	used int64
}

func (e *storagePressureError) Unwrap() error { return e.error }

// Keep completed response facts in memory while the existing maintenance worker
// frees space. Every attempt revalidates and publishes under store.lock; neither
// that lock nor a host operation is held or repeated while waiting.
func (s *mekugiReplayStore) putAfterMaintenance(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	err := s.put(ctx, workspace, histories)
	pressure, ok := errors.AsType[*storagePressureError](err)
	if !ok {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if waitErr := s.waitForStorageMaintenance(ctx); waitErr != nil {
			return errors.Join(err, waitErr)
		}
		var used int64
		if checkErr := s.locked(ctx, func() error {
			files, err := s.storageFileSizes()
			if err == nil {
				used, _ = s.storageNeeds(files, pressure.name, files[pressure.name])
			}
			return err
		}); checkErr != nil {
			return errors.Join(err, checkErr)
		}
		// Measure reclamation before retrying: a batch may publish earlier
		// histories before reaching pressure again. Those successful writes
		// must not be mistaken for maintenance making no progress.
		if used >= pressure.used {
			return err
		}
		err = s.put(ctx, workspace, histories)
		next, ok := errors.AsType[*storagePressureError](err)
		if !ok {
			return err
		}
		pressure = next
	}
}

func (s *mekugiReplayStore) waitForStorageMaintenance(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pending *storagePressureRequest
		err := s.locked(ctx, func() error {
			var err error
			pending, err = s.readStoragePressure()
			return err
		})
		if err != nil {
			return err
		}
		if pending == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func storageIOError(err error) error {
	if err == nil {
		return nil
	}
	return criticalDiagnostic(err, "storage_io", "Mekugi could not persist session data; check available disk space and storage permissions before retrying", false)
}

type storageCandidate struct {
	name    string
	thread  string
	used    time.Time
	files   map[string]bool
	legacy  bool
	pending []string
}

type storageSnapshot struct {
	sessions []storageCandidate
	files    map[string]int64
	owners   map[string]int
}

func (s *mekugiReplayStore) storageSnapshotContext(ctx context.Context) (storageSnapshot, error) {
	snapshot := storageSnapshot{files: make(map[string]int64), owners: make(map[string]int)}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return snapshot, err
	}
	times := make(map[string]time.Time)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		name := entry.Name()
		if storageCatalogName(name) {
			session, err := s.readRetainedSession(name)
			if err != nil {
				return snapshot, err
			}
			snapshot.sessions = append(snapshot.sessions, storageCandidate{
				name: name, thread: session.Thread, used: session.LastUsed, files: session.Files,
			})
			for file := range session.Files {
				snapshot.owners[file]++
			}
		}
		if retainedDataName(name) || storageCatalogName(name) {
			info, err := entry.Info()
			if err != nil {
				return snapshot, err
			}
			if !info.Mode().IsRegular() {
				return snapshot, errors.New("session storage contains a non-regular managed record; cleanup refused")
			}
			snapshot.files[name] = info.Size()
			times[name] = info.ModTime()
		}
	}
	// Old journals have an exact thread identity even before the ownership
	// catalog existed. Adopt them so a live old chat is never treated as garbage.
	for name := range snapshot.files {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		if snapshot.owners[name] != 0 || !strings.HasPrefix(name, "journal-") {
			continue
		}
		journal, exists, err := readJournalRecord(filepath.Join(s.directory, name))
		if err != nil || !exists {
			return snapshot, errors.Join(err, errors.New("cannot identify legacy journal for retention"))
		}
		files := map[string]bool{name: true}
		for receipt := range journal.Receipts {
			call := strings.TrimSuffix(receipt, ":journal")
			file := replayRecordName(journal.Workspace, call, false)
			if _, exists := snapshot.files[file]; exists && snapshot.owners[file] == 0 {
				files[file] = true
			}
		}
		snapshot.sessions = append(snapshot.sessions, storageCandidate{
			thread: journal.Thread, used: times[name], files: files, legacy: true,
		})
		for file := range files {
			snapshot.owners[file]++
		}
	}
	// Older output and replay records have no trustworthy chat identity.
	// Keep them as legacy records, ordered by last write, never infer an owner
	// from a filename or borrow another workspace's session.
	// Expand dependencies even for older catalogs and uncatalogued calls.
	// Process calls before considering orphan blobs as legacy candidates.
	addDependencies := func(candidate *storageCandidate) error {
		for _, name := range slices.Sorted(maps.Keys(candidate.files)) {
			if err := ctx.Err(); err != nil {
				return err
			}
			dependencies, err := s.snapshotDependencies(name)
			if err != nil {
				return err
			}
			for _, dependency := range dependencies {
				if !candidate.files[dependency] {
					candidate.files[dependency] = true
					snapshot.owners[dependency]++
				}
			}
		}
		return nil
	}
	for i := range snapshot.sessions {
		if err := addDependencies(&snapshot.sessions[i]); err != nil {
			return snapshot, err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(snapshot.files)) {
		if snapshot.owners[name] == 0 && retainedDataName(name) && !strings.HasPrefix(name, changeIndexPrefix) {
			snapshot.sessions = append(snapshot.sessions, storageCandidate{
				used: times[name], files: map[string]bool{name: true}, legacy: true,
			})
			snapshot.owners[name]++
			if err := addDependencies(&snapshot.sessions[len(snapshot.sessions)-1]); err != nil {
				return snapshot, err
			}
		}
	}
	slices.SortFunc(snapshot.sessions, func(a, b storageCandidate) int {
		if order := a.used.Compare(b.used); order != 0 {
			return order
		}
		return strings.Compare(a.name+a.thread+firstStorageFile(a.files), b.name+b.thread+firstStorageFile(b.files))
	})
	return snapshot, nil
}

func firstStorageFile(files map[string]bool) string {
	first := ""
	for file := range files {
		if first == "" || file < first {
			first = file
		}
	}
	return first
}

// Only deletion facts from this locked cleanup may prune an index. An unrelated
// missing replay file remains corruption, not an apparently successful cleanup.
func retireStoredChanges(index *changeIndex, deleted map[string]bool, thread string) bool {
	changed := false
	position := slices.IndexFunc(index.Streams, func(candidate changeStream) bool {
		return candidate.Thread == thread && thread != ""
	})
	for id, change := range index.Changes {
		stream, _, _ := parseChangeID(id)
		pending := len(change.Calls) == 0 && position >= 0 && index.streamName(position) == stream
		if !pending && !slices.ContainsFunc(change.Calls, func(call trackedCall) bool {
			return deleted[replayRecordName(index.Workspace, call.ID, false)]
		}) {
			continue
		}
		if !changed {
			index.Streams = slices.Clone(index.Streams)
			index.Changes = maps.Clone(index.Changes)
			changed = true
		}
		before := len(change.Calls)
		change.Calls = slices.DeleteFunc(slices.Clone(change.Calls), func(call trackedCall) bool {
			return deleted[replayRecordName(index.Workspace, call.ID, false)]
		})
		if len(change.Calls) == 0 {
			delete(index.Changes, id)
			for position := range index.Streams {
				if index.streamName(position) == stream {
					index.Streams[position].Retired++
					break
				}
			}
		} else {
			change.RetiredCalls += before - len(change.Calls)
			index.Changes[id] = change
		}
	}
	return changed
}

func (s *mekugiReplayStore) reconcileRetiredChanges(index *changeIndex) (bool, error) {
	copy := *s
	copy.session.Namespace = index.Namespace
	s = &copy
	current, err := s.readChangeIndex(index.Workspace)
	if err != nil {
		return false, err
	}
	changed := false
	for position, stream := range current.Streams {
		if position >= len(index.Streams) || stream.Retired <= index.Streams[position].Retired {
			continue
		}
		for id := range index.Changes {
			owner, number, _ := parseChangeID(id)
			if owner == index.streamName(position) && number <= stream.Next {
				if _, exists := current.Changes[id]; !exists {
					delete(index.Changes, id)
				}
			}
		}
		index.Streams[position].Retired = stream.Retired
		changed = true
	}
	for id, change := range index.Changes {
		if prior, exists := current.Changes[id]; exists && prior.RetiredCalls > change.RetiredCalls {
			var retained []trackedCall
			for _, call := range change.Calls {
				_, found, err := s.read(index.Workspace, call.ID, false)
				if err != nil {
					return false, err
				}
				if found {
					retained = append(retained, call)
				}
			}
			change.Calls, change.RetiredCalls = retained, prior.RetiredCalls
			index.Changes[id] = change
			changed = true
		}
	}
	return changed, nil
}

func (s *mekugiReplayStore) storageNeeds(files map[string]int64, replacement string, size int64) (int64, int64) {
	limit := s.maxBytes
	if limit <= 0 {
		limit = defaultReplayStorageBytes
	}
	prefix := ""
	if strings.HasPrefix(replacement, "commentary-") {
		prefix, limit = "commentary-", s.maxCommentaryBytes
	}
	total, outputs := size, int64(0)
	for name, bytes := range files {
		if name == replacement {
			continue
		}
		if prefix == "" && !strings.HasPrefix(name, "commentary-") || prefix != "" && strings.HasPrefix(name, prefix) {
			total += bytes
		}
		if strings.HasPrefix(name, "output-") {
			outputs += bytes
		}
	}
	if strings.HasPrefix(replacement, "changes-") && size > maxReplayRecordBytes {
		return size, maxReplayRecordBytes
	}
	if strings.HasPrefix(replacement, "output-") && outputs+size > maxShellOutputStoreBytes {
		return outputs + size, maxShellOutputStoreBytes
	}
	return total, limit
}

func (s *mekugiReplayStore) storageFileSizes() (map[string]int64, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, err
	}
	files := make(map[string]int64)
	for _, entry := range entries {
		if !retainedDataName(entry.Name()) && !storageCatalogName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("session storage contains a non-regular managed record; cleanup refused")
		}
		files[entry.Name()] = info.Size()
	}
	return files, nil
}

// Scoped stores share this snapshot. Access is serialized by store.lock;
// the existing revision invalidates publications and cleanup from other owners.
type storageAdmission struct {
	mu       sync.Mutex
	revision string
	files    map[string]int64
}

func (s *mekugiReplayStore) admissionFileSizes() (map[string]int64, error) {
	revision, err := s.storageRevision()
	if err != nil {
		return nil, err
	}
	if s.admission != nil && s.admission.files != nil && s.admission.revision == revision {
		return s.admission.files, nil
	}
	files, err := s.storageFileSizes()
	if err == nil && s.admission != nil {
		s.admission.revision, s.admission.files = revision, files
	}
	return files, err
}

// Admission runs under store.lock but never prunes. Background maintenance
// owns reclamation; a capacity failure cannot expose unfinished durable evidence.
func (s *mekugiReplayStore) maintainStorage(replacement string, size int64) error {
	files, err := s.admissionFileSizes()
	if err != nil {
		return storageIOError(err)
	}
	required, limit := s.storageNeeds(files, replacement, size)
	if required <= limit {
		return nil
	}
	if size > limit && !strings.HasPrefix(replacement, changeIndexPrefix) {
		return storageCapacityError("session storage write", size, limit, "A single record cannot fit; reduce the retained output or split the operation.")
	}
	request := storagePressureRequest{Name: replacement, Growth: max(0, size-files[replacement])}
	if err := s.requestStoragePrune(request); err != nil {
		return storageIOError(err)
	}
	used, _ := s.storageNeeds(files, replacement, files[replacement])
	return &storagePressureError{
		error: storageCapacityError("retained session storage", required, limit,
			"Background cleanup was requested. Retry retaining this evidence after maintenance frees space; do not rerun the host operation. Running sessions and shared history were preserved."),
		name: replacement, used: used,
	}
}

type managedFile struct {
	name    string
	pattern string
	data    []byte
}

func (s *mekugiReplayStore) writeManagedFile(name, pattern string, data []byte) error {
	return s.writeManagedFiles(managedFile{name: name, pattern: pattern, data: data}, nil, nil)
}

// Called under store.lock. The record and every dependency it references are
// retained in one catalog update and admitted together before any write. The
// missing dependencies are published before the record referencing them.
func (s *mekugiReplayStore) writeManagedFiles(record managedFile, dependencies []string, missing []managedFile) error {
	if err := s.retainFiles(append([]string{record.name}, dependencies...)...); err != nil {
		return storageIOError(err)
	}
	size := int64(len(record.data))
	for _, file := range missing {
		size += int64(len(file.data))
	}
	if err := s.maintainStorage(record.name, size); err != nil {
		return err
	}
	if len(dependencies) > 0 {
		if err := s.writeDependencies(missing); err != nil {
			return storageIOError(err)
		}
	}
	return storageIOError(s.writeFile(record.name, record.pattern, record.data))
}

func (s *mekugiReplayStore) writeDependencies(files []managedFile) error {
	for _, file := range files {
		if err := s.writeFile(file.name, file.pattern, file.data); err != nil {
			return err
		}
	}
	return nil
}

// Readers acquire this lease before store.lock; cleanup only tries it without
// waiting while holding store.lock. Thus input validation can span several
// reads without allowing another router to retire its not-yet-adopted history.
func (s *mekugiReplayStore) lockStorageSnapshot(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	path := filepath.Join(s.directory, "retention-snapshot.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("invalid retention snapshot lease")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lease := flock.New(path, flock.SetPermissions(0600))
	ok, err := lease.TryRLockContext(ctx, 25*time.Millisecond)
	if err != nil || !ok {
		return nil, errors.Join(err, ctx.Err())
	}
	return func() { _ = lease.Unlock() }, nil
}

// Age-based cleanup is router maintenance, not part of any request's replay
// view. An unrelated catalog must not prevent a new thread from starting.
func runStorageRetention(ctx context.Context, store func() *mekugiReplayStore, notice func(error)) {
	var debugSweep time.Time
	cleanup := func() {
		if time.Since(debugSweep) >= time.Hour {
			debugSweep = time.Now()
			if err := cleanupDebugBundles(ctx, debugSweep); err != nil && ctx.Err() == nil && notice != nil {
				notice(fmt.Errorf("debug bundle cleanup: %w", err))
			}
		}
		if err := store().cleanupSessions(ctx); err != nil && ctx.Err() == nil && notice != nil {
			notice(fmt.Errorf("session cleanup: %w", err))
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
