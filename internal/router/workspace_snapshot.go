package router

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// Workspace snapshots bound the file effects of writer calls whose targets
// the parsers cannot name. A private Git directory and index below the replay
// store record the selected workspace before a call and when its result is
// reconciled. Inside a Git repository they borrow its objects and seed their
// index from a copy of its index, but never write its state, run its hooks or
// filters, or follow user or system Git configuration.
const (
	workspaceSnapshotDirectory = "workspace-snapshots"
	// workspaceSnapshotWait bounds how long forwarding a call waits for its
	// checkpoint. A first or slow snapshot keeps warming in the background.
	workspaceSnapshotWait = time.Second
	workspaceSnapshotWarm = 2 * time.Minute
	// A failed warmup is retried by a checkpoint after workspaceSnapshotRetry.
	workspaceSnapshotRetry = 30 * time.Second
	// Without a Git repository there are no authoritative ignore rules. The
	// first snapshot is bounded, and later checkpoints admit only a bounded
	// number of new untracked files per directory, so an installation tree
	// does not become an agent edit.
	workspaceSnapshotPlainFiles = 20000
	workspaceSnapshotPlainAdds  = 256
	// A private object store past workspaceSnapshotStoreBytes starts over;
	// calls whose checkpoint it held keep only their named evidence. A
	// workspace unused for workspaceSnapshotIdle loses its private state.
	workspaceSnapshotStoreBytes = 512 << 20
	workspaceSnapshotStoreCheck = 64
	workspaceSnapshotIdle       = 30 * 24 * time.Hour
)

var workspaceSnapshotVCSMetadata = []string{".hg/", ".svn/", ".jj/", ".bzr/"}

// workspaceSnapshotAttributes overrides every attribute that converts content,
// so snapshots hold the bytes on disk rather than Git's normalized form, and
// no filter driver runs.
const workspaceSnapshotAttributes = "* -text -eol -crlf -filter -ident -working-tree-encoding\n"

// workspaceSnapshotSeeded marks an index copied from the repository that has
// not yet been rehashed under the private attributes.
const workspaceSnapshotSeeded = "mekugi-seeded"

// workspaceSnapshotClaims are snapshot states that a finished call recorded,
// by private repository, so a claim never crosses comparison domains.
type workspaceSnapshotClaims map[string]map[string]workspaceSnapshotEntry

type workspaceSnapshots struct {
	directory string
	// ctx bounds background warm-ups; close cancels it and waits for them.
	ctx     context.Context
	cancel  context.CancelFunc
	warmups sync.WaitGroup
	mu      sync.Mutex
	closed  bool
	repos   map[string]*workspaceSnapshotRepo
}

func newWorkspaceSnapshots(storeDirectory string) *workspaceSnapshots {
	ctx, cancel := context.WithCancel(context.Background())
	return &workspaceSnapshots{directory: filepath.Join(storeDirectory, workspaceSnapshotDirectory), ctx: ctx, cancel: cancel, repos: make(map[string]*workspaceSnapshotRepo)}
}

// close stops background warm-ups and waits until none writes the store.
func (s *workspaceSnapshots) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.warmups.Wait()
}

// workspaceSnapshotEntry is one path's state in a snapshot tree. An empty mode
// is an absent path.
type workspaceSnapshotEntry struct {
	mode, oid string
}

// execSnapshotChange is a snapshot-derived before/after pair for reconcile.
type execSnapshotChange struct {
	before, after execFileSnapshot
}

type workspaceSnapshotRepo struct {
	owner     *workspaceSnapshots
	workspace string
	git       string
	gitDir    string
	index     string
	lockPath  string
	// worktree is the real repository's top level in Git mode, so its ignore
	// files apply; pathspec limits the snapshot to the workspace below it.
	worktree  string
	pathspec  string
	plain     bool
	durable   bool // Run-owned snapshots require complete indexing and retain their objects.
	committed bool // Reconstructed SVN baselines contain only versioned files.
	excludes  string
	exclude   string // the real repository's info/exclude
	objects   string // the real repository's object directory
	seed      string // the real repository's index
	gate      chan struct{}
	// finalize serializes snapshot comparisons and their claims.
	finalize sync.Mutex
	// snapshots counts refreshes since the store size was last checked.
	snapshots int

	mu      sync.Mutex
	ready   chan struct{}
	warming bool
	warmed  bool
	failed  time.Time
}

