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
	type row struct {
		first, last, status, coverage string
		added, removed, managed       int
		unknown                       bool
	}
	var output strings.Builder
	var previous *row
	flush := func() {
		if previous == nil {
			return
		}
		id := previous.first
		if previous.last != id {
			id += ".." + previous.last
		}
		fmt.Fprintf(&output, "%s %s", id, previous.status)
		if previous.coverage != "" {
			fmt.Fprintf(&output, " %s", previous.coverage)
		}
		if previous.status != "pending" && previous.status != "retired" && (previous.added != 0 || previous.removed != 0 || previous.managed == 0) {
			fmt.Fprintf(&output, " +%d -%d", previous.added, previous.removed)
		}
		if previous.unknown {
			output.WriteString(" ?")
		}
		if previous.managed != 0 {
			fmt.Fprintf(&output, " managed:%d", previous.managed)
		}
		output.WriteByte('\n')
	}
	for _, id := range options.ids {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		next := row{first: id, last: id}
		change, found := index.Changes[id]
		switch {
		case !found:
			next.status, _ = missingChangeState(index, id)
		case len(change.Calls) == 0:
			next.status = "pending"
		default:
			var outcome *execOutcome
			for _, call := range change.Calls {
				record, found, err := s.read(options.workspace, call.ID, false)
				if err != nil {
					return "", err
				}
				if !found || record.History.ChangeID != id || record.History.CorrelationID != change.Correlation {
					return "", fmt.Errorf("change %s has a missing or inconsistent attempt", id)
				}
				history := record.History
				next.status = trackedStatus(history, call.Confirmed)
				if history.ExecOutcome != nil {
					outcome = history.ExecOutcome
					next.coverage = outcome.Coverage
				}
				for _, file := range history.ReviewFiles {
					if file.Origin != "" {
						next.managed++
						continue
					}
					added, removed := file.LineCounts()
					if added < 0 || file.Binary || file.Incomplete != "" {
						next.unknown = true
						continue
					}
					next.added += added
					next.removed += removed
				}
			}
			switch next.status {
			case "changes observed":
				next.status = "observed"
			case "no changes observed":
				next.status = "no changes"
			}
			if outcome != nil {
				switch outcome.Status {
				case execStatusCompleted:
					next.status = "completed"
					if len(outcome.Overlaps) != 0 {
						next.status += " shared"
					}
				case execStatusFailed:
					next.status = "failed"
				default:
					next.status = "observed"
				}
			}
			if change.RetiredCalls != 0 {
				next.status += " history:partial"
				next.unknown = true
			}
		}
		// Compress only complete, comparable rows. Partial IDs retain their
		// individual known counts so an agent can select the useful capture.
		if previous != nil && previous.status == next.status && previous.coverage == next.coverage &&
			!previous.unknown && !next.unknown && previous.managed == 0 && next.managed == 0 &&
			next.coverage != execCoveragePartial && next.coverage != execCoverageUnswept {
			previous.last = id
			previous.added += next.added
			previous.removed += next.removed
		} else {
			flush()
			previous = &next
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
