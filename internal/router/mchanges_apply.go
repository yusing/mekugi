package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

// changeCapture is one completed attempt's review files with absolute paths,
// in the store-wide order that also orders the saved diff view.
type changeCapture struct {
	id    string
	order uint64
	files []mekugi.ReviewFile
	// links are captured symlink paths; their review text is a link target.
	links map[string]bool
	// exec marks captures from observed shell commands.
	exec bool
}

// loadChangeCaptures reads completed attempts, all of them when ids is nil.
// Non-nil paths keep only review files connected to them through moves; other
// diffs are dropped while reading, so only what is kept is held and bounded.
// It runs under store.lock and never reads the workspace.
func (s *mekugiReplayStore) loadChangeCaptures(ctx context.Context, workspace string, index changeIndex, ids, paths []string) ([]changeCapture, error) {
	if ids == nil {
		ids = slices.Sorted(func(yield func(string) bool) {
			for id := range index.Changes {
				if !yield(id) {
					return
				}
			}
		})
	}
	canonical := func(path string) string {
		if path == "" {
			return ""
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		return filepath.Clean(path)
	}
	relevant := make(map[string]bool, len(paths))
	for _, path := range paths {
		relevant[path] = true
	}
	keeps := func(file mekugi.ReviewFile) bool {
		return paths == nil || relevant[file.BeforePath] || relevant[file.AfterPath]
	}
	bytes := 0
	retain := func(diff string) error {
		if bytes += len(diff); bytes > maxChangeReadBytes {
			return errors.New("change history exceeds 64 MiB")
		}
		return nil
	}
	read := func(id string, call string) (replayRecord, error) {
		record, found, err := s.read(workspace, call, false)
		if err != nil {
			return replayRecord{}, err
		}
		if !found || record.History.ChangeID != id || record.History.CorrelationID != index.Changes[id].Correlation {
			return replayRecord{}, fmt.Errorf("change %s has a missing or inconsistent attempt", id)
		}
		return record, nil
	}
	var captures []changeCapture
	var calls []string
	// dropped marks files whose diff was not retained, by capture and file.
	dropped := make(map[[2]int]bool)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		change, exists := index.Changes[id]
		if !exists {
			return nil, fmt.Errorf("change %s is missing in workspace %q; check --workspace; session data may have expired after 14 days of inactivity or been removed under storage pressure", id, workspace)
		}
		for _, call := range change.Calls {
			record, err := read(id, call.ID)
			if err != nil {
				return nil, err
			}
			capture := changeCapture{id: id, order: record.CaptureOrder, links: make(map[string]bool)}
			if observation := record.History.ExecObservation; observation != nil {
				capture.exec = true
				for _, file := range observation.Files {
					if file.Kind == execFileSymlink {
						capture.links[canonical(file.Path)] = true
					}
				}
			}
			for _, file := range record.History.ReviewFiles {
				file.BeforePath, file.AfterPath = canonical(file.BeforePath), canonical(file.AfterPath)
				if keeps(file) {
					if err := retain(file.Diff); err != nil {
						return nil, err
					}
				} else {
					file.Diff = ""
					dropped[[2]int{len(captures), len(capture.files)}] = true
				}
				capture.files = append(capture.files, file)
			}
			captures = append(captures, capture)
			calls = append(calls, call.ID)
		}
	}
	if paths != nil {
		// A move connects both of its names; repeat until no file joins.
		for grown := true; grown; {
			grown = false
			for _, capture := range captures {
				for _, file := range capture.files {
					if file.BeforePath != "" && file.AfterPath != "" && relevant[file.BeforePath] != relevant[file.AfterPath] {
						relevant[file.BeforePath], relevant[file.AfterPath], grown = true, true, true
					}
				}
			}
		}
		for i := range captures {
			var record *replayRecord
			for j, file := range captures[i].files {
				if !dropped[[2]int{i, j}] || !keeps(file) {
					continue
				}
				// A move made this file relevant after its diff was dropped.
				if record == nil {
					reread, err := read(captures[i].id, calls[i])
					if err != nil {
						return nil, err
					}
					if len(reread.History.ReviewFiles) != len(captures[i].files) {
						return nil, fmt.Errorf("change %s has a missing or inconsistent attempt", captures[i].id)
					}
					record = &reread
				}
				captures[i].files[j].Diff = record.History.ReviewFiles[j].Diff
				if err := retain(captures[i].files[j].Diff); err != nil {
					return nil, err
				}
			}
			captures[i].files = slices.DeleteFunc(captures[i].files, func(file mekugi.ReviewFile) bool { return !keeps(file) })
		}
	}
	slices.SortStableFunc(captures, func(a, b changeCapture) int { return cmp.Compare(a.order, b.order) })
	return captures, nil
}