func (s *workspaceSnapshots) repo(workspace string) *workspaceSnapshotRepo {
	if s == nil || !filepath.IsAbs(workspace) {
		return nil
	}
	s.mu.Lock()
	if repo := s.repos[workspace]; repo != nil {
		s.mu.Unlock()
		return repo
	}
	name := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))[:32]
	directory := filepath.Join(s.directory, name)
	repo := &workspaceSnapshotRepo{
		owner: s, workspace: workspace, gitDir: filepath.Join(directory, "git"),
		index: filepath.Join(directory, "index"), lockPath: filepath.Join(directory, "lock"),
		gate: make(chan struct{}, 1),
	}
	s.repos[workspace] = repo
	s.mu.Unlock()
	repo.startWarm()
	return repo
}

func (r *workspaceSnapshotRepo) startWarm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warming {
		return
	}
	owner := r.owner
	owner.mu.Lock()
	closed := owner.closed
	if !closed {
		owner.warmups.Add(1)
	}
	owner.mu.Unlock()
	r.warming, r.ready = true, make(chan struct{})
	ready := r.ready
	if closed {
		r.warming = false
		close(ready)
		return
	}
	go func() {
		defer owner.warmups.Done()
		ctx, cancel := context.WithTimeout(owner.ctx, workspaceSnapshotWarm)
		defer cancel()
		_, err := r.snapshot(ctx)
		r.mu.Lock()
		r.warming, r.warmed = false, err == nil
		if err != nil {
			r.failed = time.Now()
		}
		r.mu.Unlock()
		close(ready)
	}()
}

// checkpoint records the workspace and returns its tree, or "" when no
// snapshot is available in time. A missing checkpoint only narrows coverage
// to the source-named targets; it never blocks the call.
func (s *workspaceSnapshots) checkpoint(ctx context.Context, workspace string) string {
	repo := s.repo(workspace)
	if repo == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, workspaceSnapshotWait)
	defer cancel()
	repo.mu.Lock()
	ready := repo.ready
	repo.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return ""
	}
	repo.mu.Lock()
	usable, retry := repo.warmed, time.Since(repo.failed) >= workspaceSnapshotRetry
	repo.mu.Unlock()
	if !usable {
		if retry {
			// A transient failure, such as a busy lock or a missing Git, does
			// not end snapshots for the workspace.
			repo.startWarm()
		}
		return ""
	}
	tree, err := repo.snapshot(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// Let a large refresh finish outside forwarding; the next
			// checkpoint reuses its index.
			repo.mu.Lock()
			repo.warmed = false
			repo.mu.Unlock()
			repo.startWarm()
		}
		return ""
	}
	return tree
}

// serialize holds the workspace's comparison step until the returned
// function is called.
func (s *workspaceSnapshots) serialize(workspace string) func() {
	repo := s.repo(workspace)
	if repo == nil {
		return func() {}
	}
	repo.finalize.Lock()
	return repo.finalize.Unlock
}

