package presentation

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

// Source: apps/mekugi/internal/router/vcs_display.go:687:823,983:1504@[9822b997].
// VCSOutputRows reads a successful VCS command's output as change rows: a
// commit's recorded files, a diff's or stat's files and counts, or a
// status's paths. The output is the evidence, except that a git commit's
// per-file counts come from the commit object its output names, which
// CommitReader reads without blocking; the key names that object. Staging
// and checks, which print nothing on success, may precede a commit in one
// output; other lists interleave output, so they keep the plain tail, as do
// commands without a recognized row.
func VCSOutputRows(command, cwd, output string) ([]ChangeRow, CommitKey) {
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil, CommitKey{}
	}
	chain := vcsChain(program.Stmts[0])
	call, ok := vcsStatement(chain[len(chain)-1])
	if !ok {
		return nil, CommitKey{}
	}
	for _, statement := range chain[:len(chain)-1] {
		if prior, ok := vcsStatement(statement); !ok || call.kind != vcsCommit || prior.kind != vcsStage && prior.kind != vcsCheck {
			return nil, CommitKey{}
		}
	}
	lines := strings.Split(strings.ReplaceAll(textsanitize.StripSequences(output), "\r\n", "\n"), "\n")
	switch {
	case call.kind == vcsCommit && call.tool == "git":
		rows, key := gitCommitRows(call, cwd, lines)
		if call.repositoryUnknown {
			key = CommitKey{}
		}
		return rows, key
	case call.kind == vcsCommit:
		return svnCommitRows(lines), CommitKey{}
	case call.kind == vcsDiff:
		return patchRows(lines), CommitKey{}
	case call.kind == vcsStat && call.tool == "mchanges":
		return nil, CommitKey{} // mchangesOutputRows reads its summaries.
	case call.kind == vcsStat && call.format == "summarize":
		return svnSummaryRows(lines), CommitKey{}
	case call.kind == vcsStat:
		return gitStatRows(call.format, lines), CommitKey{}
	case call.kind == vcsStatus && call.tool == "git":
		return gitStatusRows(lines), CommitKey{}
	case call.kind == vcsStatus:
		return svnStatusRows(lines), CommitKey{}
	}
	return nil, CommitKey{}
}

var (
	gitCommitHead   = regexp.MustCompile(`^\[(.+) ([0-9a-f]{4,64})\] (.*)$`)
	gitChangedFiles = regexp.MustCompile(`^ (\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?$`)
	gitSummaryMode  = regexp.MustCompile(`^ (create|delete) mode \d+ (.+)$`)
	gitSummaryMove  = regexp.MustCompile(`^ (rename|copy) (.+) \(\d+%\)$`)
)

// gitCommitRows reads the commit a git commit's output names: its hash,
// branch, and totals, and the created, deleted, and moved files it lists,
// without counts. The key names the commit object whose files replace them
// once read; without a host cwd there is none.
func gitCommitRows(call vcsCall, cwd string, lines []string) ([]ChangeRow, CommitKey) {
	var head []string
	footer := ChangeRow{Footer: true}
	var fallback []ChangeRow
	for _, line := range lines {
		if match := gitCommitHead.FindStringSubmatch(line); match != nil && head == nil {
			head = match
			continue
		}
		if head == nil {
			continue // Hook output precedes the commit.
		}
		if match := gitChangedFiles.FindStringSubmatch(line); match != nil {
			footer.Note = match[1] + " files"
			if match[1] == "1" {
				footer.Note = "1 file"
			}
			footer.Added, _ = strconv.Atoi(match[2])
			footer.Removed, _ = strconv.Atoi(match[3])
		} else if row, ok := gitSummaryRow(line); ok {
			fallback = append(fallback, row)
		}
	}
	if head == nil {
		return nil, CommitKey{}
	}
	branch, _ := strings.CutSuffix(head[1], " (root-commit)")
	notes := []string{"on " + textsanitize.Safe(branch, false)}
	if call.subject == "" {
		notes = append([]string{textsanitize.Safe(head[3], false)}, notes...)
	}
	if footer.Note != "" {
		notes = append(notes, footer.Note)
	}
	footer.Label, footer.Note = head[2], strings.Join(notes, " · ")
	var key CommitKey
	if dir := vcsDirectory(cwd, call.dir); dir != "" {
		key = CommitKey{Directory: dir, Hash: head[2]}
	}
	return append(fallback, footer), key
}

