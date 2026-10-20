package presentation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// Source: apps/mekugi/internal/router/vcs_display.go:844:981@[9822b997].
const (
	gitShowTimeout    = 2 * time.Second
	gitShowWaitDelay  = 500 * time.Millisecond
	gitShowBytes      = 4 << 20
	gitShowCacheLimit = 256
	gitShowReaders    = 2
)

// CommitKey identifies only a commit named by observed output, in its proven directory.
type CommitKey struct{ Directory, Hash string }

// CommitReader owns bounded immutable-object reads for one attached Activity session.
// Request never waits for Git. Close cancels outstanding reads without waiting for them.
type CommitReader struct {
	ctx     context.Context
	cancel  context.CancelFunc
	updates chan struct{}
	mu      sync.Mutex
	reads   map[CommitKey][]ChangeRow
	order   []CommitKey
	active  map[CommitKey]bool
}

func NewCommitReader(ctx context.Context) *CommitReader {
	ctx, cancel := context.WithCancel(ctx)
	return &CommitReader{ctx: ctx, cancel: cancel, updates: make(chan struct{}, 1),
		reads: make(map[CommitKey][]ChangeRow), active: make(map[CommitKey]bool)}
}

func (r *CommitReader) Close() {
	if r != nil {
		r.cancel()
	}
}

// Updates coalesces completed reads; callers retry their pending keys on notification.
func (r *CommitReader) Updates() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.updates
}

// Request returns cached file rows, including a nil result for an unreadable object.
// At capacity, it leaves the key with the caller for retry after the next update.
func (r *CommitReader) Request(key CommitKey) (rows []ChangeRow, ready bool) {
	if r == nil || key.Directory == "" || key.Hash == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return nil, false
	}
	if rows, found := r.reads[key]; found {
		return slices.Clone(rows), true
	}
	if r.active[key] || len(r.active) >= gitShowReaders {
		return nil, false
	}
	r.active[key] = true
	go r.read(key)
	return nil, false
}

func (r *CommitReader) read(key CommitKey) {
	ctx, cancel := context.WithTimeout(r.ctx, gitShowTimeout)
	defer cancel()
	output, err := gitShow(ctx, key)
	var rows []ChangeRow
	if err == nil {
		rows = slices.DeleteFunc(gitStatRows("numstat", strings.Split(string(output), "\n")), func(row ChangeRow) bool { return row.Footer })
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, key)
	if r.ctx.Err() != nil {
		return
	}
	if len(r.order) >= gitShowCacheLimit {
		delete(r.reads, r.order[0])
		r.order = slices.Delete(r.order, 0, 1)
	}
	r.reads[key] = rows
	r.order = append(r.order, key)
	select {
	case r.updates <- struct{}{}:
	default:
	}
}

func gitShow(ctx context.Context, key CommitKey) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", "-C", key.Directory, "-c", "core.quotepath=off", "show", "--no-color", "--no-ext-diff", "--no-textconv",
		"-M", "--numstat", "--summary", "--format=", key.Hash+"^{commit}", "--")
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
	command.WaitDelay = gitShowWaitDelay
	var stdout vcsBoundedBuffer
	command.Stdout = &stdout
	err := command.Run()
	if stdout.overflow {
		return nil, errGitShowBound
	}
	if err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

var errGitShowBound = errors.New("git show output exceeds its bound")

type vcsBoundedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *vcsBoundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > gitShowBytes {
		b.overflow = true
		return 0, errGitShowBound
	}
	return b.Buffer.Write(p)
}

// CommitChanges preserves the observed footer while replacing its fallback file rows.
func CommitChanges(rows []ChangeRow, key CommitKey, files []ChangeRow) []ChangeRow {
	if len(rows) == 0 || files == nil {
		return rows
	}
	footer := rows[len(rows)-1]
	if !footer.Footer || footer.Label != key.Hash {
		return rows
	}
	return append(slices.Clone(files), footer)
}
