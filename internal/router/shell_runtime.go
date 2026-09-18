package router

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/internal/shellruntime"
)

// The proxy owns one shared parent capability for thread runtime locators and
// commentary descriptors. Cleanup only unlinks the entries this router owns.
type shellSession struct {
	parent        *os.Root
	runtimeName   string
	runtimeTarget string
	commentary    *ownedShellCommentary
}

func (s *shellSession) close() error {
	cleanupErr := s.closeCommentary()
	// A flat launcher is only unlinked, never traversed or recursively removed.
	// Matching the worker target preserves a newer router's locator and makes
	// missing or replaced script storage irrelevant to launcher cleanup.
	if target, err := s.parent.Readlink(s.runtimeName); err == nil && target == s.runtimeTarget {
		cleanupErr = errors.Join(cleanupErr, s.parent.Remove(s.runtimeName))
	}
	return cleanupErr
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
			parent:      p.shellParent,
			runtimeName: runtimeName, runtimeTarget: p.registry.shellRuntime,
		}
	}
	return directory, nil
}
