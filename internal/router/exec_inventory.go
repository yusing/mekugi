package router

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi"
)

const (
	// maxExecInventoryFiles bounds the files whose content an inventory reads
	// before the call. The others keep only their stamps.
	maxExecInventoryFiles = 256
	// maxExecInventoryQueryBytes bounds the output of one VCS listing.
	maxExecInventoryQueryBytes = 32 << 20
	// execInventoryQueryTimeout bounds VCS queries made after the call, when
	// no pre-call hold applies.
	execInventoryQueryTimeout = 5 * time.Second
	// execInventoryNote qualifies inventory reviews: the change happened
	// while the command ran, which does not establish that it made the change.
	execInventoryNote = "observed during command window"
)

// execInventory is the pre-call state of the selected workspace for a call
// whose write scope is unknown. Each walked file costs a stamp; content is read
// within the pre-call budget. Only verified byte-identical Git blobs replace it.
type execInventory struct {
	// Version 1 verifies blob bytes and uses change-clock stamps. Older inventories
	// remain readable, but their unverified blobs and weak stamps are not evidence.
	Version     int                           `json:",omitzero"`
	Directories map[string]execDirectoryStamp `json:",omitempty"`
	Root        string
	// Entries maps each walked file's path relative to Root to its stamp.
	Entries map[string]string `json:",omitempty"`
	// Blobs maps tracked regular files whose content matched the git index
	// to their blob ids.
	Blobs map[string]string `json:",omitempty"`
	// Files holds bounded content not represented by a verified Git blob.
	Files []execFileSnapshot `json:",omitempty"`
	// Repositories lists the git work trees nested below Root, relative to
	// it. Their files' blob ids name objects of the innermost one.
	Repositories []string `json:",omitempty"`
	// Pruned lists the relative paths the walk skipped: VCS metadata,
	// dependency trees. Ordinary ignored files remain eligible for capture.
	Pruned  []string       `json:",omitempty"`
	Omitted []execOmission `json:",omitempty"`
}

// durable drops request-local preview stamps and spells empty collections as
// they read back.
func (i *execInventory) durable() *execInventory {
	if i == nil {
		return nil
	}
	inventory := *i
	inventory.Files = slices.Clone(inventory.Files)
	for index := range inventory.Files {
		inventory.Files[index].watchStamp = ""
	}
	if len(inventory.Files) == 0 {
		inventory.Files = nil
	}
	if len(inventory.Entries) == 0 {
		inventory.Entries = nil
	}
	if len(inventory.Blobs) == 0 {
		inventory.Blobs = nil
	}
	return &inventory
}

var execVCSMetadata = []string{".git", ".hg", ".svn", ".jj"}

