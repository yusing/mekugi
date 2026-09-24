package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi"
)

func (s *mekugiReplayStore) renderChangeList(ctx context.Context, options changeReadOptions, index changeIndex) (string, error) {
	var output strings.Builder
	type group struct {
		first, last, status string
		added, removed      int
		unknown             bool
	}
	var current *group
	flush := func() {
		if current == nil {
			return
		}
		label := current.first
		if current.last != label {
			label += ".." + current.last
		}
		if current.unknown {
			fmt.Fprintf(&output, "%s %s - -\n", label, current.status)
		} else {
			fmt.Fprintf(&output, "%s %s +%d -%d\n", label, current.status, current.added, current.removed)
		}
	}
	for _, id := range options.ids {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		next := group{first: id, last: id}
		change, found := index.Changes[id]
		switch {
		case !found:
			next.status, _ = missingChangeState(index, id)
			next.unknown = true
		case len(change.Calls) == 0:
			next.status, next.unknown = "pending", true
		default:
			managed := true
			for _, call := range change.Calls {
				record, found, err := s.read(options.workspace, call.ID, false)
				if err != nil {
					return "", err
				}
				if !found || record.History.ChangeID != id || record.History.CorrelationID != change.Correlation {
					return "", fmt.Errorf("change %s has a missing or inconsistent attempt", id)
				}
				next.status = trackedStatus(record.History, call.Confirmed)
				managed = managed && call.Managed
				for _, file := range record.History.ReviewFiles {
					added, removed := file.LineCounts()
					if added < 0 || file.Binary || file.Incomplete != "" {
						next.unknown = true
					} else {
						next.added += added
						next.removed += removed
					}
				}
			}
			if managed {
				next.status += " managed only"
			}
			if change.RetiredCalls > 0 {
				next.status += " (partial history)"
				next.unknown = true
			}
		}
		if current != nil && current.status == next.status && current.unknown == next.unknown {
			current.last = id
			current.added += next.added
			current.removed += next.removed
		} else {
			flush()
			current = &next
		}
		if output.Len() > maxChangeReadBytes {
			return "", errors.New("change list exceeds 64 MiB")
		}
	}
	flush()
	return output.String(), nil
}

func (s *mekugiReplayStore) renderNetChanges(ctx context.Context, options changeReadOptions, index changeIndex) (string, error) {
	if len(options.ids) == 0 {
		return "no net changes in selected captured history\n", nil
	}
	for _, id := range options.ids {
		change, found := index.Changes[id]
		if !found {
			return "", missingChangeError(index, id)
		}
		if len(change.Calls) == 0 || change.RetiredCalls != 0 {
			return "", fmt.Errorf("change %s has pending or retired history; cannot produce a complete --net view", id)
		}
	}
	var paths []string
	for _, path := range options.paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(options.workspace, path)
		}
		paths = append(paths, filepath.Clean(path))
	}
	captures, err := s.loadChangeCaptures(ctx, options.workspace, index, options.ids, paths)
	if err != nil {
		return "", err
	}
	chains := make(map[string]*mekugi.ReviewComposition)
	var ordered []*mekugi.ReviewComposition
	for _, capture := range captures {
		if len(capture.files) != 0 && (!capture.applied || capture.coverage != "" && capture.coverage != execCoverageExact) {
			return "", fmt.Errorf("change %s has unconfirmed or partial captured effects; read without --net to inspect them", capture.id)
		}
		for _, file := range capture.files {
			if file.Binary {
				return "", fmt.Errorf("change %s includes binary evidence that cannot be composed; read without --net to inspect it", capture.id)
			}
			key := file.BeforePath
			if key == "" {
				key = file.AfterPath
			}
			chain := chains[key]
			if chain == nil {
				chain = new(mekugi.ReviewComposition)
				ordered = append(ordered, chain)
			}
			if err := chain.ApplyWithHighlight(file, false, false); err != nil {
				return "", fmt.Errorf("change %s cannot be composed: %w", capture.id, err)
			}
			delete(chains, key)
			if file.AfterPath != "" {
				key = file.AfterPath
			}
			chains[key] = chain
		}
	}
	var output strings.Builder
	for _, chain := range ordered {
		for _, file := range chain.FilesWithHighlights() {
			if len(options.paths) > 0 && !changePathMatches(options, file.BeforePath) && !changePathMatches(options, file.AfterPath) {
				continue
			}
			output.WriteString(file.UnifiedDiff())
			if output.Len() > maxChangeReadBytes {
				return "", errors.New("change read exceeds 64 MiB; narrow the range or paths after --")
			}
		}
	}
	if output.Len() == 0 {
		output.WriteString("no net changes in selected captured history\n")
	}
	return output.String(), nil
}