func (r *workspaceSnapshotRepo) snapshot(ctx context.Context) (string, error) {
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := os.MkdirAll(filepath.Dir(r.lockPath), 0o700); err != nil {
		return "", err
	}
	// Router processes sharing a store serialize on the private index.
	lock := flock.New(r.lockPath, flock.SetPermissions(0o600))
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !locked {
		return "", errors.Join(err, ctx.Err())
	}
	defer lock.Unlock()
	now := time.Now()
	_ = os.Chtimes(r.lockPath, now, now) // Marks the workspace as in use.
	// Holding the lock, any index.lock belongs to an interrupted refresh.
	_ = os.Remove(r.index + ".lock")
	if _, err := os.Stat(filepath.Join(r.gitDir, "HEAD")); err != nil {
		// Another process pruned this workspace's state as idle.
		r.git = ""
	}
	if r.git != "" && r.snapshots >= workspaceSnapshotStoreCheck {
		r.snapshots = 0
		if r.storeBytes(ctx) > workspaceSnapshotStoreBytes {
			if err := errors.Join(os.RemoveAll(r.gitDir), os.Remove(r.index)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			r.git = ""
		}
	}
	if r.git == "" {
		if !r.durable {
			r.owner.pruneIdle(filepath.Dir(r.lockPath))
		}
		if err := r.setup(ctx); err != nil {
			return "", err
		}
	}
	r.snapshots++
	if err := r.refreshPrivate(); err != nil {
		return "", err
	}
	tree, err := r.refresh(ctx)
	if err != nil && ctx.Err() == nil && strings.Contains(err.Error(), "fatal:") {
		// A seeded index can depend on state private to the repository, such
		// as a split index, an unmerged entry, or a pruned object. Start from
		// an empty index instead; comparisons are limited to the workspace.
		_ = os.Remove(r.index)
		tree, err = r.refresh(ctx)
	}
	return tree, err
}

// storeBytes reports the private object store's size, or zero when unknown.
func (r *workspaceSnapshotRepo) storeBytes(ctx context.Context) int64 {
	output, err := r.run(ctx, nil, "count-objects", "-v")
	if err != nil {
		return 0
	}
	var kib int64
	for line := range strings.SplitSeq(string(output), "\n") {
		name, value, _ := strings.Cut(line, ": ")
		if name == "size" || name == "size-pack" || name == "size-garbage" {
			size, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			kib += size
		}
	}
	return kib << 10
}

// pruneIdle removes other workspaces' private state that no router process
// has used for workspaceSnapshotIdle.
func (s *workspaceSnapshots) pruneIdle(keep string) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		directory := filepath.Join(s.directory, entry.Name())
		if !entry.IsDir() || directory == keep {
			continue
		}
		lockPath := filepath.Join(directory, "lock")
		info, err := os.Stat(lockPath)
		if err != nil || time.Since(info.ModTime()) < workspaceSnapshotIdle {
			continue
		}
		lock := flock.New(lockPath)
		if locked, err := lock.TryLock(); err == nil && locked {
			_ = os.RemoveAll(directory)
			_ = lock.Unlock()
		}
	}
}