// selectedChangeCaptures validates that every selected change has completed.
func (s *mekugiReplayStore) selectedChangeCaptures(ctx context.Context, options changeReadOptions, index changeIndex) ([]changeCapture, error) {
	for _, id := range options.ids {
		if change, exists := index.Changes[id]; exists && len(change.Calls) == 0 {
			return nil, fmt.Errorf("change %s is pending (no completed result)", id)
		}
	}
	return s.loadChangeCaptures(ctx, options.workspace, index, options.ids, nil)
}

// changeMutationPaths names every path mchanges revert or apply may write, so
// the router can capture them before the host runs the command.
func (s *mekugiReplayStore) changeMutationPaths(ctx context.Context, options changeReadOptions) ([]string, error) {
	s = s.scoped(ctx)
	var paths []string
	err := s.locked(ctx, func() error {
		index, err := s.readChangeIndex(options.workspace)
		if err != nil {
			return err
		}
		captures, err := s.selectedChangeCaptures(ctx, options, index)
		if err != nil {
			return err
		}
		for _, capture := range captures {
			for _, file := range capture.files {
				for _, path := range []string{file.BeforePath, file.AfterPath} {
					if path != "" && !slices.Contains(paths, path) {
						paths = append(paths, path)
					}
				}
			}
		}
		return nil
	})
	return paths, err
}

// mutationFile is one workspace path as read before the mutation, then as the
// mutation leaves it in memory until every selected change has been merged.
type mutationFile struct {
	path              string
	existed, exists   bool
	original, content string
	mode              os.FileMode
	// blocked stops later changes from touching a path they cannot merge.
	blocked string
	// markers counts conflict regions; kept is a deletion that met other content.
	markers  int
	kept     bool
	rejected []string
	notes    []string
	failed   bool
	// movedTo is the destination this file's content moved to; the source is
	// removed only after that destination is written.
	movedTo     *mutationFile
	writeFailed bool
	// dir marks a directory at the path; only a creation may replace it, and
	// only once it is empty.
	dir bool
}

func (f *mutationFile) conflicted() bool {
	return f.markers != 0 || f.kept
}

func (f *mutationFile) note(text string) {
	if !slices.Contains(f.notes, text) {
		f.notes = append(f.notes, text)
	}
}

func (f *mutationFile) skip(reason string) {
	f.note("skipped: " + reason)
	f.failed = true
}

type changeMutation struct {
	workspace string
	view      string
	files     map[string]*mutationFile
	order     []string
	effects   []mutationEffect
	// written counts files the workspace write changed.
	written int
	// exact means the inverse command with the same IDs undoes this one.
	exact bool
}

// mutationEffect is one merged review file's in-memory effect, with the content
// it started from so history composition can check that it continues.
type mutationEffect struct {
	review mekugi.ReviewFile
	before string
	files  []*mutationFile
}

func (m *changeMutation) file(path string) *mutationFile {
	if file, ok := m.files[path]; ok {
		return file
	}
	file := &mutationFile{path: path, mode: 0o644}
	m.files[path] = file
	m.order = append(m.order, path)
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		// A file where a parent directory belongs means the path is absent.
		return file
	case err == nil && info.IsDir():
		file.dir = true
		return file
	case err != nil:
		file.blocked = err.Error()
	case info.Mode()&os.ModeSymlink != 0:
		file.blocked = "symlink changes are not replayed"
	case !info.Mode().IsRegular():
		file.blocked = "not a regular file"
	case info.Size() > maxChangeReadBytes:
		file.blocked = "file exceeds 64 MiB"
	}
	if file.blocked != "" {
		return file
	}
	handle, err := openNativePatchFile(path)
	if err != nil {
		file.blocked = err.Error()
		return file
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, maxChangeReadBytes+1))
	switch {
	case err != nil:
		file.blocked = err.Error()
	case len(data) > maxChangeReadBytes:
		file.blocked = "file exceeds 64 MiB"
	case !utf8.Valid(data) || slices.Contains(data, 0):
		file.blocked = "binary content is not replayed"
	default:
		file.existed, file.exists = true, true
		file.original, file.content = string(data), string(data)
		file.mode = info.Mode().Perm()
	}
	return file
}

