package terminalview

import (
	"fmt"
	"strings"

	pathterminal "github.com/yusing/mekugi/libraries/pathdisplay/terminal"

	"github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"

	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// vcsRow lays out a VCS operation on one row where it fits: the verb, its
// heading cut short to leave room for the source, then any exit.
func (p *Painter) vcsRow(block Block, width int) []string {
	verb, color := block.RowVerb(), VerbColor(block.Verb)
	if block.ExitCode != 0 {
		color = terminalui.Red
	}
	lead := block.lead(color, verb)
	head, source, _ := presentation.VCSSource(block.Label)
	tail := terminalui.Dim + "· " + source + terminalui.Undim
	if block.ExitCode != 0 {
		tail += terminalui.Dim + " · " + terminalui.Undim + exitText(block.ExitCode)
	}
	room := width - ansi.StringWidth(lead) - 1 - ansi.StringWidth(tail)
	heading, paths := p.vcsHeading(block.Verb, head, max(1, room))
	if heading == "" {
		return terminalui.Hang(lead, tail, width)
	}
	// Below a readable width the heading wraps rather than shrinking to a stub.
	if !paths && ansi.StringWidth(heading) > room && room >= 16 {
		heading = ansi.Truncate(heading, room, "…") + terminalui.Reset
	}
	return terminalui.Hang(lead, heading+" "+tail, width)
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
	return out.String(), paths
}

// vcsSummary is a VCS row's roster summary: its verb and heading, the
// changed lines its rows name, and its source.
func (p *Painter) vcsSummary(block Block) string {
	head, source, _ := presentation.VCSSource(block.Label)
	detail, _ := p.vcsHeading(block.Verb, head, ansi.StringWidth(head))
	if counts := editCounts(presentation.ChangeTotals(block.Changes)); counts != "" {
		detail = strings.TrimSpace(detail + " " + countText(counts))
	}
	detail = strings.TrimSpace(detail + " " + terminalui.Dim + "· " + source + terminalui.Undim)
	if block.ExitCode != 0 {
		detail += " " + terminalui.Red + fmt.Sprintf("(exit %d)", block.ExitCode) + terminalui.Reset
	}
	return SummaryVerb(block.RowVerb()) + detail
}
