package diffmodel

import "strings"

// Source: internal/ui/diffview/navigation.go:306:362@[cc24b102] semantic file status

// Status is a file row's net change, coded like git's short status:
// ? (incomplete evidence), A, D, M, R (rename only), RM (rename with edits), or UU (the net diff still
// adds conflict markers that mchanges revert or apply left for resolution).
type Status struct {
	Before, After string
	// Edited reports captured content changes, not missing evidence.
	Edited, Conflict bool
	Incomplete       bool
	Directory        bool
}

// StatusOf classifies a file's diff regions, in capture order.
func StatusOf(regions ...ReviewFile) Status {
	var status Status
	if len(regions) > 0 {
		status.Before = regions[0].BeforePath
	}
	for _, region := range regions {
		status.Add(region)
	}
	return status
}

// add folds a later capture of the file into its status; before stays the
// first capture's.
func (s *Status) Add(region ReviewFile) {
	s.After = region.AfterPath
	s.Directory = region.Directory
	s.Incomplete = s.Incomplete || region.Incomplete != "" && !region.Directory
	added, removed := region.LineCounts()
	s.Edited = s.Edited || added > 0 || removed > 0 || region.Directory
	if region.Binary {
		if _, sides, found := strings.Cut(region.Diff, " differ ("); found {
			before, after, _ := strings.Cut(strings.TrimSuffix(strings.TrimSpace(sides), ")"), " -> ")
			s.Edited = s.Edited || before != after
		}
	}
	s.Conflict = s.Conflict || strings.Contains(region.Diff, "\n+>>>>>>> mchanges ")
}

func (s Status) ShortCode() string {
	switch {
	case s.Incomplete:
		return "?"
	case s.Conflict:
		return "UU"
	case s.Before == "":
		return "A"
	case s.After == "":
		return "D"
	case s.Before != s.After && s.Edited:
		return "RM"
	case s.Before != s.After:
		return "R"
	}
	return "M"
}
