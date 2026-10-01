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
		added, removed                int
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
		output.WriteString(id)
		if previous.status == "pending" || previous.status == "retired" || previous.status == "unknown" {
			fmt.Fprintf(&output, " %s", previous.status)
		} else if strings.Contains(previous.status, "history:partial") {
			output.WriteString(" history:partial")
		}
		if previous.status != "pending" && previous.status != "retired" {
			fmt.Fprintf(&output, " +%d -%d", previous.added, previous.removed)
		}
		if previous.unknown {
			output.WriteString(" ?")
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
			hasFiles := false
			for _, call := range change.Calls {
				record, found, err := s.read(options.workspace, call.ID, false)
				if err != nil {
					return "", err
				}
				if !found || record.History.ChangeID != id || record.History.CorrelationID != change.Correlation {
					return "", fmt.Errorf("change %s has a missing or inconsistent attempt", id)
				}
				history := authoredChangeHistory(record.History)
				hasFiles = hasFiles || len(history.ReviewFiles) != 0
				if history.ExecOutcome != nil {
					next.coverage = history.ExecOutcome.Coverage
					next.unknown = next.unknown || next.coverage != "" && next.coverage != execCoverageExact
				}
				for _, file := range history.ReviewFiles {
					next.unknown = next.unknown || file.Incomplete != ""
					added, removed := file.LineCounts()
					if added < 0 || file.Binary || file.Incomplete != "" {
						next.unknown = true
						continue
					}
					next.added += added
					next.removed += removed
				}
			}
			if !hasFiles && !next.unknown && change.RetiredCalls == 0 {
				// Older routers allocated no-op IDs. Keep explicit reads valid,
				// but do not advertise them or compress a range across their gap.
				flush()
				previous = nil
				continue
			}
			if change.RetiredCalls != 0 {
				next.status += " history:partial"
				next.unknown = true
			}
		}
		// Compress only complete, comparable rows. Partial IDs retain their
		// individual known counts so an agent can select the useful capture.
		consecutive := false
		if previous != nil {
			previousStream, previousNumber, _ := parseChangeID(previous.last)
			stream, number, _ := parseChangeID(id)
			consecutive = stream == previousStream && number == previousNumber+1
		}
		if consecutive && previous.status == next.status && previous.coverage == next.coverage &&
			!previous.unknown && !next.unknown &&
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
		return fmt.Sprintf("no captures selected in workspace %q; own-thread selection excludes other agents, use explicit IDs\n", options.workspace), nil
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
	var failures []error
	matched := len(options.paths) == 0
	chains := make(map[string]*mekugi.ReviewComposition)
	var ordered []*mekugi.ReviewComposition
	for _, capture := range captures {
		for _, file := range capture.files {
			matched = matched || changePathMatches(options, file.BeforePath) || changePathMatches(options, file.AfterPath)
		}
		if capture.coverage != "" && capture.coverage != execCoverageExact {
			failures = append(failures, fmt.Errorf("change %s has partial captured effects; read without --net to inspect them", capture.id))
			continue
		}
		for _, file := range capture.files {
			if file.Binary {
				failures = append(failures, fmt.Errorf("change %s includes binary evidence that cannot be composed; read without --net to inspect it", capture.id))
				continue
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
				failures = append(failures, fmt.Errorf("change %s cannot be composed: %w; read without --net to inspect its captured diff", capture.id, err))
				continue
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
	if !matched && len(failures) == 0 {
		fmt.Fprintf(&output, "no captured files match paths after -- in workspace %q\n", options.workspace)
	} else if output.Len() == 0 && len(failures) == 0 {
		output.WriteString("no net changes in selected captured history\n")
	}
	if len(failures) > 0 {
		return output.String(), &partialChangeReadError{errors.Join(failures...)}
	}
	return output.String(), nil
}