func (r *workspaceSnapshotRepo) refresh(ctx context.Context) (string, error) {
	var err error
	if r.durable {
		args := []string{"add", "-A"}
		if r.committed {
			args = append(args, "--force")
		}
		_, err = r.run(ctx, nil, append(args, "--", r.pathspec)...)
	} else if r.plain {
		err = r.addPlain(ctx)
	} else {
		err = r.add(ctx, nil, "add", "-A", "--ignore-errors", "--", r.pathspec)
	}
	if err == nil {
		err = r.renormalize(ctx)
	}
	if err != nil {
		return "", err
	}
	output, err := r.run(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// renormalize rehashes a seeded index once when the repository declares
// attributes: seeded entries hold Git's converted content, while snapshots
// record the bytes on disk.
func (r *workspaceSnapshotRepo) renormalize(ctx context.Context) error {
	marker := filepath.Join(r.gitDir, workspaceSnapshotSeeded)
	if _, err := os.Stat(marker); err != nil {
		return nil
	}
	attributes, _, err := r.list(ctx, 1, "ls-files", "-z", "--", ":(glob)**/.gitattributes")
	if err != nil {
		return err
	}
	if len(attributes) != 0 {
		if err := r.add(ctx, nil, "add", "--renormalize", "--ignore-errors", "--", r.pathspec); err != nil {
			return err
		}
	}
	return os.Remove(marker)
}

func (r *workspaceSnapshotRepo) setup(ctx context.Context) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return errors.New("git is unavailable")
	}
	userEnv := workspaceSnapshotUserEnv()
	r.worktree, r.pathspec, r.plain, r.exclude, r.objects, r.seed = r.workspace, ".", true, "", "", ""
	if !r.durable {
		query := exec.CommandContext(ctx, git, "-c", "core.fsmonitor=false", "-C", r.workspace,
			"rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--git-path", "index")
		query.Env, query.WaitDelay = userEnv, 100*time.Millisecond
		if output, err := query.Output(); err == nil {
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			if len(lines) == 3 && filepath.IsAbs(lines[0]) {
				if relative, err := filepath.Rel(lines[0], r.workspace); err == nil && filepath.IsLocal(relative) {
					r.worktree, r.pathspec, r.plain = lines[0], filepath.ToSlash(relative), false
					r.exclude = filepath.Join(lines[1], "info", "exclude")
					r.objects, r.seed = filepath.Join(lines[1], "objects"), lines[2]
				}
			}
		}
	}
	// Snapshot commands ignore user configuration, so the configured global
	// excludes file is passed explicitly; Git finds the default one itself.
	configured := exec.CommandContext(ctx, git, "-C", r.workspace, "config", "--path", "--get", "core.excludesFile")
	configured.Env, configured.WaitDelay = userEnv, 100*time.Millisecond
	r.excludes = ""
	if output, err := configured.Output(); err == nil {
		r.excludes = strings.TrimSpace(string(output))
	}
	if _, err := os.Stat(filepath.Join(r.gitDir, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(r.gitDir, 0o700); err != nil {
			return err
		}
		init := exec.CommandContext(ctx, git, "init", "--bare", "--quiet", r.gitDir)
		init.Env = append(workspaceSnapshotUserEnv(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		init.WaitDelay = 100 * time.Millisecond
		if output, err := init.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %w: %s", err, bytes.TrimSpace(output))
		}
	}
	// Borrowed objects keep unchanged tracked content out of the private
	// store; only new content is written there.
	alternates := filepath.Join(r.gitDir, "objects", "info", "alternates")
	if r.objects == "" {
		if err := os.Remove(alternates); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if err := os.WriteFile(alternates, []byte(r.objects+"\n"), 0o600); err != nil {
			return err
		}
		if _, err := os.Stat(r.index); errors.Is(err, os.ErrNotExist) {
			// The repository's index already knows its files' stat data, so
			// the first snapshot rehashes only what differs from it.
			if data, err := os.ReadFile(r.seed); err == nil && os.WriteFile(r.index, data, 0o600) == nil {
				if err := os.WriteFile(filepath.Join(r.gitDir, workspaceSnapshotSeeded), nil, 0o600); err != nil {
					return err
				}
			}
		}
	}
	r.git = git
	return nil
}

// refreshPrivate writes the private attributes and excludes.
func (r *workspaceSnapshotRepo) refreshPrivate() error {
	if err := workspaceSnapshotWrite(filepath.Join(r.gitDir, "info", "attributes"), []byte(workspaceSnapshotAttributes)); err != nil {
		return err
	}
	return r.refreshExclude()
}

// refreshExclude mirrors the real repository's local excludes, and excludes
// other VCS metadata and the replay store itself.
func (r *workspaceSnapshotRepo) refreshExclude() error {
	var content bytes.Buffer
	if r.exclude != "" {
		if data, err := os.ReadFile(r.exclude); err == nil {
			content.Write(data)
			content.WriteByte('\n')
		}
	}
	for _, metadata := range workspaceSnapshotVCSMetadata {
		content.WriteString(metadata + "\n")
	}
	if relative, err := filepath.Rel(r.worktree, filepath.Dir(r.owner.directory)); err == nil && filepath.IsLocal(relative) && relative != "." {
		content.WriteString("/" + filepath.ToSlash(relative) + "/\n")
	}
	return workspaceSnapshotWrite(filepath.Join(r.gitDir, "info", "exclude"), content.Bytes())
}

func workspaceSnapshotWrite(path string, content []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

// addPlain refreshes known files and admits bounded new files. A first
// snapshot of a workspace too large to bound is unavailable rather than swept.
func (r *workspaceSnapshotRepo) addPlain(ctx context.Context) error {
	if _, err := os.Stat(r.index); errors.Is(err, os.ErrNotExist) {
		_, truncated, err := r.list(ctx, workspaceSnapshotPlainFiles, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil {
			return err
		}
		if truncated {
			return fmt.Errorf("workspace has more than %d files and no Git repository", workspaceSnapshotPlainFiles)
		}
		return r.add(ctx, nil, "add", "-A", "--ignore-errors", "--", ".")
	}
	if err := r.add(ctx, nil, "add", "-u", "--ignore-errors", "--", "."); err != nil {
		return err
	}
	entries, _, err := r.list(ctx, workspaceSnapshotPlainFiles, "ls-files", "-z", "--others", "--directory", "--no-empty-directory", "--exclude-standard")
	if err != nil {
		return err
	}
	// New files count against their parent directory, whether it is new or
	// already known, so neither a new tree nor a burst of files in a known
	// directory is admitted past the bound.
	parents := make(map[string][]string)
	for _, entry := range entries {
		if !strings.HasSuffix(entry, "/") {
			parent := path.Dir(entry)
			parents[parent] = append(parents[parent], entry)
			continue
		}
		files, truncated, err := r.list(ctx, workspaceSnapshotPlainAdds, "ls-files", "-z", "--others", "--exclude-standard", "--", entry)
		if err != nil {
			return err
		}
		if truncated {
			continue
		}
		parent := strings.TrimSuffix(entry, "/")
		parents[parent] = append(parents[parent], files...)
	}
	var admitted []string
	for _, parent := range slices.Sorted(maps.Keys(parents)) {
		if files := parents[parent]; len(files) <= workspaceSnapshotPlainAdds {
			admitted = append(admitted, files...)
		}
	}
	if len(admitted) == 0 {
		return nil
	}
	var stdin bytes.Buffer
	for _, path := range admitted {
		stdin.WriteString(path)
		stdin.WriteByte(0)
	}
	return r.add(ctx, &stdin, "add", "--ignore-errors", "--pathspec-from-file=-", "--pathspec-file-nul")
}

// add tolerates per-file indexing errors, such as an unreadable file, which
// leave that path's previous state; a fatal error fails the snapshot.
func (r *workspaceSnapshotRepo) add(ctx context.Context, stdin io.Reader, args ...string) error {
	_, err := r.run(ctx, stdin, args...)
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil && !strings.Contains(err.Error(), "fatal:") {
		return nil
	}
	return err
}

func (r *workspaceSnapshotRepo) command(ctx context.Context, stdin io.Reader, args ...string) *exec.Cmd {
	flags := []string{
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "core.untrackedCache=true",
		"-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "-c", "core.quotePath=false",
		"-c", "gc.auto=0", "-c", "advice.addEmbeddedRepo=false",
	}
	if r.excludes != "" {
		flags = append(flags, "-c", "core.excludesFile="+r.excludes)
	}
	command := exec.CommandContext(ctx, r.git, append(flags, args...)...)
	command.Dir = r.worktree
	command.Stdin = stdin
	command.WaitDelay = 100 * time.Millisecond
	// User and system configuration could run filters or hooks; the private
	// directory and index replace any inherited repository selection.
	command.Env = append(workspaceSnapshotUserEnv(),
		"GIT_DIR="+r.gitDir, "GIT_WORK_TREE="+r.worktree, "GIT_INDEX_FILE="+r.index,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	return command
}

func (r *workspaceSnapshotRepo) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	command := r.command(ctx, stdin, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 512 {
			message = message[:512]
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, message)
	}
	return stdout.Bytes(), nil
}

// list reads NUL-separated paths, stopping once more than limit arrive.
func (r *workspaceSnapshotRepo) list(ctx context.Context, limit int, args ...string) ([]string, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := r.command(ctx, nil, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := command.Start(); err != nil {
		return nil, false, err
	}
	reader := bufio.NewReader(stdout)
	var entries []string
	truncated := false
	for {
		entry, err := reader.ReadString(0)
		if err != nil {
			break
		}
		if len(entries) == limit {
			truncated = true
			cancel()
			break
		}
		entries = append(entries, strings.TrimSuffix(entry, "\x00"))
	}
	_, _ = io.Copy(io.Discard, reader)
	if err := command.Wait(); err != nil && !truncated {
		return nil, false, fmt.Errorf("git %s: %w", args[0], err)
	}
	return entries, truncated, nil
}

func workspaceSnapshotUserEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

// since compares a writer call's checkpoint with the current workspace.
// Claimed paths start from the state an overlapping call already recorded,
// so one effect is attributed once. It returns the changes and every changed
// path's current state for claiming onto calls that are still open.
func (s *workspaceSnapshots) since(ctx context.Context, workspace, tree string, all workspaceSnapshotClaims) ([]execSnapshotChange, workspaceSnapshotClaims, error) {
	repo := s.repo(workspace)
	if repo == nil || tree == "" {
		return nil, nil, nil
	}
	current := s.checkpoint(ctx, workspace)
	if current == "" {
		return nil, nil, errors.New("no current checkpoint")
	}
	claims := all[repo.gitDir]
	ctx, cancel := context.WithTimeout(ctx, workspaceSnapshotWait)
	defer cancel()
	output, err := repo.run(ctx, nil, "diff-tree", "-r", "-z", "--no-renames", "--raw", tree, current, "--", repo.pathspec)
	if err != nil {
		return nil, nil, err
	}
	type pair struct{ before, after workspaceSnapshotEntry }
	side := func(mode, oid string) workspaceSnapshotEntry {
		if strings.Trim(mode, "0") == "" {
			return workspaceSnapshotEntry{}
		}
		return workspaceSnapshotEntry{mode, oid}
	}
	changed := make(map[string]pair)
	fields := strings.Split(string(output), "\x00")
	for index := 0; index+1 < len(fields); index += 2 {
		header := strings.Fields(strings.TrimPrefix(fields[index], ":"))
		if len(header) < 5 {
			return nil, nil, errors.New("unexpected snapshot comparison output")
		}
		path := filepath.Join(repo.worktree, filepath.FromSlash(fields[index+1]))
		changed[path] = pair{side(header[0], header[2]), side(header[1], header[3])}
	}
	var unchanged []string
	for path := range claims {
		if _, found := changed[path]; !found {
			unchanged = append(unchanged, path)
		}
	}
	if len(unchanged) != 0 {
		states, err := repo.states(ctx, current, unchanged)
		if err != nil {
			return nil, nil, err
		}
		for _, path := range unchanged {
			changed[path] = pair{after: states[path]}
		}
	}
	latest := make(map[string]workspaceSnapshotEntry, len(changed))
	var oids []string
	for path, change := range changed {
		latest[path] = change.after
		if claim, found := claims[path]; found {
			change.before = claim
			changed[path] = change
		}
		if change.before == change.after {
			delete(changed, path)
			continue
		}
		for _, side := range []workspaceSnapshotEntry{change.before, change.after} {
			if side.mode != "" && side.mode != "160000" {
				oids = append(oids, side.oid)
			}
		}
	}
	contents, err := repo.blobs(ctx, oids)
	if err != nil {
		return nil, nil, err
	}
	var changes []execSnapshotChange
	for _, path := range slices.Sorted(maps.Keys(changed)) {
		change := changed[path]
		if change.before.mode == "160000" || change.after.mode == "160000" {
			continue // Nested repositories are not reviewed as files.
		}
		changes = append(changes, execSnapshotChange{
			before: workspaceSnapshotFile(path, change.before, contents),
			after:  workspaceSnapshotFile(path, change.after, contents),
		})
	}
	return changes, workspaceSnapshotClaims{repo.gitDir: latest}, nil
}

// states reads paths' entries in a snapshot tree; absent paths stay zero.
func (r *workspaceSnapshotRepo) states(ctx context.Context, tree string, paths []string) (map[string]workspaceSnapshotEntry, error) {
	args := []string{"ls-tree", "-z", "--full-tree", tree, "--"}
	states := make(map[string]workspaceSnapshotEntry)
	for _, path := range paths {
		relative, err := filepath.Rel(r.worktree, path)
		if err != nil || !filepath.IsLocal(relative) {
			continue
		}
		args = append(args, filepath.ToSlash(relative))
	}
	if len(args) == 5 {
		return states, nil
	}
	output, err := r.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	for line := range strings.SplitSeq(string(output), "\x00") {
		header, name, found := strings.Cut(line, "\t")
		fields := strings.Fields(header)
		if !found || len(fields) != 3 {
			continue
		}
		states[filepath.Join(r.worktree, filepath.FromSlash(name))] = workspaceSnapshotEntry{fields[0], fields[2]}
	}
	return states, nil
}

type workspaceSnapshotBlob struct {
	data  []byte
	size  int64
	error string
}

// blobs reads bounded content with the same limits as named capture.
func (r *workspaceSnapshotRepo) blobs(ctx context.Context, oids []string) (map[string]workspaceSnapshotBlob, error) {
	slices.Sort(oids)
	oids = slices.Compact(oids)
	blobs := make(map[string]workspaceSnapshotBlob, len(oids))
	if len(oids) == 0 {
		return blobs, nil
	}
	request := strings.Join(oids, "\n") + "\n"
	output, err := r.run(ctx, strings.NewReader(request), "cat-file", "--batch-check=%(objectname) %(objectsize)")
	if err != nil {
		return nil, err
	}
	budget := maxExecContentBytes
	var wanted []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		oid, sizeText, _ := strings.Cut(line, " ")
		size, err := strconv.ParseInt(sizeText, 10, 64)
		if err != nil {
			continue
		}
		switch {
		case size > maxNativePatchFileBytes:
			blobs[oid] = workspaceSnapshotBlob{size: size, error: "file exceeds the 8 MiB capture bound"}
		case size > int64(budget):
			blobs[oid] = workspaceSnapshotBlob{size: size, error: "capture budget exhausted"}
		default:
			budget -= int(size)
			wanted = append(wanted, oid)
		}
	}
	if len(wanted) == 0 {
		return blobs, nil
	}
	output, err = r.run(ctx, strings.NewReader(strings.Join(wanted, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for len(output) != 0 {
		end := bytes.IndexByte(output, '\n')
		if end < 0 {
			return nil, errors.New("unexpected snapshot object output")
		}
		fields := strings.Fields(string(output[:end]))
		output = output[end+1:]
		if len(fields) != 3 {
			continue // A missing object leaves its path unavailable.
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size+1 > len(output) {
			return nil, errors.New("unexpected snapshot object output")
		}
		blobs[fields[0]] = workspaceSnapshotBlob{data: output[:size], size: int64(size)}
		output = output[size+1:]
	}
	return blobs, nil
}

func workspaceSnapshotFile(path string, entry workspaceSnapshotEntry, blobs map[string]workspaceSnapshotBlob) execFileSnapshot {
	if entry.mode == "" {
		return execFileSnapshot{Path: path}
	}
	blob, found := blobs[entry.oid]
	switch {
	case !found:
		return execFileSnapshot{Path: path, Kind: execFileText, Error: "snapshot content unavailable"}
	case blob.error != "":
		return execFileSnapshot{Path: path, Kind: execFileText, Size: blob.size, Error: blob.error}
	case entry.mode == "120000":
		return execFileSnapshot{Path: path, Kind: execFileSymlink, Link: string(blob.data)}
	}
	return execDataSnapshot(path, blob.data, nil)
}