// legacyLinkReview recognizes an exec review recorded before ReviewFile.Link:
// a link side is a whole file of one "-> target" row. A text file of that
// shape is skipped too, which errs toward leaving the workspace alone.
func legacyLinkReview(review mekugi.ReviewFile) bool {
	hunks, err := review.Hunks()
	if err != nil || len(hunks) != 1 {
		return false
	}
	// Excluding added rows leaves the before side; excluding removed rows, the after side.
	for _, excluded := range []byte{'+', '-'} {
		var side []string
		for _, row := range hunks[0].Rows {
			if row.Kind != excluded {
				side = append(side, row.Text)
			}
		}
		if len(side) == 1 && strings.HasPrefix(side[0], "-> ") {
			return true
		}
	}
	return false
}

// merge replays one captured review file and records its in-memory effect.
// Reverse swaps the captured sides: the recorded result becomes the source.
func (m *changeMutation) merge(capture changeCapture, review mekugi.ReviewFile) {
	src, dst := review.BeforePath, review.AfterPath
	if m.view == "revert" {
		src, dst = dst, src
	}
	primary := m.file(cmp.Or(dst, src))
	reason := review.Incomplete
	switch {
	case review.Binary:
		reason = "binary content is not retained"
	case review.Link || capture.links[src] || capture.links[dst] || capture.exec && legacyLinkReview(review):
		reason = "symlink changes are not replayed"
	}
	if reason != "" {
		primary.skip(reason)
		m.exact = false
		return
	}
	current := primary
	if src != "" {
		current = m.file(src)
		if !current.exists && dst != "" && dst != src && primary.exists {
			// A move already in the requested state merges in place.
			current, src = primary, dst
			primary.note("move already " + m.done())
			m.exact = false
		}
	}
	for _, file := range []*mutationFile{current, primary} {
		if file.blocked != "" {
			file.skip(file.blocked)
			m.exact = false
			return
		}
	}
	if current.dir && src != "" {
		current.skip("not a regular file")
		m.exact = false
		return
	}
	switch {
	case src != "" && !current.exists && dst == "":
		current.note("already absent")
		m.exact = false
		return
	case src != "" && !current.exists:
		current.skip("file is missing")
		m.exact = false
		return
	case src != "" && src != dst && dst != "" && primary.exists:
		primary.skip("destination exists")
		m.exact = false
		return
	}
	merged, err := review.Merge(current.content, m.view == "revert", "mchanges "+m.view+" "+capture.id)
	if err != nil {
		primary.skip(err.Error())
		m.exact = false
		return
	}
	if merged.Satisfied != 0 || merged.Conflicts != 0 || len(merged.Rejected) != 0 {
		m.exact = false
	}
	if merged.Satisfied != 0 {
		primary.note(fmt.Sprintf("%d hunk%s already %s", merged.Satisfied, plural(merged.Satisfied), m.done()))
	}
	before, beforePath, afterPath := current.content, src, dst
	if src == "" && primary.exists {
		beforePath = dst // Created over existing content.
	}
	primary.markers += merged.Conflicts
	primary.rejected = append(primary.rejected, merged.Rejected...)
	switch {
	case dst == "" && merged.Content == "" && merged.Conflicts == 0 && len(merged.Rejected) == 0:
		current.exists, current.content = false, ""
	case dst == "":
		// Like git's modify/delete conflict, the file stays for resolution.
		current.content, current.kept, afterPath = merged.Content, true, src
		current.note("kept: content outside the recorded change remains")
		m.exact = false
	case src != "" && src != dst:
		primary.exists, primary.content, primary.mode = true, merged.Content, current.mode
		current.exists, current.content, current.movedTo = false, "", primary
	default:
		primary.exists, primary.content = true, merged.Content
	}
	for _, file := range []*mutationFile{current, primary} {
		if file.conflicted() || len(file.rejected) != 0 {
			file.blocked = "an earlier change did not merge cleanly"
		}
	}
	after := merged.Content
	if afterPath == "" {
		after = ""
	}
	m.effects = append(m.effects, mutationEffect{
		review: mekugi.RenderReviewFile(beforePath, afterPath, before, after),
		before: before, files: []*mutationFile{current, primary},
	})
}

func (m *changeMutation) done() string {
	if m.view == "revert" {
		return "reverted"
	}
	return "applied"
}