// gitSummaryRow reads one --summary line: a created, deleted, or moved file.
func gitSummaryRow(line string) (ChangeRow, bool) {
	if match := gitSummaryMode.FindStringSubmatch(line); match != nil {
		path := vcsPath(match[2])
		status := diffmodel.Status{After: path}
		if match[1] == "delete" {
			status.Before, status.After = path, ""
		}
		return ChangeRow{Code: status.ShortCode(), Label: path}, true
	}
	if match := gitSummaryMove.FindStringSubmatch(line); match != nil {
		from, to := gitRenamePaths(match[2])
		from, to = vcsPath(from), vcsPath(to)
		status := diffmodel.Status{Before: from, After: to}
		row := ChangeRow{From: from, Label: to}
		if match[1] == "copy" {
			status.Before, row.From, row.Note = "", "", "copy of "+from
		}
		row.Code = status.ShortCode()
		return row, true
	}
	return ChangeRow{}, false
}

// vcsDirectory resolves a git -C directory against the command's own cwd.
// Without a host cwd there is no directory to read.
func vcsDirectory(cwd, dir string) string {
	switch {
	case filepath.IsAbs(dir):
		return dir
	case cwd == "":
		return ""
	}
	if dir == "" {
		return cwd
	}
	return cwd + string(filepath.Separator) + dir
}

var (
	svnCommitFile     = regexp.MustCompile(`^(Sending|Adding|Deleting|Replacing) +(?:\(bin\) +)?(.+)$`)
	svnCommitRevision = regexp.MustCompile(`^Committed revision (\d+)\.$`)
	svnCommitVerbs    = map[string]string{"Sending": "Edited", "Adding": "Created", "Deleting": "Deleted", "Replacing": "Edited"}
)

// svnCommitRows reads the files svn commit sent and the revision it made.
// svn reports no line counts.
func svnCommitRows(lines []string) []ChangeRow {
	var rows []ChangeRow
	revision := ""
	for _, line := range lines {
		if match := svnCommitFile.FindStringSubmatch(line); match != nil {
			row := ChangeRow{Verb: svnCommitVerbs[match[1]], Label: vcsPath(match[2])}
			if match[1] == "Replacing" {
				row.Note = "replaced"
			}
			rows = append(rows, row)
		} else if match := svnCommitRevision.FindStringSubmatch(line); match != nil {
			revision = match[1]
		}
	}
	if revision == "" {
		return nil // Nothing claims a commit.
	}
	return append(rows, ChangeRow{Footer: true, Label: "r" + revision})
}

// patchFile is one file of a unified diff.
type patchFile struct {
	from, to         string
	added, removed   int
	created, deleted bool
	copied           bool
	binary           bool
	git              bool // Paths carry a/ and b/ prefixes.
	old, hunks       bool // A --- header or hunk was read.
}

