package activity

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// VCS operations read as their own rows rather than Run previews. Their
// label is an optional heading code span, such as a commit subject or diff
// scope, optional path spans after "in", then "· " and the source tool with
// any flag that changes what the row shows, such as "git --stat".
var vcsVerbs = []string{"Commit", "Stage", "Diff", "Status", "Check"}

// VCS reports a version-control row: a commit, stage, diff, status or
// whitespace check whose source names git, svn or mchanges.
func (b Block) VCS() bool {
	if b.Kind != "op" || !slices.Contains(vcsVerbs, b.Verb) {
		return false
	}
	_, _, ok := vcsSource(b.Label)
	return ok
}

// vcsSource splits a VCS label into its heading and source.
func vcsSource(label string) (head, source string, ok bool) {
	i := strings.LastIndex(label, "· ")
	if i < 0 || i > 0 && label[i-1] != ' ' {
		return "", "", false
	}
	head, source = strings.TrimSpace(label[:i]), label[i+len("· "):]
	tool, _, _ := strings.Cut(source, " ")
	return head, source, tool == "git" || tool == "svn" || tool == "mchanges"
}

// committed reports a commit the host completed and whose output named the
// commit it recorded; without that evidence the row keeps the request verb.
func (b Block) committed() bool {
	return b.Verb == "Commit" && !b.Running && !b.Skipped && b.ExitCode == 0 &&
		slices.ContainsFunc(b.Changes, func(row ChangeRow) bool { return row.Footer }) && b.VCS()
}

// FoldStages drops staging rows that lead straight into a commit of the same
// invocation: the commit's rows name the files it recorded. A failed or
// skipped stage, or one before a skipped commit, stays visible.
func FoldStages(blocks []Block) []Block {
	folds := func(i int) bool {
		if b := blocks[i]; b.Verb != "Stage" || !b.VCS() || b.ExitCode != 0 || b.Skipped || !b.Started.IsZero() || b.Duration != 0 {
			return false
		}
		for _, next := range blocks[i+1:] {
			switch {
			case next.Verb == "Stage" && next.VCS() && next.ExitCode == 0 && !next.Skipped:
				continue
			case next.Verb == "Commit" && next.VCS():
				return !next.Skipped
			}
			return false
		}
		return false
	}
	var kept []Block
	for i := range blocks {
		if !folds(i) {
			kept = append(kept, blocks[i])
		}
	}
	return kept
}

// vcsRow lays out a VCS operation on one row where it fits: the verb, its
// heading cut short to leave room for the source, then any exit.
func (p *Painter) vcsRow(block Block, width int) []string {
	verb, color := RowVerb(block), VerbColor(block.Verb)
	if block.ExitCode != 0 {
		color = Red
	}
	lead := block.lead(color, verb)
	head, source, _ := vcsSource(block.Label)
	tail := Dim + "· " + source + Undim
	if block.ExitCode != 0 {
		tail += Dim + " · " + Undim + exitText(block.ExitCode)
	}
	heading := p.vcsHeading(block.Verb, head)
	if heading == "" {
		return liveActivityHang(lead, tail, width)
	}
	// Below a readable width the heading wraps rather than shrinking to a stub.
	if room := width - ansi.StringWidth(lead) - 1 - ansi.StringWidth(tail); ansi.StringWidth(heading) > room && room >= 16 {
		heading = ansi.Truncate(heading, room, "…") + Reset
	}
	return liveActivityHang(lead, heading+" "+tail, width)
}

// vcsHeading styles a VCS heading: a leading span is the subject or scope,
// later spans are paths, and connecting words are muted. A commit's
// autosquash marker, such as "amend!", reads as a marker, not subject text.
func (p *Painter) vcsHeading(verb, head string) string {
	var out strings.Builder
	for i := 0; i < len(head); {
		if code, end, ok := liveActivityCodeSpan(head, i); ok {
			switch {
			case i > 0 || verb == "Stage":
				out.WriteString(Path(code))
			case verb == "Commit":
				for _, marker := range []string{"amend! ", "fixup! ", "squash! "} {
					if rest, found := strings.CutPrefix(code, marker); found {
						out.WriteString(Amber + strings.TrimSpace(marker) + "\x1b[39m ")
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
		out.WriteString(Dim + text + Undim)
		i += len(text)
	}
	return out.String()
}

// vcsSummary is a VCS row's roster summary: its verb and heading, the
// changed lines its rows name, and its source.
func (p *Painter) vcsSummary(block Block) string {
	head, source, _ := vcsSource(block.Label)
	detail := p.vcsHeading(block.Verb, head)
	if counts := editCounts(ChangeTotals(block.Changes)); counts != "" {
		detail = strings.TrimSpace(detail + " " + countText(counts))
	}
	detail = strings.TrimSpace(detail + " " + Dim + "· " + source + Undim)
	if block.ExitCode != 0 {
		detail += " " + Red + fmt.Sprintf("(exit %d)", block.ExitCode) + Reset
	}
	return SummaryVerb(RowVerb(block)) + detail
}
