package router

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const maxReplayRecordBytes = 32 << 20

// Durable records are immutable translation facts. Request-local confirmation and
// ordering are intentionally absent. Session retention removes only inactive, unshared records.
type mekugiReplayStore struct {
	directory          string
	maxBytes           int64
	session            storageSessionIdentity
	storageNotice      func(string, string)
	liveDiff           func([]liveDiffChange)
	maxCommentaryBytes int64
}
type replayRecord struct {
	Version      int
	Workspace    string
	CallID       string
	Commentary   bool
	CaptureOrder uint64 `json:",omitzero"`
	History      mekugiHistory
}

// Keep request-local state out of immutable replay comparisons as well as JSON.
func durableHistory(h mekugiHistory) mekugiHistory {
	h.bytes, h.confirmed, h.sequence = 0, false, 0
	if len(h.NativePatches) == 0 {
		h.NativePatches = nil // Empty omitted slices read back as nil.
	}
	if h.ExecObservation != nil {
		observation := *h.ExecObservation
		observation.WindowStart = observation.WindowStart.UTC()
		observation.Files = slices.Clone(observation.Files)
		for i := range observation.Files {
			// The live-preview stamp is intentionally not serialized. Keep the
			// request's copy while comparing against the persisted capture.
			observation.Files[i].watchStamp = ""
		}
		observation.Listings = slices.Clone(observation.Listings)
		for i := range observation.Listings {
			if len(observation.Listings[i].Entries) == 0 {
				observation.Listings[i].Entries = nil // Omitted empty maps read back as nil.
			}
		}
		h.ExecObservation = &observation
	}
	return h
}

