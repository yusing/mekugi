package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// Watch the directories, not the index inodes: publishers replace indexes and
// scopes atomically. Notifications only invalidate the view; durable snapshots
// remain the authority. Register before reading so publication cannot fall in
// the gap between a snapshot and its subscription.
type liveDiffWatcher struct {
	*fsnotify.Watcher
	directory, sessionFile string
	targets                map[string]bool
	directories            map[string]bool
}

func newLiveDiffWatcher(directory, workspace, sessionFile string) (*liveDiffWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &liveDiffWatcher{
		Watcher: watcher, directory: filepath.Clean(directory), sessionFile: sessionFile,
		directories: make(map[string]bool),
	}
	if err := w.sync(map[string]changeIndex{workspace: {}}); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

func (w *liveDiffWatcher) sync(indexes map[string]changeIndex) error {
	targets := make(map[string]bool, len(indexes)+1)
	for workspace := range indexes {
		targets[filepath.Join(w.directory, changeIndexName(workspace))] = true
	}
	if w.sessionFile != "" {
		targets[filepath.Clean(w.sessionFile)] = true
	}
	directories := make(map[string]bool)
	watch := []string{w.directory}
	if w.sessionFile != "" {
		watch = append(watch, filepath.Dir(w.sessionFile))
	}
	for _, dir := range watch {
		// An independent viewer can start before its store exists. Observe the
		// nearest existing ancestor until the publisher creates the directory.
		for {
			err := w.Add(dir)
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) || filepath.Dir(dir) == dir {
				return err
			}
			dir = filepath.Dir(dir)
		}
		directories[dir] = true
	}
	for dir := range w.directories {
		if !directories[dir] {
			if err := w.Remove(dir); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
				return err
			}
		}
	}
	w.targets, w.directories = targets, directories
	return nil
}

func (w *liveDiffWatcher) relevant(event fsnotify.Event) bool {
	if !event.Has(fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename) {
		return false
	}
	if w.targets[event.Name] || w.directories[event.Name] {
		return true
	}
	for path := range w.targets {
		if strings.HasPrefix(path, event.Name+string(filepath.Separator)) {
			return true // A previously missing ancestor was created or removed.
		}
	}
	return false
}
