package router

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

type pickerScanResult struct {
	id      uint64
	target  completion.Target
	cwd     string
	choices []completion.Choice
	problem string
}

// Codex 0.157.1's fuzzyFileSearch always honors ignore files. @! explicitly
// opts into this read-only native-client search instead; it never invokes a
// model tool or changes ignore configuration. Only the thread's absolute cwd
// supplies filesystem scope, never the router process directory.
func (u *appServerUI) searchExcludedFiles(target completion.Target, cwd string) {
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
	p.Loading, p.Choices, p.Problem = true, nil, ""
	go func() {
		// Do not restart a filesystem walk for each byte in a burst of typing.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		choices, problem := scanPickerFiles(ctx, cwd, strings.TrimPrefix(target.Query, "!"))
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

func scanPickerFiles(ctx context.Context, cwd, query string) ([]completion.Choice, string) {
	if !filepath.IsAbs(cwd) {
		return nil, "File search requires an absolute thread workspace"
	}
	type match struct {
		choice completion.Choice
		score  int
	}
	var best []match
	unreadable := 0
	less := func(a, b match) int {
		if a.score != b.score {
			return a.score - b.score
		}
		return strings.Compare(a.choice.Path, b.choice.Path)
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
		if composer.VCSPath(entry.Name()) {
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
		score, ok := completion.MatchScore(relative, query)
		if !ok {
			return nil
		}
		candidate := match{completion.Choice{Name: relative, Path: relative}, score}
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
	choices := make([]completion.Choice, 0, len(best))
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
	if result.id != p.scanID || result.cwd != u.session.cwd || result.target != u.completionTarget() || !p.Open {
		return
	}
	if p.scanCancel != nil {
		p.scanCancel()
		p.scanCancel = nil
	}
	p.Resolve(result.target, result.cwd, u.session.cwd, result.choices, result.problem)
	u.dirty = true
}