// captureExecInventory walks root before the call. Stamps precede Git queries;
// raw content verification shares the capture's remaining budget.
func captureExecInventory(root string, deadline time.Time, budget *int) *execInventory {
	inventory := &execInventory{Root: root, Version: 1}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	walk := execInventoryWalk{root: root, limit: maxExecListingEntries, deadline: deadline}
	entries := make(map[string]string)
	walk.run(func(path, relative string, _ fs.DirEntry) {
		file, _, err := openInventoryFile(root, relative)
		if err != nil {
			walk.omitted = append(walk.omitted, execOmission{Path: path, Reason: err.Error()})
			return
		}
		entries[relative] = "" // Symlinks are compared as targets, never skipped by a stamp.
		if file != nil {
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				walk.omitted = append(walk.omitted, execOmission{Path: path, Reason: err.Error()})
				return
			}
			entries[relative] = execInventoryStamp(info)
		}
	})
	inventory.Entries, inventory.Pruned, inventory.Omitted = entries, walk.pruned, walk.omitted
	inventory.Repositories = walk.repositories
	// Index state prioritizes changed files, but does not establish byte identity.
	candidates := make(map[string]string)
	var priority []string
	for _, repository := range append([]string{""}, walk.repositories...) {
		blobs, changed, err := execGitState(ctx, filepath.Join(root, repository))
		if err != nil {
			continue
		}
		for path, oid := range blobs {
			relative := filepath.Join(repository, path)
			if _, listed := entries[relative]; listed && inventory.repository(relative) == repository {
				candidates[relative] = oid
			}
		}
		for path := range changed {
			relative := filepath.Join(repository, path)
			if _, listed := entries[relative]; listed && inventory.repository(relative) == repository {
				priority = append(priority, relative)
			}
		}
	}
	slices.Sort(priority)
	seen := make(map[string]bool)
	reads := 0
	for _, relative := range append(priority, slices.Sorted(maps.Keys(entries))...) {
		if seen[relative] {
			continue
		}
		seen[relative] = true
		if reads >= maxExecInventoryFiles || time.Now().After(deadline) {
			break
		}
		reads++
		snapshot, _ := snapshotInventoryFile(root, relative, budget, "")
		if snapshot.Error != "" || !execFilePresent(snapshot) {
			continue
		}
		// Read and verify every retained blob against the actual worktree bytes.
		// Filters, encodings and stale Git stat caches cannot invent a baseline.
		if snapshot.Kind == execFileText && execMatchesGitBlob(snapshot.Content, candidates[relative]) {
			if inventory.Blobs == nil {
				inventory.Blobs = make(map[string]string)
			}
			inventory.Blobs[relative] = candidates[relative]
		} else {
			inventory.Files = append(inventory.Files, snapshot)
		}
	}
	// Dependency trees consume only a bounded metadata digest, never file content
	// or a retained descendant listing. Ordinary ignored files are not pruned.
	for _, relative := range walk.directories {
		if inventory.Directories == nil {
			inventory.Directories = make(map[string]execDirectoryStamp)
		}
		inventory.Directories[relative] = captureExecDirectory(root, relative, deadline)
	}
	return inventory
}

func execMatchesGitBlob(content, oid string) bool {
	var digest hash.Hash
	switch len(oid) {
	case 40:
		digest = sha1.New()
	case 64:
		digest = sha256.New()
	default:
		return false
	}
	fmt.Fprintf(digest, "blob %d%c", len(content), 0)
	io.WriteString(digest, content)
	return hex.EncodeToString(digest.Sum(nil)) == oid
}

// repository returns the innermost nested work tree holding relative, or ""
// for root.
func (i *execInventory) repository(relative string) string {
	innermost := ""
	for _, repository := range i.Repositories {
		if strings.HasPrefix(relative, repository+string(filepath.Separator)) && len(repository) > len(innermost) {
			innermost = repository
		}
	}
	return innermost
}

// boundExecInventory drops captured content, largest first, until the
// encoded inventory fits limit. Dropped files remain listed by stamp.
func boundExecInventory(inventory *execInventory, limit int) {
	inventory.Files = slices.Clone(inventory.Files)
	total := len(mustMarshalJSON(inventory))
	for total > limit && len(inventory.Files) > 0 {
		largest := 0
		for index, file := range inventory.Files {
			if len(file.Content) > len(inventory.Files[largest].Content) {
				largest = index
			}
		}
		total -= len(mustMarshalJSON(inventory.Files[largest])) + 1
		inventory.Files = slices.Delete(inventory.Files, largest, largest+1)
	}
}

// execInventoryWalk visits the files below root that an inventory covers. It
// does not follow symlinked directories and skips VCS metadata, dependency
// trees, including those of nested git work trees. Past the
// file limit or deadline it stops and records root as omitted.
type execInventoryWalk struct {
	root     string
	limit    int
	deadline time.Time
	// repositories lists the nested git work trees the walk entered.
	repositories []string
	directories  []string
	pruned       []string
	omitted      []execOmission
}

