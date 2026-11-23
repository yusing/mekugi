package terminalview

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/diffrender"
	pathterminal "github.com/yusing/mekugi/libraries/pathdisplay/terminal"

	"github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"

	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// vcsRow lays out a VCS operation on one row where it fits: the verb, its
// heading cut short to leave room for the source, then any exit.
// In compact default view, successful VCS rows inline their shortstat/status
// summary on a single truncated line instead of expanding change rows below.
func (p *Painter) vcsRow(block Block, width int) []string {
	verb, color := block.RowVerb(), block.verbColor(p.Theme)
	lead := block.lead(p.Theme, color, verb)
	head, source, _ := presentation.VCSSource(block.Label)
	summary := ""
	if block.compactSingleLine() {
		summary = p.vcsCompactSummary(block)
	}
	tail := ""
	if summary != "" {
		tail = terminalui.Dim + "· " + terminalui.Undim + summary + " "
	}
	tail += terminalui.Dim + "· " + source + terminalui.Undim
	if outcome := outcomeSuffix(p.Theme, block); outcome != "" {
		tail += " " + outcome
	}
	room := width - ansi.StringWidth(lead) - 1 - ansi.StringWidth(tail)
	heading, paths := p.vcsHeading(block.Verb, head, max(1, room))
	if heading == "" {
		if block.compactSingleLine() {
			return []string{truncateCompactLine(lead+tail, width)}
		}
		return terminalui.Hang(lead, tail, width)
	}
	// Below a readable width the heading wraps rather than shrinking to a stub.
	if (!paths || block.compactSingleLine()) && ansi.StringWidth(heading) > room && room >= 16 {
		heading = ansi.Truncate(heading, room, "…") + terminalui.Reset
	}
	if block.compactSingleLine() {
		return []string{truncateCompactLine(lead+heading+" "+tail, width)}
	}
	return terminalui.Hang(lead, heading+" "+tail, width)
}

// vcsCompactSummary produces a single-line summary for a settled VCS block in
// compact default view: git --shortstat style numbers for Diff, concise status
// counts for Status, combined Diff+Status for the same worktree, and hash +
// shortstat for Commit.
func (p *Painter) vcsCompactSummary(block Block) string {
	switch block.Verb {
	case "Diff":
		if text := vcsDiffShortstat(p.Theme, block.Changes); text != "" {
			return text
		}
		if block.Exited {
			return terminalui.Dim + "no changes" + terminalui.Undim
		}
	case "Status":
		return vcsStatusSummary(p.Theme, block.Changes, false)
	case "Diff/Status":
		var diffChanges, statusChanges []ChangeRow
		hasDiff := false
		for _, member := range block.Members {
			switch member.Verb {
			case "Diff":
				hasDiff = true
				diffChanges = append(diffChanges, member.Changes...)
			case "Status":
				statusChanges = append(statusChanges, member.Changes...)
			}
		}
		diffText := vcsDiffShortstat(p.Theme, diffChanges)
		statusText := vcsStatusSummary(p.Theme, statusChanges, hasDiff && diffText != "")
		switch {
		case diffText != "" && statusText != "":
			return diffText + terminalui.Dim + " · " + terminalui.Undim + statusText
		case diffText != "":
			return diffText
		case statusText != "":
			return statusText
		default:
			return terminalui.Dim + "clean" + terminalui.Undim
		}
	case "Commit":
		return vcsCommitShortstat(p.Theme, block.Changes)
	}
	return ""
}

// vcsDiffShortstat formats diff change rows like `git diff --shortstat`:
// e.g. `7 files changed, +69 -26`.
func vcsDiffShortstat(theme diffrender.Theme, changes []ChangeRow) string {
	if len(changes) == 0 {
		return ""
	}
	files := 0
	footerFiles := 0
	for _, row := range changes {
		if !row.Footer {
			files++
		} else if n := parseFileCount(row.Note); n > footerFiles {
			footerFiles = n
		}
	}
	if files == 0 {
		files = footerFiles
	}
	added, removed := presentation.ChangeTotals(changes)
	counts := editCounts(added, removed)
	var parts []string
	if files == 1 {
		parts = append(parts, terminalui.Dim+"1 file changed"+terminalui.Undim)
	} else if files > 1 {
		parts = append(parts, terminalui.Dim+fmt.Sprintf("%d files changed", files)+terminalui.Undim)
	}
	if counts != "" {
		parts = append(parts, countText(theme, counts))
	}
	return strings.Join(parts, terminalui.Dim+", "+terminalui.Undim)
}

