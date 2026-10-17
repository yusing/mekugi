package terminalview

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"

	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// committed reports a commit the host completed and whose output named the
// commit it recorded; without that evidence the row keeps the request verb.
func (b Block) committed() bool {
	return b.Verb == "Commit" && !b.Running && !b.Skipped && b.ExitCode == 0 &&
		slices.ContainsFunc(b.Changes, func(row ChangeRow) bool { return row.Footer }) && b.VCS()
}

// vcsRow lays out a VCS operation on one row where it fits: the verb, its
// heading cut short to leave room for the source, then any exit.
func (p *Painter) vcsRow(block Block, width int) []string {
	verb, color := RowVerb(block), VerbColor(block.Verb)
	if block.ExitCode != 0 {
		color = terminalui.Red
	}
	lead := block.lead(color, verb)
	head, source, _ := presentation.VCSSource(block.Label)
	tail := terminalui.Dim + "· " + source + terminalui.Undim
	if block.ExitCode != 0 {
		tail += terminalui.Dim + " · " + terminalui.Undim + exitText(block.ExitCode)
	}
	heading := p.vcsHeading(block.Verb, head)
	if heading == "" {
		return terminalui.Hang(lead, tail, width)
	}
	// Below a readable width the heading wraps rather than shrinking to a stub.
	if room := width - ansi.StringWidth(lead) - 1 - ansi.StringWidth(tail); ansi.StringWidth(heading) > room && room >= 16 {
		heading = ansi.Truncate(heading, room, "…") + terminalui.Reset
	}
	return terminalui.Hang(lead, heading+" "+tail, width)
}

// vcsHeading styles a VCS heading: a leading span is the subject or scope,
// later spans are paths, and connecting words are muted. A commit's
// autosquash marker, such as "amend!", reads as a marker, not subject text.
func (p *Painter) vcsHeading(verb, head string) string {
	var out strings.Builder
	for i := 0; i < len(head); {
		if code, end, ok := markdownsyntax.CodeSpan(head, i); ok {
			switch {
			case i > 0 || verb == "Stage":
				out.WriteString(terminalui.StyledPath(code))
			case verb == "Commit":
				for _, marker := range []string{"amend! ", "fixup! ", "squash! "} {
					if rest, found := strings.CutPrefix(code, marker); found {
						out.WriteString(terminalui.Amber + strings.TrimSpace(marker) + "\x1b[39m ")
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
	return out.String()
}

// vcsSummary is a VCS row's roster summary: its verb and heading, the
// changed lines its rows name, and its source.
func (p *Painter) vcsSummary(block Block) string {
	head, source, _ := presentation.VCSSource(block.Label)
	detail := p.vcsHeading(block.Verb, head)
	if counts := editCounts(presentation.ChangeTotals(block.Changes)); counts != "" {
		detail = strings.TrimSpace(detail + " " + countText(counts))
	}
	detail = strings.TrimSpace(detail + " " + terminalui.Dim + "· " + source + terminalui.Undim)
	if block.ExitCode != 0 {
		detail += " " + terminalui.Red + fmt.Sprintf("(exit %d)", block.ExitCode) + terminalui.Reset
	}
	return SummaryVerb(RowVerb(block)) + detail
}