var patchHunk = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@`)

// patchRows reads a unified diff from git, svn, or mchanges as one row per
// file, counting lines within each hunk's stated extent so content that
// looks like a header is still counted as content.
func patchRows(lines []string) []ChangeRow {
	var files []*patchFile
	var file *patchFile
	start := func(git bool) {
		file = &patchFile{git: git}
		files = append(files, file)
	}
	oldLeft, newLeft, combined := 0, 0, false
	for _, line := range lines {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, "+"):
				file.added++
				newLeft--
				continue
			case strings.HasPrefix(line, "-"):
				file.removed++
				oldLeft--
				continue
			case strings.HasPrefix(line, " ") || line == "":
				oldLeft--
				newLeft--
				continue
			case strings.HasPrefix(line, `\`):
				continue
			}
			oldLeft, newLeft = 0, 0
		}
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "Index: ") {
			combined = false
		}
		switch {
		case combined:
		case strings.HasPrefix(line, "diff --git "):
			start(true)
			file.from, file.to = gitHeaderPaths(strings.TrimPrefix(line, "diff --git "))
		case strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined "):
			// A merge's combined diff has no per-parent counts; its
			// headers name no file of their own.
			file, combined = nil, true
		case strings.HasPrefix(line, "Index: "):
			start(false)
			file.from = vcsPath(strings.TrimPrefix(line, "Index: "))
			file.to = file.from
		case strings.HasPrefix(line, "--- "):
			if file == nil || file.old || file.hunks {
				start(false)
			}
			file.old = true
			file.from = patchPath(strings.TrimPrefix(line, "--- "), file.git)
			file.created = file.created || file.from == ""
		case file == nil:
		case strings.HasPrefix(line, "+++ "):
			file.to = patchPath(strings.TrimPrefix(line, "+++ "), file.git)
			file.deleted = file.deleted || file.to == ""
		case strings.HasPrefix(line, "new file mode"):
			file.created = true
		case strings.HasPrefix(line, "deleted file mode"):
			file.deleted = true
		case strings.HasPrefix(line, "copy from "):
			file.copied, file.from = true, vcsPath(strings.TrimPrefix(line, "copy from "))
		case strings.HasPrefix(line, "copy to "):
			file.copied, file.to = true, vcsPath(strings.TrimPrefix(line, "copy to "))
		case strings.HasPrefix(line, "rename from "):
			file.from = vcsPath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			file.to = vcsPath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch" || strings.HasPrefix(line, "Cannot display: file marked as a binary type"):
			file.binary = true
		default:
			if match := patchHunk.FindStringSubmatch(line); match != nil {
				file.hunks = true
				oldLeft, newLeft = patchExtent(match[1]), patchExtent(match[2])
			}
		}
	}
	var rows []ChangeRow
	index := make(map[string]int)
	for _, file := range files {
		status := diffmodel.Status{Before: file.from, After: file.to, Edited: file.added+file.removed > 0}
		row := ChangeRow{Label: file.to, Added: file.added, Removed: file.removed}
		switch {
		case file.copied:
			status.Before, row.Note = "", "copy of "+file.from
		case file.created && !file.deleted:
			status.Before = ""
		case file.deleted || file.to == "":
			status.After, row.Label = "", file.from
		case file.from != "" && file.from != file.to:
			row.From = file.from
		}
		row.Code = status.ShortCode()
		if file.binary {
			if row.Note != "" {
				row.Note += " · "
			}
			row.Note += "binary"
		}
		if row.Label == "" {
			continue
		}
		// mchanges reads each record's patch; one path's records sum.
		if i, seen := index[row.Label]; seen {
			rows[i].Added += row.Added
			rows[i].Removed += row.Removed
			continue
		}
		index[row.Label] = len(rows)
		rows = append(rows, row)
	}
	return vcsTotals(rows)
}

// vcsTotals closes more rows than a group shows with their totals.
func vcsTotals(rows []ChangeRow) []ChangeRow {
	if len(rows) <= ChangeRowsShown {
		return rows
	}
	added, removed := ChangeTotals(rows)
	return append(rows, ChangeRow{Footer: true, Note: fmt.Sprintf("%d files", len(rows)), Added: added, Removed: removed})
}

func patchExtent(count string) int {
	if count == "" {
		return 1
	}
	n, _ := strconv.Atoi(count)
	return n
}

// gitHeaderPaths reads the paths of a `diff --git a/X b/Y` header. An
// unquoted header is ambiguous unless both sides name one path; later
// headers supply what it cannot.
func gitHeaderPaths(header string) (from, to string) {
	if strings.HasPrefix(header, `"`) {
		quoted, err := strconv.QuotedPrefix(header)
		if err != nil {
			return "", ""
		}
		return patchPath(quoted, true), patchPath(strings.TrimPrefix(header[len(quoted):], " "), true)
	}
	if half := (len(header) - 1) / 2; len(header)%2 == 1 && header[half] == ' ' && strings.HasPrefix(header, "a/") &&
		strings.HasPrefix(header[half+1:], "b/") && header[2:half] == header[half+3:] {
		path := vcsPath(header[2:half])
		return path, path
	}
	return "", ""
}