func vcsCommitShortstat(theme diffrender.Theme, changes []ChangeRow) string {
	if len(changes) == 0 {
		return ""
	}
	var hash string
	for _, row := range changes {
		if row.Footer && row.Label != "" {
			hash = row.Label
			break
		}
	}
	stat := vcsDiffShortstat(theme, changes)
	switch {
	case hash != "" && stat != "":
		return theme.Hash() + hash + "\x1b[39m " + terminalui.Dim + "· " + terminalui.Undim + stat
	case hash != "":
		return theme.Hash() + hash + "\x1b[39m"
	default:
		return stat
	}
}

func parseFileCount(note string) int {
	for part := range strings.SplitSeq(note, " · ") {
		part = strings.TrimSpace(part)
		if before, ok := strings.CutSuffix(part, " files"); ok {
			if n, err := strconv.Atoi(before); err == nil {
				return n
			}
		} else if before, ok := strings.CutSuffix(part, " file"); ok {
			if n, err := strconv.Atoi(before); err == nil {
				return n
			}
		}
	}
	return 0
}

// vcsStatusSummary formats git/svn status rows into a compact single-line summary.
// When withDiff is true (combined Diff/Status), tracked-change counts already
// covered by the diff shortstat are omitted so only additional status facts
// (conflicts, untracked files, branch/ahead/behind) are appended.
func vcsStatusSummary(theme diffrender.Theme, changes []ChangeRow, withDiff bool) string {
	if len(changes) == 0 {
		return ""
	}
	changed, untracked, conflicted := 0, 0, 0
	branch, branchNote, clean := "", "", false
	for _, row := range changes {
		if row.Footer {
			if row.Label != "" {
				branch = row.Label
			}
			if row.Note != "" {
				if row.Note == "clean" {
					clean = true
				} else if before, ok := strings.CutSuffix(row.Note, " · clean"); ok {
					branchNote, clean = before, true
				} else {
					branchNote = row.Note
				}
			}
			continue
		}
		switch {
		case row.Code == "??":
			untracked++
		case strings.ContainsAny(row.Code, "U") || row.Code == "AA" || row.Code == "DD":
			conflicted++
		default:
			changed++
		}
	}
	var parts []string
	if branch != "" {
		b := theme.Hash() + branch + "\x1b[39m"
		if branchNote != "" {
			b += " " + terminalui.Dim + branchNote + terminalui.Undim
		}
		parts = append(parts, b)
	} else if branchNote != "" {
		parts = append(parts, terminalui.Dim+branchNote+terminalui.Undim)
	}
	if conflicted > 0 {
		unit := "conflicts"
		if conflicted == 1 {
			unit = "conflict"
		}
		parts = append(parts, theme.Danger()+fmt.Sprintf("%d %s", conflicted, unit)+terminalui.Reset)
	}
	if !withDiff && changed > 0 {
		parts = append(parts, terminalui.Dim+fmt.Sprintf("%d modified", changed)+terminalui.Undim)
	}
	if untracked > 0 {
		parts = append(parts, terminalui.Dim+fmt.Sprintf("%d untracked", untracked)+terminalui.Undim)
	}
	if !withDiff && changed == 0 && untracked == 0 && conflicted == 0 && clean {
		parts = append(parts, terminalui.Dim+"clean"+terminalui.Undim)
	}
	return strings.Join(parts, terminalui.Dim+" · "+terminalui.Undim)
}

// MergeVCSDiffStatus combines adjacent settled Diff and Status operations that
// target the same worktree into a single Diff/Status row for default view,
// retaining all original operations as Members for the output dialog.
func MergeVCSDiffStatus(blocks []Block) []Block {
	var out []Block
	for i := 0; i < len(blocks); {
		tool, worktree, ok := vcsWorktreeKey(blocks[i])
		if !ok {
			out = append(out, blocks[i])
			i++
			continue
		}
		j := i + 1
		hasDiff := blocks[i].Verb == "Diff"
		hasStatus := blocks[i].Verb == "Status"
		for j < len(blocks) {
			nextTool, nextWorktree, nextOK := vcsWorktreeKey(blocks[j])
			if !nextOK || nextTool != tool || nextWorktree != worktree {
				break
			}
			hasDiff = hasDiff || blocks[j].Verb == "Diff"
			hasStatus = hasStatus || blocks[j].Verb == "Status"
			j++
		}
		if !hasDiff || !hasStatus {
			out = append(out, blocks[i:j]...)
			i = j
			continue
		}
		out = append(out, combineVCSDiffStatus(blocks[i:j], tool))
		i = j
	}
	return out
}

