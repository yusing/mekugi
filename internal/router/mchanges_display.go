package router

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"mvdan.cc/sh/v3/syntax"
)

// Rows of `mchanges --list` and `--summary`, as renderChangeList and
// renderChanges print them.
var (
	mchangesListRow       = regexp.MustCompile(`^(\S+?)((?: (?:pending|retired|unknown|history:partial))?)(?: \+(\d+) -(\d+))?( \?)?(?: managed:(\d+))?$`)
	mchangesStatusRow     = regexp.MustCompile(`^(\S+) (retired \(partial history\)|pending \(no completed result\)|retired|unknown|incomplete captured scope(?:; use --history for diagnostics)?)$`)
	mchangesManagedRow    = regexp.MustCompile(`^M \+(\d+) -(\d+)(?:; (\d+ counts unavailable))?$`)
	mchangesManagedGapRow = regexp.MustCompile(`^\? tool-managed: (.+); use --history for paths and full reasons$`)
	mchangesSummaryVerbs  = map[string]string{"A": "Created", "M": "Edited", "D": "Deleted", "R": "Moved", "RM": "Moved", "UU": "Conflict", "?": "?"}
)

const (
	mchangesManagedFiles   = "tool-managed files"
	mchangesManagedSummary = "tool-managed"
)

// mchangesOutputRows reads a successful `mchanges --list` or `--summary` as
// change rows. Other commands, and output without a recognized row, keep
// their plain tail; unrecognized lines stay as muted notes.
func mchangesOutputRows(item appServerItem) []activityui.ChangeRow {
	if item.AggregatedOutput == nil {
		return nil
	}
	source := appServerDisplayCommand(item.Command)
	program, err := syntax.NewParser().Parse(strings.NewReader(source), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil
	}
	argv, ok := toolActivityLiteralCall(program.Stmts[0])
	if !ok || len(argv) < 2 || filepath.Base(argv[0]) != "mchanges" {
		return nil
	}
	options := argv[1:]
	if end := slices.Index(options, "--"); end >= 0 {
		options = options[:end]
	}
	parse := mchangesSummaryRow
	switch {
	case slices.Contains(options, "--list"):
		parse = mchangesListOutputRow
	case !slices.Contains(options, "--summary"):
		return nil
	}
	var rows []activityui.ChangeRow
	recognized := false
	for line := range strings.Lines(*item.AggregatedOutput) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		row, ok := parse(line)
		if !ok {
			row = activityui.ChangeRow{Note: livediff.Safe(line, false)}
		}
		recognized = recognized || ok
		rows = append(rows, row)
	}
	if !recognized {
		return nil
	}
	return rows
}

// mchangesListOutputRow reads one `--list` row: an ID range, its status, and
// its direct and tool-managed counts.
func mchangesListOutputRow(line string) (activityui.ChangeRow, bool) {
	match := mchangesListRow.FindStringSubmatch(line)
	if match == nil {
		return activityui.ChangeRow{}, false
	}
	row := activityui.ChangeRow{Label: livediff.Safe(match[1], false)}
	row.Added, _ = strconv.Atoi(match[3])
	row.Removed, _ = strconv.Atoi(match[4])
	var notes []string
	if status := strings.TrimSpace(match[2]); status != "" {
		notes = append(notes, status)
	}
	if match[5] != "" {
		notes = append(notes, "?")
	}
	if match[6] != "" {
		notes = append(notes, "managed:"+match[6])
	}
	row.Note = strings.Join(notes, " · ")
	return row, true
}

// mchangesSummaryRow reads one `--summary` row: a file with its status and
// counts, the compact tool-managed rows, or an ID's status.
func mchangesSummaryRow(line string) (activityui.ChangeRow, bool) {
	if status, path, ok := strings.Cut(line, " "); ok && len(status) == 1 && strings.HasSuffix(path, "/") {
		if verb, found := mchangesSummaryVerbs[status]; found {
			label, rest, valid := mchangesSummaryPath(strings.TrimSuffix(path, "/"))
			if valid && rest == "" {
				return activityui.ChangeRow{Verb: verb, Label: label + "/"}, true
			}
		}
	}

	if match := mchangesManagedRow.FindStringSubmatch(line); match != nil {
		row := activityui.ChangeRow{Verb: "Edited", Label: mchangesManagedFiles, Note: match[3]}
		row.Added, _ = strconv.Atoi(match[1])
		row.Removed, _ = strconv.Atoi(match[2])
		return row, true
	}
	if match := mchangesManagedGapRow.FindStringSubmatch(line); match != nil {
		return activityui.ChangeRow{Verb: "?", Label: mchangesManagedFiles, Note: livediff.Safe(match[1], false)}, true
	}
	if match := mchangesStatusRow.FindStringSubmatch(line); match != nil {
		return activityui.ChangeRow{Label: livediff.Safe(match[1], false), Note: match[2]}, true
	}
	fields := strings.Split(line, "\t")
	verb, ok := mchangesSummaryVerbs[fields[0]]
	if !ok || len(fields) < 4 {
		return activityui.ChangeRow{}, false
	}
	row := activityui.ChangeRow{Verb: verb}
	var notes []string
	// Unknown counts show none; the row's status already marks it.
	if fields[1] != "-" || fields[2] != "-" {
		added, addErr := strconv.Atoi(fields[1])
		removed, removeErr := strconv.Atoi(fields[2])
		if addErr != nil || removeErr != nil {
			return activityui.ChangeRow{}, false
		}
		row.Added, row.Removed = added, removed
	}
	rest := fields[3:]
	if rest[0] == mchangesManagedSummary && len(rest) > 1 {
		notes = append(notes, mchangesManagedSummary)
		rest = rest[1:]
	}
	path, after, ok := mchangesSummaryPath(rest[0])
	if !ok {
		return activityui.ChangeRow{}, false
	}
	if moved, ok := strings.CutPrefix(after, " => "); ok {
		if row.Label, after, ok = mchangesSummaryPath(moved); !ok || after != "" {
			return activityui.ChangeRow{}, false
		}
		row.From = path
	} else if after != "" {
		return activityui.ChangeRow{}, false
	} else {
		row.Label = path
	}
	if len(rest) > 1 {
		reasons, err := strconv.Unquote(rest[1])
		if err != nil {
			reasons = rest[1]
		}
		notes = append(notes, livediff.Safe(reasons, false))
	}
	row.Note = strings.Join(notes, " · ")
	return row, true
}

// mchangesSummaryPath reads one displayed path, quoted when it holds
// characters the summary cannot show bare, and returns what follows it.
func mchangesSummaryPath(text string) (path, rest string, ok bool) {
	if strings.HasPrefix(text, `"`) {
		quoted, err := strconv.QuotedPrefix(text)
		if err != nil {
			return "", "", false
		}
		path, err = strconv.Unquote(quoted)
		return livediff.Safe(path, false), text[len(quoted):], err == nil
	}
	before, after, found := strings.Cut(text, " => ")
	if found {
		after = " => " + after
	}
	return livediff.Safe(before, false), after, before != ""
}