// patchPath reads a --- or +++ path: /dev/null is an absent side, a tab ends
// the path before a timestamp or revision, and quoted paths unquote.
func patchPath(path string, git bool) string {
	if strings.HasPrefix(path, `"`) {
		if unquoted, err := strconv.Unquote(path); err == nil {
			path = unquoted
		}
	} else if before, after, found := strings.Cut(path, "\t"); found {
		// svn marks an absent side (nonexistent) after the path.
		if path = before; after == "(nonexistent)" {
			return ""
		}
	}
	if path == "/dev/null" {
		return ""
	}
	if git {
		if trimmed, ok := strings.CutPrefix(path, "a/"); ok {
			path = trimmed
		} else if trimmed, ok := strings.CutPrefix(path, "b/"); ok {
			path = trimmed
		}
	}
	return vcsPath(path)
}

// vcsPath is a displayed path: unquoted when git quoted it, and sanitized.
func vcsPath(path string) string {
	if strings.HasPrefix(path, `"`) {
		if unquoted, err := strconv.Unquote(path); err == nil {
			path = unquoted
		}
	}
	return textsanitize.Safe(path, false)
}

// gitRenamePaths expands git's compact rename forms, `old => new` and
// `dir/{old => new}/file`, into both paths.
func gitRenamePaths(path string) (from, to string) {
	if open := strings.Index(path, "{"); open >= 0 {
		if end := strings.Index(path[open:], "}"); end >= 0 {
			if before, after, found := strings.Cut(path[open+1:open+end], " => "); found {
				prefix, suffix := path[:open], path[open+end+1:]
				join := func(middle string) string {
					return strings.ReplaceAll(prefix+middle+suffix, "//", "/")
				}
				return join(before), join(after)
			}
		}
	}
	if before, after, found := strings.Cut(path, " => "); found {
		return before, after
	}
	return path, path
}

var (
	gitNumstatRow    = regexp.MustCompile(`^(\d+|-)\t(\d+|-)\t(.+)$`)
	gitStatRow       = regexp.MustCompile(`^ (.+?) +\| +(?:(\d+)(?: ([+-]+))?|(Bin.*))$`)
	gitNameStatusRow = regexp.MustCompile(`^([ACDMRTUXB])(\d*)\t([^\t]+)(?:\t(.+))?$`)
	gitShowHeader    = regexp.MustCompile(`^(?:commit [0-9a-f]{7,}|Merge: |Author: |AuthorDate: |Commit: |CommitDate: |Date: )`)
)

// gitStatRows reads git's file summaries: --stat, --numstat (with any
// --summary lines), --shortstat, --name-status, and --name-only, including
// git show's commit header before them. A --stat graph scaled to fit its
// width does not split a file's count exactly, so its rows show only the
// total; the closing totals are exact either way.
func gitStatRows(format string, lines []string) []ChangeRow {
	var rows []ChangeRow
	var footer *ChangeRow
	verbs := make(map[string]ChangeRow)
	scaled := false
	for _, line := range lines {
		if match := gitChangedFiles.FindStringSubmatch(line); match != nil && format != "numstat" {
			footer = &ChangeRow{Footer: true, Note: match[1] + " files"}
			if match[1] == "1" {
				footer.Note = "1 file"
			}
			footer.Added, _ = strconv.Atoi(match[2])
			footer.Removed, _ = strconv.Atoi(match[3])
			continue
		}
		if format == "numstat" || format == "stat" {
			if row, ok := gitSummaryRow(line); ok {
				verbs[row.Label] = row
				continue
			}
		}
		switch format {
		case "numstat":
			if match := gitNumstatRow.FindStringSubmatch(line); match != nil {
				from, to := gitRenamePaths(match[3])
				row := ChangeRow{Code: (diffmodel.Status{Before: from, After: to}).ShortCode(), Label: vcsPath(to)}
				if from != to {
					row.From = vcsPath(from)
				}
				if match[1] == "-" {
					row.Note = "binary"
				} else {
					row.Added, _ = strconv.Atoi(match[1])
					row.Removed, _ = strconv.Atoi(match[2])
				}
				rows = append(rows, row)
			}
		case "stat":
			match := gitStatRow.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			from, to := gitRenamePaths(match[1])
			row := ChangeRow{Code: (diffmodel.Status{Before: from, After: to}).ShortCode(), Label: vcsPath(to)}
			if from != to {
				row.From = vcsPath(from)
			}
			total, _ := strconv.Atoi(match[2])
			added, removed := strings.Count(match[3], "+"), strings.Count(match[3], "-")
			switch {
			case match[4] != "":
				row.Note = "binary"
			case added+removed != total:
				scaled = true
				row.Note = vcsLines(total)
			default:
				row.Added, row.Removed = added, removed
			}
			rows = append(rows, row)
		case "name-status":
			match := gitNameStatusRow.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			path := vcsPath(match[3])
			status := diffmodel.Status{Before: path, After: path, Conflict: match[1] == "U", Incomplete: match[1] == "X"}
			switch match[1] {
			case "A", "C":
				status.Before = ""
			case "D":
				status.After = ""
			}
			row := ChangeRow{Label: path}
			if match[4] != "" {
				row.Label = vcsPath(match[4])
				if match[1] == "R" {
					row.From = vcsPath(match[3])
					status.After = row.Label
				} else {
					row.Note = "copy of " + vcsPath(match[3])
				}
			}
			row.Code = status.ShortCode()
			rows = append(rows, row)
		case "name-only":
			if line != "" && !strings.HasPrefix(line, " ") && !gitShowHeader.MatchString(line) {
				rows = append(rows, ChangeRow{Label: vcsPath(line)})
			}
		}
	}
	if scaled {
		// One scaled row means the whole graph was scaled.
		for i := range rows {
			if rows[i].Note == "" && rows[i].Added+rows[i].Removed > 0 {
				rows[i].Note = vcsLines(rows[i].Added + rows[i].Removed)
				rows[i].Added, rows[i].Removed = 0, 0
			}
		}
	}
	for i, row := range rows {
		if summary, found := verbs[row.Label]; found {
			rows[i].Code, rows[i].From = summary.Code, summary.From
			if summary.Note != "" {
				rows[i].Note = summary.Note
				if row.Note != "" {
					rows[i].Note += " · " + row.Note
				}
			}
		}
	}
	if footer != nil {
		return append(rows, *footer)
	}
	return vcsTotals(rows)
}

func vcsLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

var (
	svnSummaryRow   = regexp.MustCompile(`^([ ADM])([ M]) +(.+)$`)
	svnSummaryVerbs = map[byte]string{'A': "Created", 'D': "Deleted", 'M': "Edited", ' ': "Edited"}
)

// svnSummaryRows reads svn diff --summarize: each path's item and property
// status, without line counts.
func svnSummaryRows(lines []string) []ChangeRow {
	var rows []ChangeRow
	for _, line := range lines {
		match := svnSummaryRow.FindStringSubmatch(line)
		if match == nil || match[1] == " " && match[2] == " " {
			continue
		}
		row := ChangeRow{Verb: svnSummaryVerbs[match[1][0]], Label: vcsPath(match[3])}
		if match[1] == " " {
			row.Note = "properties"
		}
		rows = append(rows, row)
	}
	return rows
}

var (
	gitStatusEntry    = regexp.MustCompile(`^\t(new file|modified|deleted|renamed|copied|typechange|both modified|both added|both deleted|added by us|added by them|deleted by us|deleted by them):\s+(.+?)(?: \((?:new commits|modified content|untracked content)(?:, [a-z ]+)*\))?$`)
	gitStatusLetters  = map[string]string{"new file": "A", "modified": "M", "deleted": "D", "renamed": "R", "copied": "C", "typechange": "T"}
	gitStatusMerge    = map[string]string{"both modified": "UU", "both added": "AA", "both deleted": "DD", "added by us": "AU", "added by them": "UA", "deleted by us": "DU", "deleted by them": "UD"}
	gitStatusSections = map[string]string{"Changes to be committed:": "staged", "Changes not staged for commit:": "unstaged",
		"Untracked files:": "untracked", "Unmerged paths:": "unmerged", "Ignored files:": "ignored"}
	gitStatusShort   = regexp.MustCompile(`^([ MTADRCU?!])([ MTADRCU?!]) (.+)$`)
	gitStatusHeader  = regexp.MustCompile(`^## (?:No commits yet on |Initial commit on )?(.+?)(?:\.\.\.\S+)?(?: \[(?:ahead (\d+))?(?:, )?(?:behind (\d+))?\])?$`)
	gitStatusAhead   = regexp.MustCompile(`^Your branch is ahead of .+ by (\d+) commits?\.$`)
	gitStatusBehind  = regexp.MustCompile(`^Your branch is behind .+ by (\d+) commits?, `)
	gitStatusDiverge = regexp.MustCompile(`^and have (\d+) and (\d+) different commits each`)
)