// write publishes the merged files: creations and updates before removals, so
// a move never loses its source before its destination exists. A write that
// needs a removal first, to empty a directory it replaces or to clear a file
// where its parent directory belongs, waits for the removals; so does the
// source of a move into such a write.
func (m *changeMutation) write() {
	var writes, removals, waiting, held []*mutationFile
	for _, path := range m.order {
		file := m.files[path]
		if file.exists == file.existed && file.content == file.original {
			continue
		}
		if file.exists {
			writes = append(writes, file)
		} else {
			removals = append(removals, file)
		}
	}
	pending := func(file *mutationFile) bool {
		if file.dir {
			return true
		}
		for _, removal := range removals {
			if strings.HasPrefix(file.path, removal.path+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	publish := func(file *mutationFile) {
		var err error
		switch {
		case !file.exists:
			if file.movedTo != nil && file.movedTo.writeFailed {
				file.note("kept: move destination was not written")
				file.writeFailed = true
				return
			}
			err = os.Remove(file.path)
		case file.dir:
			// Remove fails on a directory that still holds files.
			if err = os.Remove(file.path); err == nil {
				err = os.WriteFile(file.path, []byte(file.content), file.mode)
			}
		default:
			if err = os.MkdirAll(filepath.Dir(file.path), 0o755); err == nil {
				err = os.WriteFile(file.path, []byte(file.content), file.mode)
			}
		}
		if err != nil {
			file.note("write failed: " + err.Error())
			file.failed, file.writeFailed = true, true
			m.exact = false
			return
		}
		m.written++
	}
	for _, file := range writes {
		if pending(file) {
			waiting = append(waiting, file)
			continue
		}
		publish(file)
	}
	for _, file := range removals {
		if file.movedTo != nil && slices.Contains(waiting, file.movedTo) {
			held = append(held, file)
			continue
		}
		publish(file)
	}
	for _, file := range append(waiting, held...) {
		publish(file)
	}
}

// mutateChanges reverts or reapplies recorded changes in the workspace. The
// router observes this command like any declared writer, so the result is
// itself a change that can be reverted.
func (s *mekugiReplayStore) mutateChanges(ctx context.Context, options changeReadOptions) (string, int, error) {
	s = s.scoped(ctx)
	var selected, history []changeCapture
	var historyErr error
	err := s.locked(ctx, func() error {
		index, err := s.readChangeIndex(options.workspace)
		if err != nil {
			return err
		}
		names, err := s.changeDependencyNames(options.workspace, options.ids)
		if err != nil {
			return err
		}
		if err := s.retainFiles(names...); err != nil {
			return err
		}
		if selected, err = s.selectedChangeCaptures(ctx, options, index); err != nil {
			return err
		}
		paths := []string{}
		for _, capture := range selected {
			for _, file := range capture.files {
				paths = append(paths, file.BeforePath, file.AfterPath)
			}
		}
		// History only explains the report; an unreadable record degrades the
		// stat rather than blocking the requested mutation.
		history, historyErr = s.loadChangeCaptures(ctx, options.workspace, index, nil, slices.DeleteFunc(paths, func(path string) bool { return path == "" }))
		return ctx.Err()
	})
	if err != nil {
		return "", 1, err
	}
	mutation := &changeMutation{workspace: options.workspace, view: options.view, files: make(map[string]*mutationFile), exact: true}
	reverse := options.view == "revert"
	if reverse {
		slices.Reverse(selected)
	}
	matched := false
	for _, capture := range selected {
		for _, file := range capture.files {
			if len(options.paths) > 0 && !changePathMatches(options, file.BeforePath) && !changePathMatches(options, file.AfterPath) {
				continue
			}
			matched = true
			mutation.merge(capture, file)
		}
	}
	if !matched {
		return "", 1, errors.New("selected changes have no files to " + options.view + "; check paths after --")
	}
	mutation.write()
	return mutation.report(options, history, historyErr), mutation.status(), nil
}

func (m *changeMutation) status() int {
	for _, file := range m.files {
		if file.conflicted() || file.failed || len(file.rejected) != 0 {
			return 1
		}
	}
	return 0
}

// errUnobservedEdit marks a composed chain whose known lines disagree with
// the content a mutation started from.
var errUnobservedEdit = errors.New("file changed outside mchanges history")

// historyChain composes one file identity from recorded captures through the
// mutation's own effects, following moves, deletion, and re-creation.
type historyChain struct {
	composition mekugi.ReviewComposition
	err         error
	path        string
	own         int
}

// report states each touched file relative to the composed mchanges history,
// not to version control: clean means every recorded change to it is undone.
func (m *changeMutation) report(options changeReadOptions, history []changeCapture, historyErr error) string {
	chains := make(map[string]*historyChain)
	extend := func(file mekugi.ReviewFile, own int, before *string) {
		key := cmp.Or(file.BeforePath, file.AfterPath)
		chain := chains[key]
		if chain == nil {
			chain = &historyChain{path: key, err: historyErr}
		}
		// Captures hold only hunk context, so a hand edit can leave every
		// recorded hunk applicable while the composed stat no longer holds.
		if chain.err == nil && before != nil && !chain.composition.Consistent(*before) {
			chain.err = errUnobservedEdit
		}
		if chain.err == nil {
			chain.err = chain.composition.ApplyWithHighlight(file, false, false)
		}
		if own != 0 && chain.own == 0 {
			chain.own = own
		}
		delete(chains, key)
		if file.AfterPath != "" {
			chain.path = file.AfterPath
		}
		chains[chain.path] = chain
	}
	for _, capture := range history {
		for _, file := range capture.files {
			extend(file, 0, nil)
		}
	}
	for index, effect := range m.effects {
		extend(effect.review, index+1, &effect.before)
	}
	var touched []*historyChain
	for _, chain := range chains {
		if chain.own != 0 {
			touched = append(touched, chain)
		}
	}
	slices.SortFunc(touched, func(a, b *historyChain) int { return strings.Compare(a.path, b.path) })
	display := func(path string) string { return pathdisplay.ForWorkspace(m.workspace, path) }
	var output strings.Builder
	conflicted, reported := 0, make(map[string]bool)
	line := func(code, path, stat string, file *mutationFile) {
		notes := []string{stat}
		if file != nil {
			reported[file.path] = true
			if file.conflicted() {
				code = "UU"
				conflicted++
			}
			if file.markers != 0 {
				notes = append(notes, fmt.Sprintf("%d conflict%s", file.markers, plural(file.markers)))
			}
			if len(file.rejected) != 0 {
				notes = append(notes, fmt.Sprintf("%d hunk%s not found", len(file.rejected), plural(len(file.rejected))))
			}
			notes = append(notes, file.notes...)
		}
		fmt.Fprintf(&output, "%s %s %s\n", code, path, strings.Join(notes, " · "))
		if file != nil {
			for _, hunk := range file.rejected {
				output.WriteString(hunk)
			}
		}
	}
	for _, chain := range touched {
		file := m.files[chain.path]
		code, stat := "  ", "clean"
		path := display(chain.path)
		net := chain.composition.FilesWithHighlights()
		switch {
		case file != nil && file.writeFailed:
			code, stat = "??", "stat unavailable"
		case chain.err != nil && chain.err == historyErr:
			code, stat = "??", "stat unavailable: mchanges history could not be read"
		case chain.err != nil:
			// Captures hold only hunk context, so an edit made outside recorded
			// history cannot be composed with it.
			code, stat = "??", "stat unavailable: file changed outside mchanges history"
		case len(net) != 0:
			added, removed := 0, 0
			for _, region := range net {
				a, r := region.LineCounts()
				added, removed = added+a, removed+r
			}
			stat = fmt.Sprintf("+%d -%d", added, removed)
			switch before, after := net[0].BeforePath, net[0].AfterPath; {
			case before == "":
				code = " A"
			case after == "":
				code = " D"
			case before != after:
				code, path = " R", display(before)+" -> "+path
			default:
				code = " M"
			}
		}
		line(code, path, stat, file)
	}
	for _, path := range m.order {
		if file := m.files[path]; !reported[path] && len(file.notes) != 0 {
			line("  ", display(path), "unchanged", file)
		}
	}
	// The summary leads so that paging cannot hide it.
	inverse := "revert"
	if options.view == "revert" {
		inverse = "apply"
	}
	var summary string
	switch {
	case conflicted != 0:
		summary = fmt.Sprintf("conflicts in %d file%s: resolve them, or undo by reverting this command's change (newest in mchanges --list)\n", conflicted, plural(conflicted))
	case m.written == 0:
		summary = "nothing changed\n"
	case m.exact:
		undo := append([]string{"mchanges", inverse}, options.ids...)
		if len(options.paths) != 0 {
			undo = append(undo, "--")
			for _, path := range options.paths {
				undo = append(undo, shellQuoteArgument(path))
			}
		}
		summary = "undo: " + strings.Join(undo, " ") + "\n"
	default:
		summary = "undo: revert this command's change (newest in mchanges --list)\n"
	}
	return summary + output.String()
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
