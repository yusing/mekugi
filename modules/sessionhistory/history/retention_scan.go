package history

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

// Source: internal/router/storage_retention.go@b8ca48c7 retention snapshot planning.
type storageCandidate struct {
	name    string
	thread  string
	used    time.Time
	files   map[string]bool
	legacy  bool
	pending []string
}

type storageSnapshot struct {
	sessions     []storageCandidate
	files        map[string]int64
	owners       map[string]int
	unknown      string
	dependencies map[string][]string
}

func (s *Store) storageSnapshotContext(ctx context.Context) (storageSnapshot, error) {
	snapshot := storageSnapshot{files: make(map[string]int64), owners: make(map[string]int), dependencies: make(map[string][]string)}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return snapshot, err
	}
	times := make(map[string]time.Time)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		name := entry.Name()
		_, registered, err := s.service.Kind(name)
		if err != nil {
			return snapshot, err
		}
		if !registered && !CatalogName(name) && !coordinationName(name) && !entry.IsDir() {
			if snapshot.unknown == "" || name < snapshot.unknown {
				snapshot.unknown = name
			}
		}
		if CatalogName(name) {
			session, err := s.ReadCatalog(name)
			if err != nil {
				return snapshot, err
			}
			snapshot.sessions = append(snapshot.sessions, storageCandidate{
				name: name, thread: session.Thread, used: session.LastUsed, files: session.Files,
			})
			for file := range session.Files {
				snapshot.owners[file]++
				if !s.registered(file) {
					if snapshot.unknown == "" || file < snapshot.unknown {
						snapshot.unknown = file
					}
				}
			}
		}
		if s.registered(name) || CatalogName(name) {
			info, err := entry.Info()
			if err != nil {
				return snapshot, err
			}
			if !info.Mode().IsRegular() {
				return snapshot, errors.New("session storage contains a non-regular managed record; cleanup refused")
			}
			snapshot.files[name] = info.Size()
			times[name] = info.ModTime()
		}
	}
	// An absent owner cannot disclose its historical dependency graph. Preserve
	// the entire potentially shared pool without opening its unregistered records.
	if snapshot.unknown != "" {
		return snapshot, nil
	}
	all := make(map[string]bool, len(snapshot.files))
	for name := range snapshot.files {
		all[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(all)) {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		dependencies, err := s.Dependencies(name)
		if err != nil {
			return snapshot, err
		}
		snapshot.dependencies[name] = dependencies
	}
	addDependencies := func(candidate *storageCandidate) {
		pending := slices.Collect(maps.Keys(candidate.files))
		for len(pending) > 0 {
			name := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			for _, dependency := range snapshot.dependencies[name] {
				if !candidate.files[dependency] {
					candidate.files[dependency] = true
					snapshot.owners[dependency]++
					pending = append(pending, dependency)
				}
			}
		}
	}
	for i := range snapshot.sessions {
		addDependencies(&snapshot.sessions[i])
	}
	// Persistent owner records pin their dependencies even without a catalog.
	for name := range all {
		kind, _, _ := s.service.Kind(name)
		if kind.Keep {
			addDependencies(&storageCandidate{files: map[string]bool{name: true}})
		}
	}
	order, err := dependencyOrder(all, snapshot.dependencies)
	if err != nil {
		return snapshot, err
	}
	// Legacy roots precede their dependencies even when their filenames do not.
	// A dependency gains each referring root's ownership before orphan discovery.
	for _, name := range order {
		kind, registered, _ := s.service.Kind(name)
		if snapshot.owners[name] == 0 && registered && !kind.Keep {
			snapshot.sessions = append(snapshot.sessions, storageCandidate{used: times[name], files: map[string]bool{name: true}, legacy: true})
			snapshot.owners[name]++
			addDependencies(&snapshot.sessions[len(snapshot.sessions)-1])
		}
	}
	slices.SortFunc(snapshot.sessions, func(a, b storageCandidate) int {
		if order := a.used.Compare(b.used); order != 0 {
			return order
		}
		return strings.Compare(a.name+a.thread+firstStorageFile(a.files), b.name+b.thread+firstStorageFile(b.files))
	})
	return snapshot, nil
}

func firstStorageFile(files map[string]bool) string {
	first := ""
	for file := range files {
		if first == "" || file < first {
			first = file
		}
	}
	return first
}

// dependencyOrder returns referrers before dependencies. A cycle leaves the
// complete view untouched rather than claiming an unsafe deletion order.
func dependencyOrder(files map[string]bool, dependencies map[string][]string) ([]string, error) {
	incoming := make(map[string]int, len(files))
	for name := range files {
		for _, dependency := range dependencies[name] {
			if files[dependency] {
				incoming[dependency]++
			}
		}
	}
	var ready []string
	for name := range files {
		if incoming[name] == 0 {
			ready = append(ready, name)
		}
	}
	order := make([]string, 0, len(files))
	for len(ready) > 0 {
		slices.Sort(ready)
		order = append(order, ready...)
		var next []string
		for _, name := range ready {
			for _, dependency := range dependencies[name] {
				if !files[dependency] {
					continue
				}
				incoming[dependency]--
				if incoming[dependency] == 0 {
					next = append(next, dependency)
				}
			}
		}
		ready = next
	}
	if len(order) != len(files) {
		return nil, errors.New("cyclic retained dependencies; cleanup refused")
	}
	return order, nil
}
