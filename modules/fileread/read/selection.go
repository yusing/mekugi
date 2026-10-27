package read

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/outputbudget"
)

// Source: plugins/mcat.ts:166:310@[543de4f3] readLines
type selectionScan struct {
	selection               commandargs.ReadSelection
	shown, retained         strings.Builder
	tailRows                []string
	tailBytes, tailHead     int
	selected                int
	incomplete, unavailable bool
	reason                  string
}

func (s *selectionScan) wants(line int, o commandargs.ReaderOptions) bool {
	return (s.selection.Start == 0 || line >= s.selection.Start && line <= s.selection.End) &&
		!(s.unavailable && !o.Tail && s.incomplete)
}

func (s *selectionScan) accept(row string, o commandargs.ReaderOptions) {
	s.selected++
	if !s.unavailable {
		if s.retained.Len()+len(row) > retainedBytes {
			s.unavailable = true
			s.retained.Reset()
			s.reason = "result exceeds the 16 MiB recovery bound; narrow the source range\n"
		} else {
			s.retained.WriteString(row)
		}
	}
	if o.Tail {
		s.tailRows = append(s.tailRows, row)
		s.tailBytes += len(row)
		for (o.Lines > 0 && len(s.tailRows)-s.tailHead > o.Lines) || s.tailBytes > o.Budget*128 {
			s.tailBytes -= len(s.tailRows[s.tailHead])
			s.tailRows[s.tailHead] = ""
			s.tailHead++
			s.incomplete = true
		}
		if s.tailHead > 1024 {
			s.tailRows = append([]string(nil), s.tailRows[s.tailHead:]...)
			s.tailHead = 0
		}
	} else if !s.incomplete {
		if o.Lines > 0 && s.selected > o.Lines || s.shown.Len()+len(row) > o.Budget*128 {
			s.incomplete = true
		} else {
			s.shown.WriteString(row)
		}
	}
}

func (s *selectionScan) rejectOversized(line int, o commandargs.ReaderOptions) {
	s.unavailable = true
	s.retained.Reset()
	s.incomplete = true
	s.reason = fmt.Sprintf("row %d exceeds the %d-byte inspection bound; use mrun with a byte-oriented command such as head -c\n", line, commandargs.MaxOutputTokens*128)
	if o.Tail {
		s.tailRows = nil
		s.tailHead = 0
		s.tailBytes = 0
	}
}

func (s *selectionScan) output(o commandargs.ReaderOptions) (command.Output, error) {
	current := s.shown.String()
	if o.Tail {
		current = strings.Join(s.tailRows[s.tailHead:], "")
	}
	budget := outputbudget.Budget{}
	bounded, err := budget.SelectRows(current, o.Budget, o.Tail)
	if err != nil {
		return command.Output{}, err
	}
	s.incomplete = s.incomplete || len(bounded) < len(current)
	result := command.Output{Stdout: bounded}
	if s.incomplete {
		result.ExitCode = 1
		result.FailureClass = "output_limit"
		if s.reason != "" {
			result.Stderr = "mcat: " + s.reason
		}
		if !s.unavailable {
			all := s.retained.String()
			omitted := strings.TrimPrefix(all, bounded)
			if o.Tail {
				omitted = strings.TrimSuffix(all, bounded)
			}
			result.OmittedOutput = &command.OmittedOutput{Stdout: omitted, StdoutKind: "rows"}
		}
	}
	return result, nil
}
