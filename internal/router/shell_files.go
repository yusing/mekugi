package router

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/interp"
)

// shellFileTracker records only operations executed by this shell worker. It
// neither watches the workspace nor attempts to snapshot unrelated writers.
type shellFileTracker struct {
	manifest toolWorkerManifest
	sink     shellCommentarySink
	notices  io.Writer
	mu       sync.Mutex
	open     map[*shellFileWrite]bool
	err      error
}

func (t *shellFileTracker) record(ctx context.Context, input string, files []mekugi.ReviewFile) {
	if len(files) == 0 {
		return
	}
	// Notices bypass command redirections, pipes, and substitutions.
	t.mu.Lock()
	defer t.mu.Unlock()
	handler := interp.HandlerCtx(ctx)
	store, err := shellOutputStore(t.manifest)
	callID := "shell-file-" + rand.Text()
	var id string
	if err == nil {
		id, err = store.reserveChange(context.WithoutCancel(ctx), handler.Dir, handler.Env.Get("CODEX_THREAD_ID").String(), callID)
	}
	if err == nil {
		history := mekugiHistory{
			ToolName: "shell", Script: input, Root: handler.Dir,
			ExecutingThread: handler.Env.Get("CODEX_THREAD_ID").String(),
			ChangeID:        id, CorrelationID: callID, Attempt: 1,
			Applied: true, ReviewFiles: files, Report: changeNotice(id),
		}
		err = store.put(context.WithoutCancel(ctx), handler.Dir, map[string]mekugiHistory{callID: history})
	}
	if err != nil {
		t.err = errors.Join(t.err, fmt.Errorf("shell file operation completed, but change evidence could not be retained: %w", err))
		return
	}
	if publisher, ok := t.sink.(interface {
		PublishEdit(context.Context, string, string) error
	}); ok {
		_ = publisher.PublishEdit(context.WithoutCancel(ctx), handler.Dir, callID)
	}
	_, _ = io.WriteString(t.notices, changeNotice(id))
	for _, file := range files {
		if file.Incomplete != "" {
			fmt.Fprintf(t.notices, "incomplete history: %q -> %q: %s\n", file.BeforePath, file.AfterPath, file.Incomplete)
		}
	}
}

func shellFilePath(directory, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	// Do not clean symlink/.. components before the filesystem resolves them.
	return directory + string(os.PathSeparator) + path
}

func shellOperationReview(beforePath, afterPath, before, after string, captureErr error) mekugi.ReviewFile {
	if captureErr != nil {
		return mekugi.RenderIncompleteReviewFile(beforePath, afterPath, "file contents could not be read: "+captureErr.Error())
	}
	return mekugi.RenderReviewFile(beforePath, afterPath, before, after)
}

type shellFileWrite struct {
	file       *os.File
	owner      *shellFileTracker
	ctx        context.Context
	path       string
	before     string
	captureErr error
	exists     bool
	mu         sync.Mutex
	closed     bool
}

func (f *shellFileWrite) Read(p []byte) (int, error) { return f.file.Read(p) }

func (f *shellFileWrite) Write(p []byte) (int, error) { return f.file.Write(p) }

func (f *shellFileWrite) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	err := errors.Join(f.checkpoint(), f.file.Close())
	f.owner.mu.Lock()
	delete(f.owner.open, f)
	f.owner.mu.Unlock()
	return err
}

func (f *shellFileWrite) checkpoint() error {
	if f.path == "" {
		return nil // The owned removal unlinked this descriptor's path.
	}
	info, err := f.file.Stat()
	var after []byte
	if err == nil {
		after, err = io.ReadAll(io.NewSectionReader(f.file, 0, info.Size()))
	}
	captureErr := errors.Join(f.captureErr, err)
	if captureErr != nil || !f.exists || f.before != string(after) {
		beforePath := f.path
		if !f.exists {
			beforePath = ""
		}
		f.owner.record(f.ctx, "redirect "+f.path, []mekugi.ReviewFile{
			shellOperationReview(beforePath, f.path, f.before, string(after), captureErr),
		})
		f.before, f.exists, f.captureErr = string(after), true, err
	}
	return nil
}

// shellEntryPath resolves pathname traversal without following the final link.
// Review consumers may clean paths lexically, so retained identities must already
// reflect symlink/.. traversal rather than retain the original operand spelling.
func shellEntryPath(path string) (string, error) {
	path = strings.TrimRight(path, string(os.PathSeparator))
	parent, base := filepath.Split(path)
	parent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, base), nil
}

