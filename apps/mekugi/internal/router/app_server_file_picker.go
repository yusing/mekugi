package router

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type pickerScanResult struct {
	id      uint64
	target  composerTarget
	cwd     string
	choices []composerChoice
	problem string
}

// Codex 0.157.1's fuzzyFileSearch always honors ignore files. @! explicitly
// opts into this read-only native-client search instead; it never invokes a
// model tool or changes ignore configuration. Only the thread's absolute cwd
// supplies filesystem scope, never the router process directory.
func (u *appServerUI) searchExcludedFiles(target composerTarget, cwd string) {
	p := &u.picker
	if p.scanCancel != nil && p.scanTarget == target && p.scanCwd == cwd {
		return
	}
	u.cancelPickerScan()
	if p.scanResults == nil {
		p.scanResults = make(chan pickerScanResult, 1)
	}
	parent := u.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	p.scanCancel, p.scanTarget, p.scanCwd = cancel, target, cwd
	p.scanID++
	id, results := p.scanID, p.scanResults
	p.loading, p.choices, p.problem = true, nil, ""
	go func() {
		// Do not restart a filesystem walk for each byte in a burst of typing.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		choices, problem := scanPickerFiles(ctx, cwd, strings.TrimPrefix(target.query, "!"))
		result := pickerScanResult{id, target, cwd, choices, problem}
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}()
}

func (u *appServerUI) cancelPickerScan() {
	if u.picker.scanCancel != nil {
		u.picker.scanCancel()
		u.picker.scanCancel = nil
		u.picker.scanID++
	}
}

func scanPickerFiles(ctx context.Context, cwd, query string) ([]composerChoice, string) {
	if !filepath.IsAbs(cwd) {
		return nil, "File search requires an absolute thread workspace"
	}
	type match struct {
		choice composerChoice
		score  int
	}
	var best []match
	unreadable := 0
	less := func(a, b match) int {
		if a.score != b.score {
			return a.score - b.score
		}
		return strings.Compare(a.choice.path, b.choice.path)
	}
	err := filepath.WalkDir(cwd, func(path string, entry fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			if path == cwd {
				return err
			}
			unreadable++
			return nil
		}
		if path == cwd {
			return nil
		}
		if pickerVCSPath(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(cwd, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		score, ok := pickerMatchScore(relative, query)
		if !ok {
			return nil
		}
		candidate := match{composerChoice{name: relative, path: relative, directory: entry.IsDir()}, score}
		index, _ := slices.BinarySearchFunc(best, candidate, less)
		if index < 50 {
			best = slices.Insert(best, index, candidate)
			if len(best) > 50 {
				best = best[:50]
			}
		}
		return nil
	})
	if ctx.Err() != nil {
		return nil, ""
	}
	if err != nil {
		return nil, "File search failed: " + err.Error()
	}
	choices := make([]composerChoice, 0, len(best))
	for _, match := range best {
		choices = append(choices, match.choice)
	}
	problem := ""
	if unreadable > 0 {
		problem = fmt.Sprintf("%d paths could not be read", unreadable)
	}
	return choices, problem
}

func (u *appServerUI) applyPickerScan(result pickerScanResult) {
	p := &u.picker
	if result.id != p.scanID || result.cwd != u.session.cwd || result.target != u.completionTarget() || !p.open {
		return
	}
	if p.scanCancel != nil {
		p.scanCancel()
		p.scanCancel = nil
	}
	p.choices, p.problem, p.loading = result.choices, result.problem, false
	p.resolved, p.resolvedCwd = result.target, result.cwd
	p.selected = min(p.selected, max(0, len(p.choices)-1))
	u.dirty = true
}

// Ignore-rule bypass never exposes version-control implementation metadata.
func pickerVCSPath(path string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		switch part {
		case ".git", ".svn", ".hg", ".bzr", "_darcs", "CVS":
			return true
		}
	}
	return false
}
