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
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/yusing/mekugi"
)

const maxReplayRecordBytes = 32 << 20

// Durable records are immutable translation facts. Request-local confirmation and
// ordering are intentionally absent. The store never evicts resumable history.
type mekugiReplayStore struct {
	directory          string
	maxBytes           int64
	maxCommentaryBytes int64
}
type replayRecord struct {
	Version    int
	Workspace  string
	CallID     string
	Commentary bool
	History    replayHistory
}
type replayHistory struct {
	ToolName             string
	PluginID             string
	Script               string
	Root                 string
	Evaluated            string
	ChangeID             string
	ReviewFiles          []mekugi.ReviewFile
	Patch                string
	Applied              bool
	CarrierName          string
	CarrierKind          codeModeCarrierKind
	CarrierPayload       string
	Report               string
	JournalIDs           []string
	OutputWarning        string
	TranslationError     string
	EvaluatorRejected    bool
	Rejections           []mekugi.HostRejection
	CorrelationID        string
	Attempt              int
	UpstreamItem         map[string]json.RawMessage
	ReplayCarrier        bool
	CommentaryMessageIDs []string
	Unevaluated          bool
	AlreadySatisfied     bool
	Aliases              []mekugi.TargetAlias
}

func durableHistory(h mekugiHistory) replayHistory {
	return replayHistory{
		ToolName:             h.toolName,
		PluginID:             h.pluginID,
		Script:               h.script,
		Root:                 h.root,
		Evaluated:            h.evaluated,
		ChangeID:             h.changeID,
		ReviewFiles:          h.reviewFiles,
		Patch:                h.patch,
		Applied:              h.applied,
		CarrierName:          h.carrierName,
		CarrierKind:          h.carrierKind,
		CarrierPayload:       h.carrierPayload,
		Report:               h.report,
		JournalIDs:           h.journalIDs,
		OutputWarning:        h.outputWarning,
		TranslationError:     h.translationError,
		EvaluatorRejected:    h.evaluatorRejected,
		Rejections:           h.rejections,
		CorrelationID:        h.correlationID,
		Attempt:              h.attempt,
		UpstreamItem:         h.upstreamItem,
		ReplayCarrier:        h.replayCarrier,
		CommentaryMessageIDs: h.commentaryMessageIDs,
		Unevaluated:          h.unevaluated,
		AlreadySatisfied:     h.alreadySatisfied,
		Aliases:              h.aliases,
	}
}
func (h replayHistory) history() mekugiHistory {
	return mekugiHistory{
		toolName:             h.ToolName,
		pluginID:             h.PluginID,
		script:               h.Script,
		root:                 h.Root,
		evaluated:            h.Evaluated,
		changeID:             h.ChangeID,
		reviewFiles:          h.ReviewFiles,
		patch:                h.Patch,
		applied:              h.Applied,
		carrierName:          h.CarrierName,
		carrierKind:          h.CarrierKind,
		carrierPayload:       h.CarrierPayload,
		report:               h.Report,
		journalIDs:           h.JournalIDs,
		outputWarning:        h.OutputWarning,
		translationError:     h.TranslationError,
		evaluatorRejected:    h.EvaluatorRejected,
		rejections:           h.Rejections,
		correlationID:        h.CorrelationID,
		attempt:              h.Attempt,
		upstreamItem:         h.UpstreamItem,
		replayCarrier:        h.ReplayCarrier,
		commentaryMessageIDs: h.CommentaryMessageIDs,
		unevaluated:          h.Unevaluated,
		alreadySatisfied:     h.AlreadySatisfied,
		aliases:              h.Aliases,
	}
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
func (s *mekugiReplayStore) locked(ctx context.Context, fn func() error) error {
	return s.withLock(ctx, false, fn)
}

func (s *mekugiReplayStore) readLocked(ctx context.Context, fn func() error) error {
	return s.withLock(ctx, true, fn)
}

func (s *mekugiReplayStore) withLock(ctx context.Context, readOnly bool, fn func() error) (err error) {
	path := filepath.Join(s.directory, "store.lock")
	info, e := os.Lstat(path)
	if e == nil && !info.Mode().IsRegular() {
		return errors.New("replay lock is not a regular file")
	}
	if e != nil && (readOnly || !errors.Is(e, os.ErrNotExist)) {
		return e
	}
	options := []flock.Option{flock.SetPermissions(0600)}
	if readOnly {
		options = append(options, flock.SetFlag(os.O_RDONLY))
	}
	lock := flock.New(path, options...)
	acquire := lock.TryLockContext
	if readOnly {
		acquire = lock.TryRLockContext
	}
	ok, err := acquire(ctx, 25*time.Millisecond)
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
		h = r.History.history()
		found = ok
		return e
	})
	return
}
func (s *mekugiReplayStore) hasCommentary(ctx context.Context, workspace, id string) (found bool, err error) {
	if s == nil {
		return false, nil
	}
	err = s.locked(ctx, func() error { _, ok, e := s.read(workspace, id, true); found = ok; return e })
	return
}
func (s *mekugiReplayStore) putCommentary(ctx context.Context, workspace string, ids []string) error {
	if s == nil {
		return nil
	}
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
	return s.locked(ctx, func() error {
		for id, h := range histories {
			if err := s.write(replayRecord{Version: 1, Workspace: workspace, CallID: id, History: durableHistory(h)}); err != nil {
				return err
			}
		}
		return s.publishChanges(workspace, histories)
	})
}

// Upstream completion may add fields or finalize status after an SSE item is
// already durable, including opaque provider passthrough metadata. It cannot
// alter the original model payload or translation.
func mergeReplayHistory(old, next replayHistory) (replayHistory, error) {
	oldItem, nextItem := old.UpstreamItem, next.UpstreamItem
	oldIDs, nextIDs := old.CommentaryMessageIDs, next.CommentaryMessageIDs
	old.UpstreamItem = nil
	next.UpstreamItem = nil
	old.CommentaryMessageIDs = nil
	next.CommentaryMessageIDs = nil
	if !reflect.DeepEqual(old, next) {
		return next, errors.New("conflicting durable replay translation")
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
				return next, fmt.Errorf("conflicting durable replay item field %q", k)
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
	data, err := marshalProtocolJSON(r)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return errors.New("durable replay record capacity reached")
	}
	name := replayRecordName(r.Workspace, r.CallID, r.Commentary)
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	total := int64(len(data))
	prefix, limit := "call-", s.maxBytes
	if r.Commentary {
		prefix, limit = "commentary-", s.maxCommentaryBytes
	}

	for _, entry := range entries {
		if entry.Name() == name || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unexpected non-regular replay store entry")
		}
		total += info.Size()
	}
	if total > limit {
		return errors.New("durable replay store quota reached; explicit cleanup required")
	}
	f, err := os.CreateTemp(s.directory, prefix+"pending-")
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.directory, name)); err != nil {
		return err
	}
	return syncReplayDirectory(s.directory)
}

func syncReplayDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
