package router

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/yusing/mekugi/internal/shellruntime"
)

// A session acquires its script directory only by exclusive creation. Its live
// root, never a historical inode snapshot or a replacement pathname, authorizes
// recursive cleanup. The proxy owns one shared parent capability for all threads.
type shellSession struct {
	parent        *os.Root
	scripts       *os.Root
	name          string
	runtimeName   string
	runtimeTarget string
	commentary    *ownedShellCommentary
	timers        map[string]*time.Timer // nil timer means expired, awaiting leases
	leases        int
}

var errShellStateUnavailable = errors.New("shell continuation storage is unavailable")

func (s *shellSession) createStorage() error {
	if s.scripts != nil {
		return nil
	}
	// An unexpected existing directory is not ours, even if an old inode number
	// has been recycled. Do not open it for either application or cleanup.
	if err := s.parent.Mkdir(s.name, 0o700); err != nil {
		return err
	}
	root, err := openExistingShellDirectory(s.parent, s.name)
	if err != nil {
		// Opening can fail transiently after Mkdir (for example, EMFILE). Only
		// roll back an empty directory entry, without traversing replacements.
		info, cleanupErr := s.parent.Lstat(s.name)
		if cleanupErr == nil && info.IsDir() {
			cleanupErr = s.parent.Remove(s.name)
		}
		if errors.Is(cleanupErr, os.ErrNotExist) {
			cleanupErr = nil
		}
		return errors.Join(err, cleanupErr)
	}
	s.scripts = root
	return nil
}

func (s *shellSession) retireStorage() error {
	if s.scripts == nil {
		return nil
	}
	var cleanupErr error
	entries, err := fs.ReadDir(s.scripts.FS(), ".")
	cleanupErr = errors.Join(cleanupErr, err)
	for _, entry := range entries {
		cleanupErr = errors.Join(cleanupErr, s.scripts.RemoveAll(entry.Name()))
	}
	cleanupErr = errors.Join(cleanupErr, removeShellDirectory(s.parent, s.name, s.scripts), s.scripts.Close())
	s.scripts = nil
	return cleanupErr
}

func (s *shellSession) retireIdle() {
	if s.leases != 0 {
		return
	}
	for name, timer := range s.timers {
		if timer == nil {
			_ = s.scripts.Remove(name)
			_ = s.scripts.Remove(name + ".lock")
			delete(s.timers, name)
		}
	}
	if len(s.timers) == 0 {
		_ = s.retireStorage()
	}
}

func (s *shellSession) close() error {
	for _, timer := range s.timers {
		if timer != nil {
			timer.Stop()
		}
	}
	cleanupErr := errors.Join(s.retireStorage(), s.closeCommentary())
	// A flat launcher is only unlinked, never traversed or recursively removed.
	// Matching the worker target preserves a newer router's locator and makes
	// missing or replaced script storage irrelevant to launcher cleanup.
	if target, err := s.parent.Readlink(s.runtimeName); err == nil && target == s.runtimeTarget {
		cleanupErr = errors.Join(cleanupErr, s.parent.Remove(s.runtimeName))
	}
	return cleanupErr
}

func removeShellDirectory(parent *os.Root, name string, owned *os.Root) error {
	identity, err := owned.Stat(".")
	if err != nil {
		return err
	}
	current, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(identity, current) {
		return nil
	}
	// The live owned root pins its inode through this comparison. Never recurse
	// through the name; even a final replacement with a nonempty directory survives.
	return parent.Remove(name)
}

func setShellRuntime(parent *os.Root, name, worker string) error {
	current, err := parent.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if current.Mode()&os.ModeSymlink == 0 {
			return errors.New("shell runtime locator is not a symbolic link")
		}
		if target, err := parent.Readlink(name); err == nil && target == worker {
			return nil
		}
		if err := parent.Remove(name); err != nil {
			return err
		}
	}
	return parent.Symlink(worker, name)
}

func (p *mekugiProxy) storeShellRuntime(threadID string) (string, error) {
	runtimePath, err := shellruntime.Path(p.shellDirectory, threadID)
	if err != nil {
		return "", err
	}
	directory, err := shellruntime.ScriptsPath(p.shellDirectory, threadID)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("mekugi proxy is closed")
	}
	if p.shellParent == nil {
		p.shellParent, err = os.OpenRoot(p.shellDirectory)
		if err != nil {
			return "", err
		}
	}
	runtimeName := filepath.Base(runtimePath)
	if err := setShellRuntime(p.shellParent, runtimeName, p.registry.shellRuntime); err != nil {
		return "", err
	}
	if session := p.shellSessions[directory]; session != nil {
		session.runtimeTarget = p.registry.shellRuntime
	} else {
		p.shellSessions[directory] = &shellSession{
			parent: p.shellParent, name: filepath.Base(directory),
			runtimeName: runtimeName, runtimeTarget: p.registry.shellRuntime,
			timers: make(map[string]*time.Timer),
		}
	}
	return directory, nil
}

func openExistingShellDirectory(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("shell storage %q is not a directory", name)
	}
	// Keep the selected name as an intermediate component: OpenRoot otherwise
	// opens its final component before checking its type, which can block on a
	// FIFO swapped in after Lstat. Traversing name/. requires a directory first.
	root, err := parent.OpenRoot(name + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("shell storage %q changed while opening", name)
	}
	return root, nil
}

func openRegularShellFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("shell continuation state is not a regular file")
	}
	// A file replaced by a FIFO after Lstat must not block before Stat can reject
	// the opened descriptor. Regular-file reads ignore O_NONBLOCK.
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("shell continuation state is not a regular file")
	}
	return file, nil
}

// shellRoot leases private continuation state while it is read or updated.
// Expiry and shutdown cannot remove its files or close its roots until release.
func (p *mekugiProxy) shellRoot(directory string) (*os.Root, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, errors.New("mekugi proxy is closed")
	}
	session, ok := p.shellSessions[directory]
	if !ok {
		return nil, nil, errShellStateUnavailable
	}
	if session.scripts == nil {
		return nil, nil, errShellStateUnavailable
	}
	session.leases++
	p.shellLeases.Add(1)
	release := sync.OnceFunc(func() {
		defer p.shellLeases.Done()
		p.mu.Lock()
		defer p.mu.Unlock()
		session.leases--
		if !p.closed {
			session.retireIdle()
		}
	})
	return session.scripts, release, nil
}

// storeShellState retains private continuation state until its original deadline.
func (p *mekugiProxy) storeShellState(directory, name, state string) bool {
	if shellruntime.ValidateID(name) != nil || name == ".runtime" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.shellSessions[directory]
	if p.closed || !ok {
		return false
	}
	if err := session.createStorage(); err != nil {
		return false
	}
	defer session.retireIdle()
	if _, pendingExpiry := session.timers[name]; pendingExpiry {
		return false
	}
	// Exclusive creation rejects duplicates and preexisting symlinks.
	file, err := session.scripts.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	_, writeErr := io.WriteString(file, state)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		_ = session.scripts.Remove(name)
		return false
	}
	session.timers[name] = time.AfterFunc(shellArtifactTTL, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.closed {
			session.timers[name] = nil
			session.retireIdle()
		}
	})
	return true
}