func defaultMekugiReplayDirectory() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("XDG_STATE_HOME must be absolute")
	}
	return filepath.Join(base, "mekugi", "replay"), nil
}
func openMekugiReplayStore(directory string) (*mekugiReplayStore, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	// Inspect existing ancestors before MkdirAll can create anything through a
	// symlink. The second check below also validates the completed path.
	for ancestor := directory; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("replay directory must not contain symlinks")
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	if resolved != directory {
		return nil, errors.New("replay directory must not contain symlinks")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, err
	}
	// Sync the complete ancestry, including on startup retries: an existing
	// directory can be left by an earlier creation whose parent sync failed.
	for ancestor := directory; ; ancestor = filepath.Dir(ancestor) {
		if err := syncReplayDirectory(ancestor); err != nil {
			return nil, fmt.Errorf("persist replay directory ancestry: %w", err)
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	s := &mekugiReplayStore{directory: directory, maxBytes: 1 << 30, maxCommentaryBytes: 16 << 20}
	if err := s.locked(context.Background(), func() error { return nil }); err != nil {
		return nil, err
	}
	return s, nil
}
func replayRecordName(workspace, callID string, commentary bool) string {
	prefix := "call-"
	if commentary {
		prefix = "commentary-"
	}
	return prefix + fmt.Sprintf("%x.json", sha256.Sum256(fmt.Appendf(nil, "%t\x00%s\x00%s", commentary, workspace, callID)))
}
func (s *mekugiReplayStore) locked(ctx context.Context, fn func() error) (err error) {
	path := filepath.Join(s.directory, "store.lock")
	info, e := os.Lstat(path)
	if e == nil && !info.Mode().IsRegular() {
		return errors.New("replay lock is not a regular file")
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	lock := flock.New(path, flock.SetPermissions(0600))
	ok, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return ctx.Err()
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
func (s *mekugiReplayStore) read(workspace, callID string, commentary bool) (replayRecord, bool, error) {
	name := filepath.Join(s.directory, replayRecordName(workspace, callID, commentary))
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return replayRecord{}, false, nil
	}
	if err != nil {
		return replayRecord{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxReplayRecordBytes {
		return replayRecord{}, false, errors.New("invalid replay record file")
	}
	f, err := os.Open(name)
	if err != nil {
		return replayRecord{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReplayRecordBytes+1))
	if err != nil {
		return replayRecord{}, false, err
	}
	if len(data) > maxReplayRecordBytes {
		return replayRecord{}, false, errors.New("replay record exceeds size limit")
	}
	var r replayRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, false, fmt.Errorf("decode replay record: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return r, false, errors.New("trailing replay record data")
	}
	if r.Version != 1 || r.Workspace != workspace || r.CallID != callID || r.Commentary != commentary {
		return r, false, errors.New("replay record identity/version mismatch")
	}
	return r, true, nil
}
func (s *mekugiReplayStore) lookup(ctx context.Context, workspace, callID string) (h mekugiHistory, found bool, err error) {
	if s == nil {
		return h, false, nil
	}
	err = s.locked(ctx, func() error {
		r, ok, e := s.read(workspace, callID, false)
		h = r.History
		found = ok
		return e
	})
	return
}
func (s *mekugiReplayStore) hasCommentary(ctx context.Context, workspace, id string) (found bool, err error) {
	if s == nil {
		return false, nil
	}
	err = s.locked(ctx, func() error {
		_, ok, e := s.read(workspace, id, true)
		found = ok
		return e
	})
	return
}
func (s *mekugiReplayStore) putCommentary(ctx context.Context, workspace string, ids []string) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	return s.locked(ctx, func() error {
		for _, id := range ids {
			if err := s.write(replayRecord{Version: 1, Workspace: workspace, CallID: id, Commentary: true}); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *mekugiReplayStore) put(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	return s.locked(ctx, func() error {
		for _, id := range slices.SortedFunc(maps.Keys(histories), func(a, b string) int {
			return cmp.Or(cmp.Compare(histories[a].sequence, histories[b].sequence), strings.Compare(a, b))
		}) {
			if err := s.write(replayRecord{Version: 1, Workspace: workspace, CallID: id, History: durableHistory(histories[id])}); err != nil {
				return err
			}
		}
		return s.publishChanges(workspace, histories)
	})
}

// Upstream completion may add fields or finalize status after an SSE item is
// already durable, including opaque provider passthrough metadata. It cannot
// alter the original model payload or translation.
func mergeReplayHistory(old, next mekugiHistory) (mekugiHistory, error) {
	// Older retained timestamps can carry an offset even when the current
	// file-clock capture is UTC. Compare their durable instants, not locations.
	old, next = durableHistory(old), durableHistory(next)
	oldItem, nextItem := old.UpstreamItem, next.UpstreamItem
	oldIDs, nextIDs := old.CommentaryMessageIDs, next.CommentaryMessageIDs
	old.UpstreamItem = nil
	next.UpstreamItem = nil
	old.CommentaryMessageIDs = nil
	next.CommentaryMessageIDs = nil
	if !reflect.DeepEqual(old, next) {
		return next, criticalDiagnostic(errors.New("conflicting durable replay translation"),
			"replay_translation_conflict", "a completed tool call conflicted with its retained replay translation", true)
	}
	merged := make(map[string]json.RawMessage, len(oldItem)+len(nextItem))
	maps.Copy(merged, oldItem)
	for k, v := range nextItem {
		previous, exists := merged[k]
		// Persistence compacts RawMessage whitespace. Compare that spelling,
		// preserving string escapes and object order rather than decoding values.
		var compact bytes.Buffer
		if err := json.Compact(&compact, v); err != nil {
			return next, fmt.Errorf("invalid durable replay item field %q: %w", k, err)
		}
		v = bytes.Clone(compact.Bytes())
		if exists {
			compact.Reset()
			if err := json.Compact(&compact, previous); err != nil {
				return next, fmt.Errorf("invalid retained replay item field %q: %w", k, err)
			}
			previous = compact.Bytes()
			// The provider enriches this opaque metadata between input.done and
			// output_item.done. Preserve its latest spelling for replay without
			// relaxing checks on tool identity, input, or other item fields.
			metadata := k == "internal_chat_message_metadata_passthrough"
			finalized := k == "status" && string(previous) == `"in_progress"` &&
				(string(v) == `"completed"` || string(v) == `"incomplete"`)
			if !bytes.Equal(previous, v) && !metadata && !finalized {
				return next, criticalDiagnostic(fmt.Errorf("conflicting durable replay item field %q", k),
					"replay_item_conflict", "a completed tool call changed a retained replay item field", true)
			}
		}
		merged[k] = v
	}
	if len(merged) > 0 {
		next.UpstreamItem = merged
	}
	next.CommentaryMessageIDs = slices.Clone(oldIDs)
	for _, id := range nextIDs {
		if !slices.Contains(next.CommentaryMessageIDs, id) {
			next.CommentaryMessageIDs = append(next.CommentaryMessageIDs, id)
		}
	}
	return next, nil
}
func (s *mekugiReplayStore) write(r replayRecord) (err error) {
	previous, exists, err := s.read(r.Workspace, r.CallID, r.Commentary)
	if err != nil {
		return err
	}
	if exists {
		r.CaptureOrder = previous.CaptureOrder
		r.History, err = mergeReplayHistory(previous.History, r.History)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(previous, r) {
			// A prior rename may have succeeded while its directory sync failed.
			// Even an identical retry must establish durability before success.
			return syncReplayDirectory(s.directory)
		}
	}
	if !exists && !r.Commentary && r.History.ChangeID != "" && len(r.History.ReviewFiles) > 0 {
		r.CaptureOrder, err = s.nextCaptureOrder()
		if err != nil {
			return err
		}
	}
	data, err := marshalProtocolJSON(r)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return storageCapacityError("replay record", int64(len(data)), maxReplayRecordBytes, "Split the tool call or reduce its retained output before retrying.")
	}
	name := replayRecordName(r.Workspace, r.CallID, r.Commentary)
	prefix := "call-"
	if r.Commentary {
		prefix = "commentary-"
	}
	return s.writeManagedFile(name, prefix+"pending-", data)
}

// writeFile publishes an already-validated record. Callers retain their lock,
// schema, identity, and quota policies; all records share the durability sequence.
func (s *mekugiReplayStore) writeFile(name, pattern string, data []byte) error {
	if err := writeAtomicFile(filepath.Join(s.directory, name), pattern, data, true); err != nil {
		return err
	}
	return syncReplayDirectory(s.directory)
}

// writeAtomicFile publishes in the destination directory. Durability beyond
// the file itself (directory sync), quotas, and locks remain caller-owned.
func writeAtomicFile(path, pattern string, data []byte, syncFile bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if syncFile {
		if err = f.Sync(); err != nil {
			return err
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func syncReplayDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