func (w *execInventoryWalk) run(visit func(path, relative string, entry fs.DirEntry)) {
	visited := 0
	var walk func(string) bool
	walk = func(relative string) bool {
		if !w.deadline.IsZero() && time.Now().After(w.deadline) {
			w.omitted = append(w.omitted, execOmission{Path: w.root, Reason: "capture deadline"})
			return false
		}
		path := filepath.Join(w.root, relative)
		directory, _, err := openInventoryFile(w.root, relative)
		if err != nil || directory == nil {
			w.omitted = append(w.omitted, execOmission{Path: path, Reason: "inventory directory unavailable without following symlinks"})
			return true
		}
		defer directory.Close()
		entries, err := directory.ReadDir(w.limit - visited + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			w.omitted = append(w.omitted, execOmission{Path: path, Reason: err.Error()})
			return true
		}
		visited += len(entries)
		if visited > w.limit {
			w.omitted = append(w.omitted, execOmission{Path: w.root, Reason: fmt.Sprintf("workspace inventory exceeds %d entries", w.limit)})
			return false
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, entry := range entries {
			if !w.deadline.IsZero() && time.Now().After(w.deadline) {
				w.omitted = append(w.omitted, execOmission{Path: w.root, Reason: "capture deadline"})
				return false
			}
			name := filepath.Join(relative, entry.Name())
			path := filepath.Join(w.root, name)
			if slices.Contains(execVCSMetadata, entry.Name()) {
				w.pruned = append(w.pruned, name)
				continue
			}
			if entry.IsDir() {
				if execBuiltinPruned(path, entry.Name()) {
					w.pruned = append(w.pruned, name)
					w.directories = append(w.directories, name)
					continue
				}
				w.enter(path, name)
				if !walk(name) {
					return false
				}
			} else {
				visit(path, name, entry)
			}
		}
		return true
	}
	walk(".")
}

// enter assigns nested Git object identities to their owning work tree.
func (w *execInventoryWalk) enter(path, relative string) error {
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		w.repositories = append(w.repositories, relative)
	}
	return nil
}