func vcsWorktreeKey(b Block) (tool, worktree string, ok bool) {
	if !b.VCS() || (b.Verb != "Diff" && b.Verb != "Status") {
		return "", "", false
	}
	if b.Running || b.Skipped || b.Pending || b.Requested || b.FailedOutcome() || b.ExitUnknown() || b.Approval != "" {
		return "", "", false
	}
	_, source, ok := presentation.VCSSource(b.Label)
	if !ok {
		return "", "", false
	}
	fields := strings.Fields(source)
	if len(fields) == 0 || (fields[0] != "git" && fields[0] != "svn") {
		return "", "", false
	}
	tool = fields[0]
	dir := ""
	for k := 1; k+1 < len(fields) && fields[k] == "-C"; k += 2 {
		if dir == "" || filepath.IsAbs(fields[k+1]) {
			dir = fields[k+1]
		} else {
			dir = filepath.Join(dir, fields[k+1])
		}
	}
	switch {
	case filepath.IsAbs(dir):
		worktree = filepath.Clean(dir)
	case b.Workdir != "" && dir != "":
		worktree = filepath.Clean(filepath.Join(b.Workdir, dir))
	case b.Workdir != "":
		worktree = filepath.Clean(b.Workdir)
	case dir != "":
		worktree = filepath.Clean(dir)
	}
	return tool, worktree, true
}

func combineVCSDiffStatus(run []Block, tool string) Block {
	var diffBlock *Block
	var members []presentation.Value
	var duration = run[0].Duration
	for idx := range run {
		if idx > 0 {
			duration += run[idx].Duration
		}
		members = append(members, run[idx].Value)
		if run[idx].Verb == "Diff" && diffBlock == nil {
			diffBlock = &run[idx]
		}
	}
	base := run[0]
	if diffBlock != nil {
		base = *diffBlock
	}
	head, source, _ := presentation.VCSSource(base.Label)
	// Preserve any -C worktree selector in the combined source label, dropping
	// diff-only format flags such as --numstat or --stat.
	fields := strings.Fields(source)
	sourceParts := []string{tool}
	for k := 1; k+1 < len(fields) && fields[k] == "-C"; k += 2 {
		sourceParts = append(sourceParts, "-C", fields[k+1])
	}
	if head == "" {
		head = "`working tree`"
	}
	merged := base
	merged.Value = presentation.Group(base.Value, members)
	merged.Verb = "Diff/Status"
	merged.Label = head + " · " + strings.Join(sourceParts, " ")
	merged.Duration = duration
	merged.Changes = nil
	merged.Tail, merged.TailOmitted = nil, 0
	return merged
}

// vcsHeading styles a VCS heading: a leading span is the subject or scope,
// later spans are paths, and connecting words are muted. A commit's
// autosquash marker, such as "amend!", reads as a marker, not subject text.
func (p *Painter) vcsHeading(verb, head string, width int) (string, bool) {
	var out strings.Builder
	paths := false
	for i := 0; i < len(head); {
		if code, end, ok := markdownsyntax.CodeSpan(head, i); ok {
			switch {
			case i > 0 || verb == "Stage":
				paths = true
				out.WriteString(terminalui.StyledPath(pathterminal.Truncate(code, max(1, width-ansi.StringWidth(out.String())))))
			case verb == "Commit":
				for _, marker := range []string{"amend! ", "fixup! ", "squash! "} {
					if rest, found := strings.CutPrefix(code, marker); found {
						out.WriteString(p.Theme.Warning() + strings.TrimSpace(marker) + "\x1b[39m ")
						code = rest
						break
					}
				}
				out.WriteString(code)
			default:
				out.WriteString(code)
			}
			i = end
			continue
		}
		next := strings.IndexByte(head[i+1:], '`')
		text := head[i:]
		if next >= 0 {
			text = head[i : i+1+next]
		}
		out.WriteString(terminalui.Dim + text + terminalui.Undim)
		i += len(text)
	}
	return out.String(), paths
}

// vcsSummary is a VCS row's roster summary: its verb and heading, the
// changed lines its rows name, and its source.
func (p *Painter) vcsSummary(block Block) string {
	head, source, _ := presentation.VCSSource(block.Label)
	detail, _ := p.vcsHeading(block.Verb, head, ansi.StringWidth(head))
	if counts := editCounts(presentation.ChangeTotals(block.Changes)); counts != "" {
		detail = strings.TrimSpace(detail + " " + countText(p.Theme, counts))
	}
	detail = strings.TrimSpace(detail + " " + terminalui.Dim + "· " + source + terminalui.Undim)
	if outcome := outcomeSuffix(p.Theme, block); outcome != "" {
		detail += " " + outcome
	}
	return block.verbColor(p.Theme) + block.RowVerb() + terminalui.Reset + " " + detail
}