// flushPath orders pending descriptor writes before an owned rename or unlink.
// The returned callback updates only descriptors under that entry after success.
func (t *shellFileTracker) flushPath(path string) (func(string), error) {
	path, err := shellEntryPath(path)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	var affected []*shellFileWrite
	for file := range t.open {
		affected = append(affected, file)
	}
	t.mu.Unlock()
	var matched []*shellFileWrite
	for _, file := range affected {
		file.mu.Lock()
		if file.path == path || strings.HasPrefix(file.path, path+"/") {
			err = file.checkpoint()
			matched = append(matched, file)
		}
		file.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return func(target string) {
		for _, file := range matched {
			file.mu.Lock()
			if target == "" {
				file.path = ""
			} else {
				file.path = target + strings.TrimPrefix(file.path, path)
			}
			file.mu.Unlock()
		}
	}, nil
}

// openWrite owns capture and truncation on the same descriptor; it never reads
// a path and then delegates the destructive open to an external cat process.
// Retaining the inode preserves hard links and existing open handles.
func (t *shellFileTracker) openWrite(ctx context.Context, path string, flags int, mode os.FileMode) (io.ReadWriteCloser, error) {
	if flags&(os.O_TRUNC|os.O_APPEND) == 0 {
		return interp.DefaultOpenHandler()(ctx, path, flags, mode)
	}
	path = shellFilePath(interp.HandlerCtx(ctx).Dir, path)
	info, statErr := os.Stat(path)
	if statErr == nil && !info.Mode().IsRegular() {
		return interp.DefaultOpenHandler()(ctx, path, flags, mode)
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	file, err := os.OpenFile(path, flags&^(os.O_WRONLY|os.O_TRUNC)|os.O_RDWR, mode)
	var captureErr error
	if err != nil && statErr == nil && flags&os.O_WRONLY != 0 {
		captureErr = err
		file, err = os.OpenFile(path, flags&^os.O_TRUNC, mode)
	}
	if err != nil {
		return nil, err
	}
	var before []byte
	if captureErr == nil {
		before, captureErr = io.ReadAll(file)
	}
	_, err = file.Seek(0, io.SeekStart)
	if err == nil && flags&os.O_TRUNC != 0 {
		err = file.Truncate(0)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	write := &shellFileWrite{file: file, owner: t, ctx: ctx, path: path, before: string(before), exists: statErr == nil, captureErr: captureErr}
	t.mu.Lock()
	if t.open == nil {
		t.open = make(map[*shellFileWrite]bool)
	}
	t.open[write] = true
	t.mu.Unlock()
	return write, nil
}

func (t *shellFileTracker) detachUnlinkedFiles() {
	t.mu.Lock()
	var open []*shellFileWrite
	for file := range t.open {
		open = append(open, file)
	}
	t.mu.Unlock()
	for _, file := range open {
		file.mu.Lock()
		current, pathErr := os.Stat(file.path)
		opened, fileErr := file.file.Stat()
		if errors.Is(pathErr, os.ErrNotExist) || pathErr == nil && fileErr == nil && !os.SameFile(current, opened) {
			file.path = ""
		}
		file.mu.Unlock()
	}
}

func (t *shellFileTracker) finish() error {
	// exec's persistent redirections are not closed by the interpreter.
	t.mu.Lock()
	open := make([]*shellFileWrite, 0, len(t.open))
	for file := range t.open {
		open = append(open, file)
	}
	t.mu.Unlock()
	for _, file := range open {
		_ = file.Close()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

type shellFileOptions struct {
	paths                        []string
	force, recursive, dir        bool
	noClobber, noTarget, verbose bool
	target                       string
}

func shellTrackedFileCommand(name string) bool {
	return name == "rm" || name == "mv" || name == "touch"
}

func parseShellFileOptions(command []string) (shellFileOptions, error) {
	var options shellFileOptions
	literal := false
	for i := 1; i < len(command); i++ {
		arg := command[i]
		if literal || arg == "-" || !strings.HasPrefix(arg, "-") {
			options.paths = append(options.paths, arg)
			continue
		}
		if arg == "--" {
			literal = true
			continue
		}
		switch {
		case arg == "--force":
			arg = "-f"
		case arg == "--recursive":
			arg = "-r"
		case arg == "--dir":
			arg = "-d"
		case arg == "--no-clobber":
			arg = "-n"
		case arg == "--no-target-directory":
			arg = "-T"
		case arg == "--verbose":
			arg = "-v"
		case command[0] == "mv" && strings.HasPrefix(arg, "--target-directory="):
			options.target = strings.TrimPrefix(arg, "--target-directory=")
			if options.target == "" {
				return options, errors.New("target directory must not be empty")
			}
			continue
		case command[0] == "mv" && (arg == "-t" || arg == "--target-directory"):
			i++
			if i == len(command) {
				return options, fmt.Errorf("%s needs a directory", arg)
			}
			options.target = command[i]
			if options.target == "" {
				return options, errors.New("target directory must not be empty")
			}
			continue
		}
		allowed := "fdRrv"
		if command[0] == "mv" {
			allowed = "fnTv"
		}
		for _, flag := range strings.TrimPrefix(arg, "-") {
			if !strings.ContainsRune(allowed, flag) {
				return options, fmt.Errorf("%s: unsupported tracked option %q", command[0], arg)
			}
			switch {
			case flag == 'f':
				options.force, options.noClobber = true, false
			case flag == 'r' || flag == 'R':
				options.recursive = true
			case flag == 'd':
				options.dir = true
			case flag == 'n':
				options.noClobber, options.force = true, false
			case flag == 'T':
				options.noTarget = true
			case flag == 'v':
				options.verbose = true
			default:
				return options, fmt.Errorf("%s: unsupported tracked option %q", command[0], arg)
			}
		}
	}
	if options.target != "" && options.noTarget {
		return options, errors.New("target-directory conflicts with no-target-directory")
	}
	return options, nil
}

func shellRemovedContent(path string, info os.FileInfo) (string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Readlink(path)
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	data, err := os.ReadFile(path)
	return string(data), err
}

func shellRemovalInfo(path string) (os.FileInfo, error) {
	trimmed := strings.TrimRight(path, string(os.PathSeparator))
	base := filepath.Base(trimmed)
	if base == "." || base == ".." {
		return nil, fmt.Errorf("refusing to remove dot or dotdot directory: %s", path)
	}
	info, err := os.Lstat(path)
	if err == nil && info.IsDir() {
		root, rootErr := os.Stat(string(os.PathSeparator))
		if rootErr != nil {
			return nil, rootErr
		}
		if os.SameFile(info, root) {
			return nil, fmt.Errorf("refusing to remove filesystem root: %s", path)
		}
	}
	return info, err
}

// remove owns both capture and unlink. Successful children remain recorded when
// a later removal fails, matching rm's partial-success behavior.
func (t *shellFileTracker) remove(ctx context.Context, path string, options shellFileOptions, files *[]mekugi.ReviewFile) error {
	info, err := shellRemovalInfo(path)
	if errors.Is(err, os.ErrNotExist) && options.force {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && options.recursive {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		var failures []error
		for _, entry := range entries {
			failures = append(failures, t.remove(ctx, path+"/"+entry.Name(), options, files))
		}
		if err := errors.Join(failures...); err != nil {
			return err
		}
		// GNU rm traverses a trailing-slash symlink operand's children,
		// but retains the operand link and its target directory.
		if trimmed := strings.TrimRight(path, "/"); trimmed != path {
			entry, err := os.Lstat(trimmed)
			if err != nil {
				return err
			}
			if entry.Mode()&os.ModeSymlink != 0 {
				return nil
			}
		}
	} else if info.IsDir() && !options.dir {
		return fmt.Errorf("%s: is a directory", path)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reviewPath, err := shellEntryPath(path)
	if err != nil {
		return err
	}
	removed, err := t.flushPath(path)
	if err != nil {
		return err
	}
	content, captureErr := shellRemovedContent(path, info)
	if err := os.Remove(path); err != nil {
		return err
	}
	removed("")
	*files = append(*files, shellOperationReview(reviewPath, "", content, "", captureErr))
	if options.verbose {
		fmt.Fprintf(interp.HandlerCtx(ctx).Stdout, "removed %q\n", path)
	}
	return nil
}

func (t *shellFileTracker) move(ctx context.Context, source, target string, options shellFileOptions, files *[]mekugi.ReviewFile) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	dest, err := os.Lstat(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	exists := err == nil
	err = nil
	if exists && (options.noClobber || os.SameFile(info, dest)) {
		return nil
	}
	sourceReview, err := shellEntryPath(source)
	if err != nil {
		return err
	}
	targetReview, err := shellEntryPath(target)
	if err != nil {
		return err
	}
	moved, err := t.flushPath(source)
	if err != nil {
		return err
	}
	replaced, err := t.flushPath(target)
	if err != nil {
		return err
	}
	var old string
	var captureErr error
	if exists {
		old, captureErr = shellRemovedContent(target, dest)
	}
	// Enumerate names, not contents. A directory move does not rewrite its files.
	var names []string
	if info.IsDir() {
		walkSource, resolveErr := filepath.EvalSymlinks(source)
		if resolveErr != nil {
			return resolveErr
		}
		err = filepath.WalkDir(walkSource, func(path string, entry os.DirEntry, err error) error {
			if err == nil {
				relative, relErr := filepath.Rel(walkSource, path)
				if relErr != nil {
					return relErr
				}
				if relative == "." {
					names = append(names, "")
				} else {
					names = append(names, "/"+relative)
				}
			}
			return err
		})
	} else {
		names = []string{""}
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return err
		}
		err = t.crossDeviceMove(ctx, source, target, sourceReview, targetReview, old, exists, names, files, captureErr)
		t.detachUnlinkedFiles()
		if err == nil && options.verbose {
			fmt.Fprintf(interp.HandlerCtx(ctx).Stdout, "renamed %q -> %q\n", source, target)
		}
		return err
	}
	replaced("")
	resolvedTarget, resolveErr := filepath.EvalSymlinks(target)
	if resolveErr != nil {
		resolvedTarget = target
	}
	moved(resolvedTarget)
	if exists {
		*files = append(*files, shellOperationReview(targetReview, "", old, "", captureErr))
	}
	for _, name := range names {
		*files = append(*files, mekugi.RenderReviewFile(sourceReview+name, targetReview+name, "", ""))
	}
	if options.verbose {
		fmt.Fprintf(interp.HandlerCtx(ctx).Stdout, "renamed %q -> %q\n", source, target)
	}
	return nil
}

func (t *shellFileTracker) command(ctx context.Context, command []string, next interp.ExecHandlerFunc) error {
	if !shellTrackedFileCommand(command[0]) {
		return next(ctx, command)
	}
	handler := interp.HandlerCtx(ctx)
	var files []mekugi.ReviewFile
	defer func() { t.record(ctx, strings.Join(command, " "), files) }()
	if command[0] == "touch" {
		// Let touch own its timestamp/option semantics. Only successful creation
		// needs a content diff; an existing inode's timestamp is not a text edit.
		var missing []string
		literal := false
		for _, arg := range command[1:] {
			if arg == "--" && !literal {
				literal = true
				continue
			}
			if literal || !strings.HasPrefix(arg, "-") {
				path := shellFilePath(handler.Dir, arg)
				if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
					missing = append(missing, path)
				}
			}
		}
		err := next(ctx, command)
		for _, path := range missing {
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() && info.Size() == 0 {
				files = append(files, mekugi.RenderReviewFile("", path, "", ""))
			}
		}
		return err
	}
	options, err := parseShellFileOptions(command)
	fail := func(err error) error {
		fmt.Fprintf(handler.Stderr, "%s: %v\n", command[0], err)
		return interp.ExitStatus(1)
	}
	if err != nil {
		return fail(err)
	}
	if command[0] == "rm" {
		if len(options.paths) == 0 && !options.force {
			return fail(errors.New("missing operand"))
		}
		var failures []error
		for _, path := range options.paths {
			failures = append(failures, t.remove(ctx, shellFilePath(handler.Dir, path), options, &files))
		}
		if err := errors.Join(failures...); err != nil {
			return fail(err)
		}
		return nil
	}
	explicitTarget := options.target != ""
	if options.target == "" {
		if len(options.paths) < 2 {
			return fail(errors.New("missing destination"))
		}
		options.target = options.paths[len(options.paths)-1]
		options.paths = options.paths[:len(options.paths)-1]
	}
	target := shellFilePath(handler.Dir, options.target)
	info, statErr := os.Stat(target)
	directory := statErr == nil && info.IsDir() && !options.noTarget
	if (explicitTarget || len(options.paths) > 1) && !directory {
		return fail(fmt.Errorf("target %s is not a directory", target))
	}
	if len(options.paths) == 0 {
		return fail(errors.New("missing source"))
	}
	var failures []error
	for _, path := range options.paths {
		destination := target
		if directory {
			destination += "/" + filepath.Base(path)
		}
		failures = append(failures, t.move(ctx, shellFilePath(handler.Dir, path), destination, options, &files))
	}
	if err := errors.Join(failures...); err != nil {
		return fail(err)
	}
	return nil
}