// gitStatusRows reads git status output, long or short, as two-cell status
// rows closed by the branch and its distance from upstream.
func gitStatusRows(lines []string) []ChangeRow {
	type entry struct{ x, y byte }
	var rows []ChangeRow
	codes := make(map[string]*entry)
	add := func(path string, x, y byte) {
		from, to, moved := strings.Cut(path, " -> ")
		if !moved {
			to = from
		}
		label := vcsPath(to)
		if code, seen := codes[label]; seen {
			code.x, code.y = max(code.x, x), max(code.y, y)
			return
		}
		codes[label] = &entry{x, y}
		row := ChangeRow{Label: label}
		if moved {
			row.From = vcsPath(from)
		}
		rows = append(rows, row)
	}
	branch, ahead, behind, clean, section, long := "", "", "", false, "", false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "On branch "):
			branch, long = strings.TrimPrefix(line, "On branch "), true
		case strings.HasPrefix(line, "HEAD detached "):
			branch, long = line, true
		case strings.HasPrefix(line, "## "):
			if match := gitStatusHeader.FindStringSubmatch(line); match != nil {
				branch, ahead, behind = match[1], match[2], match[3]
			}
		case strings.HasPrefix(line, "nothing to commit, working tree clean") || line == "nothing to commit (working directory clean)":
			clean = true
		case gitStatusSections[line] != "":
			section, long = gitStatusSections[line], true
		case line == "":
		case strings.HasPrefix(line, "\t") && section != "":
			match := gitStatusEntry.FindStringSubmatch(line)
			switch {
			case section == "untracked":
				add(strings.TrimPrefix(line, "\t"), '?', '?')
			case section == "ignored":
				add(strings.TrimPrefix(line, "\t"), '!', '!')
			case match == nil:
			case section == "unmerged" && gitStatusMerge[match[1]] != "":
				code := gitStatusMerge[match[1]]
				add(match[2], code[0], code[1])
			case section == "staged" && gitStatusLetters[match[1]] != "":
				add(match[2], gitStatusLetters[match[1]][0], ' ')
			case section == "unstaged" && gitStatusLetters[match[1]] != "":
				add(match[2], ' ', gitStatusLetters[match[1]][0])
			}
		default:
			if match := gitStatusAhead.FindStringSubmatch(line); match != nil {
				ahead = match[1]
			} else if match := gitStatusBehind.FindStringSubmatch(line); match != nil {
				behind = match[1]
			} else if match := gitStatusDiverge.FindStringSubmatch(line); match != nil {
				ahead, behind = match[1], match[2]
			} else if match := gitStatusShort.FindStringSubmatch(line); match != nil && !long {
				add(match[3], match[1][0], match[2][0])
			}
		}
	}
	for i := range rows {
		code := codes[rows[i].Label]
		rows[i].Code = string([]byte{code.x, code.y})
	}
	if branch == "" && !clean {
		return rows
	}
	var notes []string
	if ahead != "" && ahead != "0" {
		notes = append(notes, "↑"+ahead)
	}
	if behind != "" && behind != "0" {
		notes = append(notes, "↓"+behind)
	}
	note := strings.Join(notes, " ")
	if clean && len(rows) == 0 {
		note = strings.TrimSpace(note + " · clean")
		note = strings.TrimPrefix(note, "· ")
	}
	return append(rows, ChangeRow{Footer: true, Label: textsanitize.Safe(branch, false), Note: note})
}

var svnStatusRow = regexp.MustCompile(`^([ ACDIMRX?!~])([ CM])[ L][ +][ SX][ KOTB][ C] (.+)$`)

// svnStatusRows reads svn status: each path's item and property state.
func svnStatusRows(lines []string) []ChangeRow {
	var rows []ChangeRow
	for _, line := range lines {
		if match := svnStatusRow.FindStringSubmatch(line); match != nil && match[1]+match[2] != "  " {
			rows = append(rows, ChangeRow{Code: match[1] + match[2], Label: vcsPath(match[3])})
		}
	}
	return rows
}