// prunedWithin reports whether the walk skipped path or one of its parents.
func (i *execInventory) prunedWithin(relative string) bool {
	for _, pruned := range i.Pruned {
		if relative == pruned || strings.HasPrefix(relative, pruned+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// file returns the captured content of one inventory file, if any.
func (i *execInventory) file(path string) (execFileSnapshot, bool) {
	index := slices.IndexFunc(i.Files, func(file execFileSnapshot) bool { return file.Path == path })
	if index < 0 {
		return execFileSnapshot{}, false
	}
	return i.Files[index], true
}

// blobs reads previously byte-verified Git content within the before budget.
// Legacy inventories did not verify byte identity and cannot supply blobs.
func (i *execInventory) blobs(ctx context.Context, relatives []string, budget *int) map[string]execFileSnapshot {
	if i.Version != 1 {
		return nil
	}
	want := make(map[string]map[string]int64)
	remaining := *budget
	var selected []string
	for _, relative := range relatives {
		repository, oid := i.repository(relative), i.Blobs[relative]
		size, ok := execStampSize(i.Entries[relative])
		if !ok || oid == "" || size < 0 || size > maxNativePatchFileBytes || size > int64(remaining) {
			continue
		}
		if want[repository] == nil {
			want[repository] = make(map[string]int64)
		}
		want[repository][oid] = size
		remaining -= int(size)
		selected = append(selected, relative)
	}
	data := make(map[string]map[string][]byte)
	for repository, objects := range want {
		data[repository] = execGitBlobs(ctx, filepath.Join(i.Root, repository), objects)
	}
	snapshots := make(map[string]execFileSnapshot)
	for _, relative := range selected {
		content, ok := data[i.repository(relative)][i.Blobs[relative]]
		if size, _ := execStampSize(i.Entries[relative]); ok && int64(len(content)) == size {
			snapshots[relative] = execDataSnapshot(filepath.Join(i.Root, relative), content, budget)
		}
	}
	return snapshots
}

func execStampSize(stamp string) (int64, bool) {
	size, _, found := strings.Cut(stamp, ":")
	if !found {
		return 0, false
	}
	value, err := strconv.ParseInt(size, 10, 64)
	return value, err == nil
}

// reconcileExecInventory compares the inventory with the workspace after the
// call. Changed and deleted files take their before-content from captured
// files or git blobs. Files missing from the inventory are creations unless
// the inventory omitted their directory: its named gap stands for them. Skipped
// paths belong to explicit scope, same-cell patches, or overlapping writers.
func reconcileExecInventory(inventory *execInventory, skip func(string) bool, compare func(execFileSnapshot, execFileSnapshot), budget *int) []mekugi.ReviewFile {
	var gaps []mekugi.ReviewFile
	gap := func(beforePath, afterPath, reason string) {
		review := mekugi.RenderIncompleteReviewFile(beforePath, afterPath, reason)
		review.OriginNote = execInventoryNote
		gaps = append(gaps, review)
	}
	ctx, cancel := context.WithTimeout(context.Background(), execInventoryQueryTimeout)
	defer cancel()
	var blobs []string
	after := make(map[string]execFileSnapshot)
	for _, relative := range slices.Sorted(maps.Keys(inventory.Entries)) {
		path := filepath.Join(inventory.Root, relative)
		if skip(path) {
			continue
		}
		stamp := ""
		if inventory.Version == 1 {
			stamp = inventory.Entries[relative]
		}
		current, unchanged := snapshotInventoryFile(inventory.Root, relative, budget, stamp)
		if unchanged {
			continue
		}
		if current.Error != "" {
			gap(path, path, current.Error)
			continue
		}
		if before, captured := inventory.file(path); captured {
			compare(before, current)
		} else if inventory.Version == 1 && inventory.Blobs[relative] != "" {
			blobs = append(blobs, relative)
			after[relative] = current
		} else {
			before := execFileSnapshot{Path: path, Error: "before content not captured"}
			compare(before, current)
		}
	}
	// Before and after have independent content budgets.
	beforeBudget := maxExecContentBytes
	read := inventory.blobs(ctx, blobs, &beforeBudget)
	for _, relative := range blobs {
		before, ok := read[relative]
		if !ok {
			before = execFileSnapshot{Path: filepath.Join(inventory.Root, relative), Error: "before content not captured"}
		}
		compare(before, after[relative])
	}
	omitted := slices.Clone(inventory.Omitted)
	walk := execInventoryWalk{root: inventory.Root, limit: 2 * maxExecListingEntries, deadline: time.Now().Add(execInventoryQueryTimeout)}
	walk.run(func(path, relative string, _ fs.DirEntry) {
		_, listed := inventory.Entries[relative]
		unlisted := inventory.prunedWithin(relative) || slices.ContainsFunc(inventory.Omitted, func(o execOmission) bool { return execPathWithin(path, o.Path) })
		if !listed && !unlisted && !skip(path) {
			current, _ := snapshotInventoryFile(inventory.Root, relative, budget, "")
			compare(execFileSnapshot{Path: path}, current)
		}
	})
	omitted = append(omitted, walk.omitted...)
	directories := slices.Clone(walk.directories)
	for relative := range inventory.Directories {
		if !slices.Contains(directories, relative) {
			directories = append(directories, relative)
		}
	}
	slices.Sort(directories)
	for _, relative := range directories {
		path := filepath.Join(inventory.Root, relative)
		if skip(path) {
			continue
		}
		before, listed := inventory.Directories[relative]
		if !listed {
			if inventory.prunedWithin(relative) || filepath.Base(relative) != "node_modules" && filepath.Base(relative) != "__pycache__" {
				gap(path, path, "dependency directory has no pre-call metadata")
				continue
			}
			before.Complete = true
		}
		if !listed && slices.ContainsFunc(inventory.Omitted, func(o execOmission) bool { return execPathWithin(path, o.Path) }) {
			continue
		}
		current := captureExecDirectory(inventory.Root, relative, walk.deadline)
		if review, ok := execDirectoryReview(path, before, current); ok {
			gaps = append(gaps, review)
		}
	}
	for _, omission := range omitted {
		if !skip(omission.Path) {
			gap(omission.Path, omission.Path, omission.Reason)
		}
	}
	return gaps
}

// execGitState lists the index blob ids of tracked regular files below root,
// with neither assume-unchanged nor skip-worktree set, and the modified and
// untracked files with their ls-files tags ("?" for untracked), all relative
// to root.
func execGitState(ctx context.Context, root string) (blobs map[string]string, changed map[string]string, err error) {
	var index, status []byte
	var indexErr, statusErr error
	var wg sync.WaitGroup
	wg.Go(func() { index, indexErr = execVCSQuery(ctx, root, "git", "ls-files", "-z", "--stage", "-v") })
	wg.Go(func() {
		status, statusErr = execVCSQuery(ctx, root, "git", "ls-files", "-z", "-t", "--modified", "--others", "--exclude-standard")
	})
	wg.Wait()
	if err := errors.Join(indexErr, statusErr); err != nil {
		return nil, nil, err
	}
	blobs = make(map[string]string)
	for record := range strings.SplitSeq(string(index), "\x00") {
		// "H 100644 <oid> 0\t<path>": lower-case h marks assume-unchanged,
		// S skip-worktree; neither proves the file matches the index.
		fields, path, found := strings.Cut(record, "\t")
		parts := strings.Fields(fields)
		if !found || len(parts) != 4 || parts[0] != "H" || parts[3] != "0" || parts[1] != "100644" && parts[1] != "100755" {
			continue
		}
		blobs[filepath.FromSlash(path)] = parts[2]
	}
	changed = make(map[string]string)
	for record := range strings.SplitSeq(string(status), "\x00") {
		if tag, path, found := strings.Cut(record, " "); found && path != "" {
			changed[filepath.FromSlash(path)] = tag
		}
	}
	return blobs, changed, nil
}

// execGitBlobs reads blobs of the wanted sizes with one cat-file process.
// Missing objects and blobs of another size are left out.
func execGitBlobs(ctx context.Context, root string, want map[string]int64) map[string][]byte {
	found := make(map[string][]byte)
	if len(want) == 0 {
		return found
	}
	command := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	command.Stderr = io.Discard
	command.WaitDelay = 10 * time.Millisecond
	var input strings.Builder
	for _, oid := range slices.Sorted(maps.Keys(want)) {
		input.WriteString(oid + "\n")
	}
	command.Stdin = strings.NewReader(input.String())
	stdout, err := command.StdoutPipe()
	if err != nil || command.Start() != nil {
		return found
	}
	defer func() { _ = command.Wait() }()
	reader := bufio.NewReader(stdout)
	for range want {
		header, err := reader.ReadString('\n')
		if err != nil {
			return found
		}
		// "<oid> blob <size>\n<content>\n", or "<oid> missing\n".
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return found
		}
		if fields[1] != "blob" || size != want[fields[0]] {
			if _, err := io.CopyN(io.Discard, reader, size+1); err != nil {
				return found
			}
			continue
		}
		content := make([]byte, size+1)
		if _, err := io.ReadFull(reader, content); err != nil {
			return found
		}
		if execMatchesGitBlob(string(content[:size]), fields[0]) {
			found[fields[0]] = content[:size]
		}
	}
	return found
}

var errExecVCSQueryBound = errors.New("VCS listing exceeds its bound")

func execVCSQuery(ctx context.Context, root, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	command.Stderr = io.Discard
	command.WaitDelay = 10 * time.Millisecond
	var output execVCSOutput
	command.Stdout = &output
	err := command.Run()
	if output.overflow {
		return nil, errExecVCSQueryBound
	}
	return output.Bytes(), err
}

type execVCSOutput struct {
	bytes.Buffer
	overflow bool
}

// Write fails past the bound, so the query stops on a closed pipe.
func (o *execVCSOutput) Write(p []byte) (int, error) {
	if o.Len()+len(p) > maxExecInventoryQueryBytes {
		o.overflow = true
		return 0, errExecVCSQueryBound
	}
	return o.Buffer.Write(p)
}
