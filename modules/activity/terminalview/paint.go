package terminalview

import (
	"github.com/yusing/mekugi/libraries/pathdisplay"
	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/activity/presentation"

	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

// Painter lays out parsed blocks as terminal rows. Syntax colors
// come from the same renderer and theme as the live diff pane.
type Painter struct {
	terminalui.Painter
	Clock func() time.Time // Optional presentation clock, shared with replay.
}

func (p *Painter) now() time.Time {
	if p.Clock != nil {
		return p.Clock()
	}
	return time.Now()
}

func VerbColor(verb string) string {
	first, _, _ := strings.Cut(verb, " ")
	switch first {
	case "Attached":
		return terminalui.Green
	case "Attach":
		return terminalui.Red
	case "Read", "Inspect", "View", "Open", "List", "Check", "Diff", "Status":
		return "\x1b[38;5;75m"
	case "Commit", "Committed", "Stage":
		return Hash
	case "Search", "Web", "Find", "Skill":
		return "\x1b[38;5;141m"
	case "Run", "Running", "Send", "Sleep", "Wait", "Still", "Stop":
		return terminalui.Amber
	case "Edit", "Edited", "Create", "Delete", "Update", "Write", "Move", "Rename":
		return terminalui.Green
	case "MCP", "Tool", "Browse", "Generate":
		return "\x1b[38;5;80m"
	}
	return ""
}

// liveAgentGutter is the unbolded agent color used for feed gutters.
func Gutter(name string, theme diffrender.Theme) string {
	if color := terminalui.AgentColor(name); color != "" {
		return strings.Replace(color, "1;", "", 1)
	}
	return theme.Accent()
}

func (p *Painter) Agent(name string) string {
	color := terminalui.AgentColor(name)
	if color == "" {
		color = "\x1b[1m" + p.Theme.Accent()
	}
	return color + pathdisplay.AgentDisplayName(name) + terminalui.Reset
}

func (p *Painter) recipient(name string) string {
	return Gutter(name, p.Theme) + pathdisplay.AgentDisplayName(name) + terminalui.Reset
}

// code styles a code span according to the operation that carries it.
func (p *Painter) code(verb, code string) string {
	first, _, _ := strings.Cut(verb, " ")
	switch first {
	case "Skill":
		prefix := ""
		if strings.HasPrefix(code, "run ") || strings.HasPrefix(code, "run\t") {
			rest := strings.TrimLeft(code[3:], " \t")
			prefix, code = code[:len(code)-len(rest)], rest
		}
		end := strings.IndexAny(code, "/ \t")
		if end < 0 {
			end = len(code)
		}
		return prefix + "\x1b[1m" + code[:end] + terminalui.Undim + code[end:]
	case "Run", "Send":
		return strings.Join(p.Highlight("bash", code), " ")
	case "Search":
		// Search operands are patterns, not shell programs. A shell lexer
		// miscolors regular-expression punctuation and can reset the verb color.
		return p.Theme.Accent() + code + "\x1b[39m"
	case "Read", "Edit", "Create", "Delete", "Update", "Write", "View", "Move", "Rename":
		return terminalui.StyledPath(code)
	case "MCP", "Tool":
		return "\x1b[38;5;80m" + code + "\x1b[39m"
	}
	return p.Theme.Accent() + code + "\x1b[39m"
}

// compactSearchTargets only abbreviates the target spans after the query.
// The retained label remains the source for dialogs and copying.
func (p *Painter) compactSearchTargets(label string) string {
	original := p.Label("Search", label)
	_, end, ok := markdownsyntax.CodeSpan(label, 0)
	if !ok || !strings.HasPrefix(label[end:], " in ") {
		return original
	}
	start := end + len(" in ")
	var paths []string
	for i := start; i < len(label); {
		path, end, ok := markdownsyntax.CodeSpan(label, i)
		if !ok {
			return original
		}
		paths = append(paths, path)
		i = end
		if i < len(label) {
			if label[i] != ' ' {
				return original
			}
			i++
		}
	}
	prefix := pathdisplay.CommonDirectory(paths)
	if prefix == "" {
		return original
	}
	for i := range paths {
		paths[i] = terminalui.StyledPath(strings.TrimPrefix(paths[i], prefix))
	}
	compact := p.Label("Search", label[:start]) + terminalui.Dim + prefix + "{" + terminalui.Undim + strings.Join(paths, terminalui.Dim+","+terminalui.Undim) + terminalui.Dim + "}" + terminalui.Undim
	return compact
}

// A brace group is an atomic display abbreviation, like a clipped output row:
// selecting it copies the unabbreviated operands, never a fictional brace path.
func (p *Painter) copyPathGroup(compact, original string) string {
	if !p.CopySource || p.LayoutOnly {
		return compact
	}
	return terminalui.AttachCopy(compact, []terminalui.CopySpan{{CopyFragment: terminalui.CopyFragment{
		ID: terminalui.CopyID(original, p.CopyScope), Prefix: ansi.Strip(original), Width: ansi.StringWidth(compact),
	}}})
}

func (p *Painter) Label(verb, label string) string {
	if slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, verb) {
		if head, source, ok := strings.CutLast(label, " · "); ok && !strings.ContainsRune(source, '`') {
			// Source metadata uses the same secondary foreground as UI chrome.
			return p.Label(verb, head) + terminalui.Dim + " · " + source + terminalui.Undim + "\x1b[39m"
		}
	}
	var out strings.Builder
	searchTargets := false
	for i := 0; i < len(label); {
		if code, end, ok := markdownsyntax.CodeSpan(label, i); ok {
			if verb == "Search" && searchTargets {
				out.WriteString(terminalui.StyledPath(code))
			} else {
				out.WriteString(p.code(verb, code))
			}
			i = end
			continue
		}
		next := strings.IndexByte(label[i+1:], '`')
		text := label[i:]
		if next >= 0 {
			text = label[i : i+1+next]
		}
		if verb == "Search" && strings.Contains(text, " in ") {
			searchTargets = true
		}
		for j, word := range strings.Split(text, " ") {
			if j > 0 {
				out.WriteByte(' ')
			}
			switch {
			case len(word) > 1 && word[0] == '+':
				word = terminalui.Green + word + "\x1b[39m"
			case len(word) > 1 && word[0] == '-':
				word = terminalui.Red + word + "\x1b[39m"
			case word != "":
				word = terminalui.Dim + word + terminalui.Undim
			}
			out.WriteString(word)
		}
		i += len(text)
	}
	return out.String()
}

// program keeps operation source under a gutter beside its verb.
func (p *Painter) program(lang, source string, width int, block Block) []string {
	var lines []string
	hidden := 0
	width = max(1, width-2)
	for _, line := range p.Highlight(lang, source) {
		if block.SourceRows > 0 {
			var count int
			line, count = programPreview(line, width, max(0, block.SourceRows-len(lines)))
			if len(lines) >= block.SourceRows {
				hidden += count
				continue
			}
			hidden += max(0, count-(block.SourceRows-len(lines)))
		}
		for _, part := range terminalui.Wrap(line, width, true) {
			lines = append(lines, terminalui.Dim+"│"+terminalui.Undim+" "+part)
		}
	}
	if hidden > 0 {
		keep := max(1, block.SourceRows-1)
		hidden += len(lines) - keep
		lines = append(lines[:keep:keep], terminalui.Dim+"│"+terminalui.Undim+" "+Elision{Hidden: hidden, Hovered: block.Hovered}.String())
	}
	return lines
}

// programPreview counts hard-wrapped rows without materializing hidden text.
// ANSI sequences and wide graphemes retain their geometry; only the visible
// prefix is handed to the shared styled wrapper. Full source stays in the block.
func programPreview(line string, width, limit int) (string, int) {
	rows, cells, end := 1, 0, len(line)
	if limit == 0 {
		end = 0
	}
	var state byte
	for at := 0; at < len(line); {
		n, size := 1, 1
		if line[at] < ' ' || line[at] > '~' || state != 0 {
			_, size, n, state = ansi.DecodeSequence(line[at:], state, nil)
		}
		if size > 0 && cells+size > width {
			rows++
			cells = 0
			if rows == limit+1 && limit > 0 {
				end = at
			}
		}
		cells += size
		at += n
	}
	return line[:end], rows
}

func liveActivityIndent(lines []string, prefix string) []string {
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return lines
}

// Block renders one parsed block within width columns.
func (p *Painter) Block(block Block, width int) []string {
	width = max(8, width)
	if block.Effect {
		block.Effect = false
		return liveActivityIndent(p.Block(block, width-2), "  ")
	}
	lines := p.blockRows(block, width)
	if block.AutoReload && block.Exited && block.ExitCode == 0 && !block.Running && len(lines) > 0 {
		suffix := terminalui.Dim + " (auto reloaded)" + terminalui.Undim
		if ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
			lines[0] += suffix
		} else {
			lines = slices.Insert(lines, 1, terminalui.Hang(strings.Repeat(" ", block.cell(RowVerb(block))), suffix, width)...)
		}
	}
	if len(block.Timeouts) > 0 && !block.Skipped && runDuration(block, p.now()) >= 500*time.Millisecond && len(lines) > 0 {
		var labels []string
		for _, timeout := range block.Timeouts {
			labels = append(labels, "(timeout "+formatTimeout(timeout)+")")
		}
		suffix := terminalui.Dim + strings.Join(labels, " ") + terminalui.Undim
		if ansi.StringWidth(lines[0])+1+ansi.StringWidth(suffix) <= width {
			lines[0] += " " + suffix
		} else {
			lines = slices.Insert(lines, 1, terminalui.Hang(strings.Repeat(" ", block.cell(RowVerb(block))), suffix, width)...)
		}
	}
	if outcome := approvalLabel(block.Approval); outcome != "" && len(lines) > 0 {
		if block.AutoReload && strings.HasPrefix(block.Approval, "Pending Approval") {
			outcome = terminalui.Amber + "Approving" + terminalui.Reset
		}
		suffix := terminalui.Dim + " · " + terminalui.Undim + outcome
		if ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
			lines[0] += suffix
		} else {
			lines = slices.Insert(lines, 1, terminalui.Hang(strings.Repeat(" ", block.cell(RowVerb(block))), outcome, width)...)
		}
	}
	if block.ShowWorkdir && block.Workdir != "" && block.Verb != "Run" && len(lines) > 0 {
		// Run rows carry it in their label, ahead of the exit and elapsed time.
		if suffix := " " + workdirLabel(block.Workdir); ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
			lines[0] += suffix
		} else {
			lines = slices.Insert(lines, 1, terminalui.Hang(strings.Repeat(" ", block.cell(RowVerb(block))), workdirLabel(block.Workdir), width)...)
		}
	}
	if block.Verb != "Run" && !block.Skipped && len(lines) > 0 {
		if elapsed := RunElapsed(block, p.now()); elapsed != "" {
			suffix := terminalui.Dim + " · " + elapsed + terminalui.Undim
			if ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
				lines[0] += suffix
			} else {
				lines = append(lines, strings.Repeat(" ", block.cell(RowVerb(block)))+terminalui.Dim+"· "+elapsed+terminalui.Undim)
			}
		}
	}
	if trailer := segmentTrailer(block); trailer != "" && len(lines) > 0 {
		if last := len(lines) - 1; ansi.StringWidth(lines[last])+1+ansi.StringWidth(trailer) <= width {
			lines[last] += " " + trailer
		} else {
			lines = append(lines, strings.Repeat(" ", block.cell(RowVerb(block)))+trailer)
		}
	}
	if block.Repeated != "" && len(lines) > 0 {
		suffix := terminalui.Dim + " · " + block.Repeated + terminalui.Undim
		if ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
			lines[0] += suffix
		} else {
			lines = slices.Insert(lines, 1, terminalui.Hang(strings.Repeat(" ", block.cell(RowVerb(block))), terminalui.Dim+block.Repeated+terminalui.Undim, width)...)
		}
	}
	// Row metadata is appended after compact path headings. Keep it selectable
	// independently while joining it to the original heading in whole-row copies.
	if p.CopySource && !p.LayoutOnly && len(lines) > 0 {
		row, spans := terminalui.ExtractCopy(lines[0])
		if len(spans) == 1 && spans[0].Text == "" && spans[0].Prefix != "" {
			end := spans[0].Column + spans[0].Width
			if width := ansi.StringWidth(row); end < width {
				spans = append(spans, terminalui.CopySpan{Column: end, CopyFragment: terminalui.CopyFragment{
					ID: spans[0].ID, Text: ansi.Strip(ansi.Cut(row, end, width)), Offset: 1, Width: width - end,
				}})
				lines[0] = terminalui.AttachCopy(row, spans)
			}
		}
	}
	if block.Skipped {
		for i, line := range lines {
			row, spans := terminalui.ExtractCopy(line)
			lines[i] = terminalui.AttachCopy(terminalui.Dim+ansi.Strip(row)+terminalui.Reset, spans)
		}
	}
	if block.Expanded && block.MarkdownOutput() && len(block.Tail) > 0 {
		indent := min(block.cell(RowVerb(block)), max(0, width-8))
		rows := p.Markdown(strings.Join(block.Tail, "\n"), width-indent, false)
		if block.TailOmitted > 0 {
			rows = append([]string{terminalui.Dim + Elision{Hidden: block.TailOmitted, Form: ElisionEarlier}.Text() + " not retained" + terminalui.Undim}, rows...)
		}
		return append(lines, liveActivityIndent(rows, strings.Repeat(" ", indent))...)
	}
	if !block.Collapsed {
		tail := p.tailColors(block)
		if p.CopySource && !p.LayoutOnly {
			identity := terminalui.CopyID(strings.Join(block.Tail, "\n"), p.CopyScope)
			for i, row := range tail {
				if ansi.Strip(row) != block.Tail[i] {
					tail[i] = terminalui.AttachCopy(row, []terminalui.CopySpan{{ID: terminalui.CopyID(strconv.Itoa(i), identity), Prefix: block.Tail[i], Width: ansi.StringWidth(row)}})
				}
			}
		}
		block.Tail = tail
	}
	return outputRows(block, lines, width)
}

// approvalLabel keeps the decision separate from whether a command ran or
// succeeded. Withdrawal and external resolution do not imply a decision.
func approvalLabel(outcome string) string {
	switch {
	case strings.HasPrefix(outcome, "Pending Approval"):
		return terminalui.Amber + "Pending Approval" + terminalui.Reset
	case strings.HasPrefix(outcome, "Auto Denied"):
		return terminalui.Red + "Auto Denied" + terminalui.Reset
	case strings.HasPrefix(outcome, "Approved"), strings.HasPrefix(outcome, "Granted"), strings.HasPrefix(outcome, "Allowed"):
		return terminalui.Green + "Approved" + terminalui.Reset
	case strings.HasPrefix(outcome, "Denied"), strings.HasPrefix(outcome, "Declined"), strings.HasPrefix(outcome, "Blocked"):
		return terminalui.Red + "Denied" + terminalui.Reset
	}
	return ""
}

// workdirLabel names the directory a command ran in when it is not the
// workspace, so its relative paths do not read as workspace paths.
func workdirLabel(workdir string) string {
	return terminalui.Dim + "· in " + terminalui.Undim + terminalui.StyledPath(workdir)
}

// segmentTrailer names a tracked segment's outcome where its row would not:
// a list that never reached it, or the exit of an operation whose row shows
// no exit, such as a failed read.
func segmentTrailer(block Block) string {
	switch {
	case block.Skipped && RowVerb(block) != "Skipped" && EditStatus(block) != "skipped":
		return "· skipped"
	case block.ExitCode == 0 || block.EditOperation() || block.Kind != "op" && block.Kind != "reads" || block.VCS():
		return ""
	case block.Kind == "op" && (block.Verb == "Run" || block.Verb == "Skill" || block.Verb == "Capture" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.Verb)):
		return "" // The row already shows the exit.
	}
	return terminalui.Dim + "· " + terminalui.Undim + exitText(block.ExitCode)
}

func (p *Painter) blockRows(block Block, width int) []string {
	if block.EditOperation() && block.GroupHeader != "" {
		return p.editGroupRow(block, width)
	}
	switch block.Kind {
	case "progress":
		style := p.statusStyle(block.Live)
		text := block.progressText(style)
		if block.Label != "" && block.Hovered {
			text = Underline(text)
		}
		return terminalui.Wrap(style+"• "+text+terminalui.Reset, width, false)
	case "summary":
		// Public reasoning shares one streaming block across providers.
		if block.Label == "" {
			block.Label = presentation.ReasoningSections(block.Body)[0].Label
		}
		body := presentation.ReasoningSummaryBody(block.Body)
		if body == "" {
			// A request's thinking block before its first delta.
			if block.Live && strings.TrimSpace(block.Body) == "" {
				header, _ := p.thinkingHeader(block, width-2)
				return []string{terminalui.Dim + "• " + header + terminalui.Undim}
			}
			return nil
		}
		header, headingElided := p.thinkingHeader(block, width-2)
		rows, short, titleOnly := p.reasoningContent(block, width)
		if short {
			// Short summaries stay visible, including heading-only summaries,
			// both while streaming and after completion.
			text := rows[0]
			if !block.Live && block.Thought >= time.Second {
				text = reasoningFor(text, FormatDuration(block.Thought))
			}
			rows = terminalui.Hang("• ", text, width)
			for i, row := range rows {
				rows[i] = reasoningRow(row)
			}
			return rows
		}
		if block.Collapsed {
			if block.Hovered {
				header = Underline(header)
			}
			return []string{terminalui.Dim + "• " + header + terminalui.Undim}
		}
		if block.Live && !block.Expanded {
			var hidden int
			if rows, hidden = TailRows(rows, ThinkingTailRows); hidden > 0 {
				suffix := " · " + Elision{Hidden: hidden, Form: ElisionSuffix, Hovered: block.Hovered}.String()
				suffix = ansi.Truncate(suffix, max(0, width-3), "…")
				header, headingElided = p.thinkingHeader(block, width-2-ansi.StringWidth(suffix))
				header += suffix + terminalui.Dim
			}
		}
		if block.Hovered && headingElided && !titleOnly {
			header = Underline(header)
		}
		for i, row := range rows {
			rows[i] = reasoningRow("  " + row)
		}
		return append([]string{terminalui.Dim + "• " + header + terminalui.Undim}, rows...)
	case "final":
		return append([]string{terminalui.Green + "✓ Final answer" + terminalui.Reset}, liveActivityIndent(p.Markdown(block.Body, width-2, false), "  ")...)
	case "reads":
		color := VerbColor(block.Verb)
		lead, count := block.lead(color, block.Verb), ResultCount(block.Results, block.Verb)+readLines(block)
		indent := ansi.StringWidth(lead)
		literal := block.Verb == "Search" || block.Verb == "Web Search" || block.Verb == "Skill"
		// fit shortens a path that cannot share its row with its ranges, gap
		// cells after it, and, on the last row, the count after them.
		fit := func(read activitymodel.Read, gap int, last bool) string {
			if literal {
				return read.Path
			}
			room := width - indent
			if len(read.Ranges) > 0 {
				room -= gap
			}
			room -= ansi.StringWidth(readDetails(read, false))
			if last {
				room -= ansi.StringWidth(count)
			}
			return fitPath(read.Path, room)
		}
		item := func(path string, read activitymodel.Read) string {
			item := terminalui.StyledPath(path)
			if literal {
				item = p.code(block.Verb, path)
			}
			if len(read.Ranges) > 0 {
				item += " "
			}
			return item + readDetails(read, block.Hovered)
		}
		var items []string
		var paths []string
		for _, read := range block.Reads {
			items = append(items, item(read.Path, read))
			paths = append(paths, read.Path)
		}
		if prefix := pathdisplay.CommonDirectory(paths); prefix != "" && !literal {
			var grouped []string
			for _, read := range block.Reads {
				grouped = append(grouped, item(strings.TrimPrefix(read.Path, prefix), read))
			}
			compact := terminalui.Dim + prefix + "{" + terminalui.Undim + strings.Join(grouped, terminalui.Dim+","+terminalui.Undim) + terminalui.Dim + "}" + terminalui.Undim + count
			if indent+ansi.StringWidth(compact) <= width {
				return []string{p.copyPathGroup(lead+compact, lead+strings.Join(items, " · ")+count)}
			}
		}
		joined := strings.Join(items, terminalui.Dim+" · "+terminalui.Undim) + count
		if indent+ansi.StringWidth(joined) <= width || indent > width/2 {
			return terminalui.Hang(lead, joined, width)
		}
		if len(items) < 2 {
			return terminalui.Hang(lead, item(fit(block.Reads[0], 1, true), block.Reads[0])+count, width)
		}
		// Items that do not fit on one row take one row each, rather than
		// leaving separators dangling at wrapped row ends. Their ranges share
		// a column when it fits.
		column := 0
		for i, read := range block.Reads {
			paths[i] = fit(read, 2, i == len(block.Reads)-1)
			items[i] = item(paths[i], read)
			column = max(column, ansi.StringWidth(paths[i]))
		}
		for i, read := range block.Reads {
			if len(read.Ranges) > 0 && !literal && indent+column+2+ansi.StringWidth(readDetails(read, false)) <= width {
				items[i] = terminalui.StyledPath(paths[i]) + strings.Repeat(" ", column-ansi.StringWidth(paths[i])+2) + readDetails(read, block.Hovered)
			}
		}
		items[len(items)-1] += count
		var lines []string
		for i, item := range items {
			if i > 0 {
				lead = strings.Repeat(" ", indent)
			}
			lines = append(lines, terminalui.Hang(lead, item, width)...)
		}
		return lines
	case "op":
		if len(block.Questions) > 0 {
			return p.questionRows(block, width)
		}
		if block.VCS() {
			return p.vcsRow(block, width)
		}
		if block.Verb == "Run" {
			return p.ranRow(block, width)
		}
		label := p.Label(block.Verb, block.Label) + ResultCount(block.Results, block.Verb)
		copyLabel := ""
		if block.Verb == "Search" && block.Path == "" && block.Code == "" {
			compact := p.compactSearchTargets(block.Label) + ResultCount(block.Results, block.Verb)
			if compact != label && ansi.StringWidth(block.lead(VerbColor(block.Verb), block.Verb)+compact) <= width {
				copyLabel = label
				label = compact
			}
		}
		if block.Path != "" {
			path := terminalui.StyledPath(textsanitize.Safe(block.Path, false))
			if label != "" {
				path += terminalui.Dim + " · " + terminalui.Undim + label
			}
			label = path
		}
		if row, ok := p.editRow(block, width, block.cell(block.Verb)); ok {
			label = row
		}
		code, body := block.Code, block.Body
		omittedSource := 0
		if !block.Expanded && block.TemporarySource() {
			omittedSource, code = strings.Count(code, "\n")+1, ""
		}
		exit := ""
		if block.ExitCode != 0 && (block.Verb == "Run" || block.Verb == "Skill" || block.Verb == "Capture" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.Verb)) {
			exit = terminalui.Red + fmt.Sprintf("(exit %d)", block.ExitCode) + terminalui.Reset
		}
		// Keep exec source below its long heading, regardless of source line count.
		// Other single-line programs or arguments read best on the operation row.
		if code != "" && !strings.Contains(code, "\n") && (label == "" || !block.Fenced) && !(block.Fenced && block.Verb == "Run JavaScript") {
			inline := p.code(block.Verb, code)
			if block.Fenced {
				inline = strings.Join(p.Highlight(block.Lang, code), " ")
			}
			label, code = strings.TrimSpace(label+" "+inline), ""
		}
		lines := terminalui.Hang(block.lead(VerbColor(block.Verb), block.Verb), label, width)
		if copyLabel != "" {
			lines[0] = p.copyPathGroup(lines[0], block.lead(VerbColor(block.Verb), block.Verb)+copyLabel)
		}
		if omittedSource > 0 {
			lines = append(lines, "  "+Elision{Hidden: omittedSource, Hovered: block.Hovered}.String())
		}
		if code != "" {
			lang := block.Lang
			if !block.Fenced && (block.Verb == "MCP" || strings.HasPrefix(block.Verb, "Tool")) {
				lang = "json"
			}
			lines = append(lines, liveActivityIndent(p.program(lang, sourceTabs(code), width-2, block), "  ")...)
		}
		if body != "" {
			lines = append(lines, liveActivityIndent(p.Markdown(body, width-2, false), "  ")...)
		}
		if exit != "" {
			last := len(lines) - 1
			if ansi.StringWidth(lines[last])+1+ansi.StringWidth(exit) <= width {
				lines[last] += " " + exit
			} else {
				lines = append(lines, strings.Repeat(" ", block.cell(block.Verb))+exit)
			}
		}
		return lines
	case "message":
		lines := []string{ansi.Truncate(p.messageDirection(block), width, "…")}
		return append(lines, p.Markdown(block.Body, width, false)...)
	case "start":
		head := terminalui.Green + "\x1b[1m▶ Started" + terminalui.Reset
		lines := terminalui.Hang(head+" · ", p.Inline(block.Label), width)
		if block.Body != "" {
			lines = append(lines, p.Markdown(block.Body, width, false)...)
		}
		return lines
	case "compaction":
		label := "◉ Context compacted"
		if block.Hovered {
			label = Underline(label)
		}
		return []string{terminalui.Amber + label + terminalui.Reset}
	case "batch":
		return p.batchRow(block, width)
	case "filter":
		return filterRows(block.Body, width, min(block.cell("Run"), width/2))
	case "error":
		return liveActivityIndent(ErrorRows(block, width-2), terminalui.Red+"✗"+terminalui.Reset+" ")
	}
	return p.Markdown(block.Body, width, false)
}

// filterRows aligns an auxiliary note with its command text.
func filterRows(body string, width, indent int) []string {
	rows := terminalui.Wrap(body, width-indent, true)
	for i := range rows {
		rows[i] = strings.Repeat(" ", indent) + terminalui.Dim + rows[i] + terminalui.Reset
	}
	return rows
}

func exitText(code int) string {
	return terminalui.Red + fmt.Sprintf("exit %d", code) + terminalui.Reset
}

// editGroupRow lays out one file row of an edit group. Rows share one verb
// cell: the first row names the group's verb, source, and any unconfirmed
// outcome, and later rows name only a verb that differs from Edit.
func (p *Painter) editGroupRow(block Block, width int) []string {
	if block.HiddenEdits > 0 {
		unit := "files"
		if block.HiddenEdits == 1 {
			unit = "file"
		}
		return terminalui.Hang(strings.Repeat(" ", max(block.cell("Edit"), block.VerbAlign+1)),
			Elision{Hidden: block.HiddenEdits, Unit: unit, Hovered: block.Hovered}.String(), width)
	}
	verb, status, start := EditVerb(block), EditStatus(block), block.GroupStart
	color := terminalui.Green
	switch status {
	case "failed", "declined":
		color = terminalui.Red
	case "requested", "pending":
		color = terminalui.Amber
	}
	cell := max(block.cell("Edit"), block.VerbAlign+1)
	lead := strings.Repeat(" ", cell)
	if start || block.Verb != "Edit" {
		lead = color + "\x1b[1m" + verb + terminalui.Reset + strings.Repeat(" ", cell-ansi.StringWidth(verb))
	}
	block.Label = block.EditLabel()
	if status != "" {
		// The first row names the outcome that every row's tail repeats.
		block.Label = strings.TrimSuffix(block.Label, " · "+status)
	}
	row, ok := p.editRow(block, width, cell)
	if !ok {
		row = p.Label(block.Verb, block.Label)
	}
	var trailer []string
	if start && !block.DirectoryDeletion() {
		trailer = p.groupSource(block)
		if status != "" {
			trailer = append(trailer, terminalui.Dim+"· "+terminalui.Undim+color+status+terminalui.Reset)
		}
	}
	if block.ExitCode != 0 {
		trailer = append(trailer, terminalui.Dim+"· "+terminalui.Undim+exitText(block.ExitCode))
	}
	rows := terminalui.Hang(lead, row, width)
	// The trailer follows the file when it fits, and otherwise takes its own row.
	if last, text := len(rows)-1, strings.Join(trailer, " "); text == "" || ansi.StringWidth(rows[last])+1+ansi.StringWidth(text) <= width {
		rows[last] = strings.TrimSuffix(rows[last]+" "+text, " ")
	} else {
		rows = append(rows, terminalui.Hang(strings.Repeat(" ", cell), text, width)...)
	}
	return rows
}

// groupSource names edit sources other than stock apply_patch, each
// highlighted as its own command, and the invocations a group spans.
func (p *Painter) groupSource(block Block) []string {
	var parts []string
	if source := strings.TrimSuffix(block.EditSource, presentation.RequestedEditSuffix); source != "" && source != "apply_patch" {
		var sources []string
		for part := range strings.SplitSeq(source, ", ") {
			sources = append(sources, strings.Join(p.Highlight("bash", part), " ")+terminalui.Reset)
		}
		parts = append(parts, terminalui.Dim+"via "+terminalui.Undim+strings.Join(sources, terminalui.Dim+", "+terminalui.Undim))
	}
	if block.GroupCount > 1 {
		parts = append(parts, terminalui.Dim+fmt.Sprintf("×%d", block.GroupCount)+terminalui.Undim)
	}
	return parts
}

// editRow lays out a file row with its counts in the group's shared column.
// Zero counts are omitted.
func (p *Painter) editRow(block Block, width, lead int) (string, bool) {
	if !slices.Contains([]string{"Create", "Edit", "Delete"}, block.Verb) {
		return "", false
	}
	path, added, removed, tail, ok := EditStat(block.Label)
	if !ok {
		return "", false
	}
	plain := editCounts(added, removed)
	stats := countText(plain)
	room := width - lead - ansi.StringWidth(tail)
	if plain != "" {
		room -= ansi.StringWidth(plain) + 1
	}
	// Every row of a group decides its count column alike: the widest path,
	// narrowed so the group's widest counts still fit, eliding longer paths.
	column := min(block.PathAlign, width-lead-2-block.StatAlign)
	if plain == "" || column < min(16, block.PathAlign) {
		column = 0
	}
	if column > 0 {
		room = min(room, column)
	}
	path = fitPath(path, room)
	row := terminalui.StyledPath(path)
	if plain != "" {
		pad := 1
		if column > 0 && ansi.StringWidth(path) <= column {
			pad = column - ansi.StringWidth(path) + 2
		}
		row += strings.Repeat(" ", pad) + stats
		if bar := statBar(added, removed, block.StatScale); bar != "" {
			if gap := max(1, block.StatAlign-ansi.StringWidth(plain)+1); lead+ansi.StringWidth(row)+gap+statBarCells <= width {
				row += strings.Repeat(" ", gap) + bar
			}
		}
	}
	if tail != "" {
		row += p.Label(block.Verb, tail)
	}
	return row, true
}

const statBarCells = 8

// statBar scales a row's changed lines against the largest row in its group.
func statBar(added, removed, scale int) string {
	total := added + removed
	if scale <= 0 || total <= 0 {
		return ""
	}
	filled := min(statBarCells, max(1, (total*statBarCells+scale/2)/scale))
	green := (added*filled + total/2) / total
	if added > 0 {
		green = max(1, green)
	}
	if removed > 0 && filled > 1 {
		green = min(green, filled-1)
	}
	return terminalui.Green + strings.Repeat("━", green) + terminalui.Red + strings.Repeat("━", filled-green) + "\x1b[39m" +
		terminalui.Dim + strings.Repeat("━", statBarCells-filled) + terminalui.Undim
}

// fitPath shortens a path wider than width from its start, dropping whole
// leading directories so its nearest directories and file name remain.
func fitPath(path string, width int) string {
	// Below this, eliding leaves too little of the name; rows wrap instead.
	if width < 16 {
		return path
	}
	return terminalui.TruncatePath(path, width)
}

// readDetails uses observed per-range counts only when they account for the
// target's entire count. Combined output from multiple ranges stays unattributed.
func readDetails(read activitymodel.Read, hovered bool) string {
	total := 0
	for _, n := range read.RangeLines {
		total += n
	}
	if total > 0 && total == read.Lines && len(read.RangeLines) == len(read.Ranges) {
		parts := make([]string, len(read.Ranges))
		for i, span := range read.Ranges {
			parts[i] = terminalui.Dim + lineRanges([]string{span}) + terminalui.Undim + lineSuffix(read.RangeLines[i], hovered)
		}
		return strings.Join(parts, terminalui.Dim+", "+terminalui.Undim)
	}
	return terminalui.Dim + lineRanges(read.Ranges) + terminalui.Undim + lineSuffix(read.Lines, hovered)
}

// lineRanges shows read spans as line numbers: 25:46 reads as L25–46.
func lineRanges(ranges []string) string {
	shown := make([]string, len(ranges))
	for i, span := range ranges {
		from, to, ok := strings.Cut(span, ":")
		switch {
		case !ok:
			shown[i] = span
		case from == to:
			shown[i] = "L" + from
		default:
			shown[i] = "L" + from + "–" + to
		}
	}
	return strings.Join(shown, ", ")
}

// ranRow lays out one command after Running or Ran: a one-line command beside
// the verb, or a program beside its gutter, then any failure's exit and the
// live or failed output tail.
func (p *Painter) ranRow(block Block, width int) []string {
	code, label := sourceTabs(block.Code), strings.TrimSpace(p.Label(block.Verb, block.Label)+ResultCount(block.Results, block.Verb))
	if code == "" {
		if span, end, ok := markdownsyntax.CodeSpan(block.Label, 0); ok && end == len(block.Label) {
			code, label = span, ""
		}
	}
	if block.ShowWorkdir && block.Workdir != "" {
		label = strings.TrimSpace(label + " " + workdirLabel(block.Workdir))
	}
	color, exit := block.runColor(), ""
	if block.ExitCode != 0 {
		color, exit = terminalui.Red, terminalui.Dim+"· "+terminalui.Undim+exitText(block.ExitCode)
	}
	verb := RowVerb(block)
	lead, indent := block.lead(color, verb), block.cell(verb)
	padding := strings.Repeat(" ", indent)
	// suffix follows the last row when it fits, and otherwise takes its own.
	suffix := func(lines []string, text string) []string {
		if last := len(lines) - 1; ansi.StringWidth(lines[last])+1+ansi.StringWidth(text) <= width {
			lines[last] += " " + text
			return lines
		}
		return append(lines, terminalui.Hang(padding, text, width)...)
	}
	var lines []string
	switch {
	case code != "" && !strings.Contains(code, "\n"):
		lang := "bash"
		if block.Fenced && block.Lang != "" {
			lang = block.Lang
		}
		lines = p.command(lead, lang, code, width, block)
		if label != "" {
			lines = suffix(lines, label)
		}
	case code != "":
		program := liveActivityIndent(p.program(block.Lang, code, width-indent, block), padding)
		if label != "" {
			lines = terminalui.Hang(lead, label, width)
		} else {
			program[0] = lead + strings.TrimPrefix(program[0], padding)
		}
		lines = append(lines, program...)
	default:
		lines = terminalui.Hang(lead, label, width)
	}
	switch {
	case exit != "" && strings.Contains(code, "\n"):
		// Beside a program's last row, the exit would read as source.
		lines = append(lines, padding+exit)
	case exit != "":
		lines = suffix(lines, exit)
	}
	if elapsed := RunElapsed(block, p.now()); elapsed != "" {
		if strings.Contains(code, "\n") {
			lines = append(lines, padding+terminalui.Dim+"· "+elapsed+terminalui.Undim)
		} else {
			lines = suffix(lines, terminalui.Dim+"· "+elapsed+terminalui.Undim)
		}
	}
	if block.Body != "" {
		lines = append(lines, liveActivityIndent(p.Markdown(block.Body, width-indent-2, false), padding+"  ")...)
	}
	return lines
}

// sourceTabs expands tabs as textsanitize.Safe does; a raw tab has no cell width,
// so wrapping would misjudge its row.
func sourceTabs(code string) string {
	return strings.ReplaceAll(code, "\t", "    ")
}

// clipSource keeps a command or program preview within block.SourceRows,
// counting wrapped rows, so a one-line command wider than many rows is bounded
// too. The last kept row counts the rows left out, under lead.
func clipSource(rows []string, block Block, lead string) []string {
	if block.SourceRows <= 0 || len(rows) <= block.SourceRows {
		return rows
	}
	keep := max(1, block.SourceRows-1)
	return append(rows[:keep:keep], lead+Elision{Hidden: len(rows) - keep, Hovered: block.Hovered}.String())
}

// readLines counts collapsed read output after the target it came from: a
// file's content needs no row of its own to say how much there is.
func readLines(block Block) string {
	if !block.Collapsed || !block.ReadOutput() || len(block.Tail) == 0 {
		return ""
	}
	return lineSuffix(block.TailOmitted+len(block.Tail), block.Hovered)
}

// lineSuffix counts n lines of collapsed content, underlined under the
// pointer that opens it.
func lineSuffix(n int, hovered bool) string {
	if n <= 0 {
		return ""
	}
	return " " + Elision{Hidden: n, Form: ElisionContent, Hovered: hovered}.String()
}

// outputRows attaches the invocation's output to its final operation. Open
// output counts its earlier lines in the verb column of its first row.
func outputRows(block Block, lines []string, width int) []string {
	indent := block.cell(RowVerb(block))
	padding := strings.Repeat(" ", indent)
	// A VCS row's changes are its result, not output to collapse.
	if len(block.Changes) > 0 && (!block.Collapsed || block.VCS()) {
		return append(lines, changeRows(block.Changes, padding, width-indent)...)
	}
	tail, omitted := block.Tail, block.TailOmitted
	if block.TailRows > 0 && len(tail) > block.TailRows {
		omitted += len(tail) - block.TailRows
		tail = tail[len(tail)-block.TailRows:]
	}
	if suffix := readLines(block); suffix != "" && len(lines) > 0 {
		last := &lines[len(lines)-1]
		if block.Kind == "reads" {
			return lines // Its layout placed the count.
		}
		if ansi.StringWidth(*last)+ansi.StringWidth(suffix) <= width {
			*last += suffix
			return lines
		}
	}
	if block.Collapsed && !block.Running && !block.Skipped && block.ExitCode == 0 && (block.Exited || block.Results != nil) && slices.Contains([]string{"Search", "List", "Inspect"}, block.Verb) {
		return lines
	}
	// Output uses a dashed gutter, distinct from the program gutter above it.
	if block.Collapsed && len(tail) > 0 {
		hint := Elision{Hidden: omitted + len(tail), Hovered: block.Hovered}
		return append(lines, padding+ansi.Truncate(terminalui.Dim+"┆ "+terminalui.Undim+hint.String(), width-indent, "…"))
	}
	for i, line := range tail {
		lead := padding
		if count := (Elision{Hidden: omitted, Form: ElisionLead, Hovered: block.Hovered}); i == 0 && omitted > 0 {
			if cells := ansi.StringWidth(count.Text()); cells < indent {
				lead = count.String() + padding[cells:]
			} else {
				// A count wider than the verb column keeps its own row.
				count.Form = ElisionEarlier
				lines = append(lines, padding+ansi.Truncate(terminalui.Dim+"┆ "+terminalui.Undim+count.String(), width-indent, "…"))
			}
		}
		lines = append(lines, lead+ansi.Truncate(terminalui.Dim+"┆"+terminalui.Undim+" "+line, width-indent, "…"))
	}
	return lines
}

// command places a one-line command after lead. A statement after a
// separator starts a row under the first; other continuations sit two
// columns deeper.
func (p *Painter) command(lead, lang, code string, width int, block Block) []string {
	styled := strings.Join(p.Highlight(lang, code), " ")
	indent := ansi.StringWidth(lead)
	padding := strings.Repeat(" ", indent)
	if indent > width/2 || ansi.Strip(styled) != code {
		return clipSource(terminalui.Hang(lead, styled, width), block, padding)
	}
	var lines []string
	wrapped, hidden := shellWrap(styled, code, width-indent, lang == "bash", block.SourceRows)
	for i, row := range wrapped {
		switch {
		case i == 0:
			lines = append(lines, lead+row.text)
		case row.deeper:
			lines = append(lines, strings.Repeat(" ", indent+2)+row.text)
		default:
			lines = append(lines, strings.Repeat(" ", indent)+row.text)
		}
	}
	if hidden > 0 {
		keep := max(1, block.SourceRows-1)
		hidden += len(lines) - keep
		lines = append(lines[:keep:keep], padding+Elision{Hidden: hidden, Hovered: block.Hovered}.String())
	}
	return lines
}

type wrapRow struct {
	text   string
	deeper bool // Continues a statement rather than starting one.
}

// shellWrap breaks a styled one-line command at unquoted blanks of its plain
// text. When the command does not fit, each top-level statement after ;, &&,
// or || starts a row. Other breaks inside a statement end with a line
// continuation, or follow a pipe, so each row reads as the same command; a
// word wider than a row is cut without one. A positive limit materializes only
// that many rows, returning the exact count of the rest without ANSI slicing.
func shellWrap(styled, plain string, width int, shell bool, limit int) ([]wrapRow, int) {
	type blank struct {
		from, to  int  // Cell offsets of a blank run.
		statement bool // Follows a top-level statement separator.
		pipe      bool
	}
	var blanks []blank
	quote, escaped := rune(0), false
	cells, depth := 0, 0
	var previous [2]rune // The two runes before the current one.
	for _, r := range plain {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '(':
			depth++
		case r == ')':
			depth = max(0, depth-1)
		case r == ' ' || r == '\t':
			if n := len(blanks); n > 0 && blanks[n-1].to == cells {
				blanks[n-1].to++
				break
			}
			last, before := previous[1], previous[0]
			top := depth == 0 && before != '\\'
			blanks = append(blanks, blank{from: cells, to: cells + 1,
				statement: top && (last == ';' || last == '&' && before == '&' || last == '|' && before == '|'),
				pipe:      top && last == '|' && before != '|'})
		}
		previous[0], previous[1] = previous[1], r
		cells += ansi.StringWidth(string(r))
	}
	continuation := 0
	if shell {
		continuation = 2
	}
	total := ansi.StringWidth(plain)
	if total <= width {
		return []wrapRow{{styled, false}}, 0
	}
	var rows []wrapRow
	hidden := 0
	add := func(start, end int, deeper, continuation bool) {
		if limit > 0 && len(rows) >= max(1, limit) {
			hidden++
			return
		}
		text := ansi.Cut(styled, start, end) + terminalui.Reset
		if continuation {
			text += terminalui.Dim + " \\" + terminalui.Undim
		}
		rows = append(rows, wrapRow{text, deeper})
	}
	start, firstBlank, deeper := 0, 0, false
	for {
		avail := max(1, width)
		if deeper {
			avail = max(1, width-2)
		}
		for firstBlank < len(blanks) && blanks[firstBlank].from <= start {
			firstBlank++
		}
		chosen := -1
		for i := firstBlank; i < len(blanks); i++ {
			b := blanks[i]
			if b.from-start > avail {
				break
			}
			if b.statement {
				chosen = i
				break
			}
		}
		if chosen < 0 && total-start <= avail {
			add(start, total, deeper, false)
			return rows, hidden
		}
		if chosen < 0 {
			for i := firstBlank; i < len(blanks); i++ {
				b := blanks[i]
				if b.from-start > avail {
					break
				}
				cost := continuation
				if b.pipe || b.statement {
					cost = 0
				}
				if b.from-start+cost <= avail {
					chosen = i
				}
			}
		}
		if chosen < 0 {
			end := start + avail
			add(start, end, deeper, false)
			start, deeper = end, true
			continue
		}
		b := blanks[chosen]
		add(start, b.from, deeper, !b.pipe && !b.statement && shell)
		start, deeper = b.to, !b.statement
	}
}

// event renders a block for the native Activity log: a short label row, then
// the body flush with the gutter so narrow panes keep their full width.
// Operations keep their ordinary one-row-per-operation layout.
func (p *Painter) Event(block Block, width int) []string {
	width = max(8, width)
	// A flashed entry highlights its text, never its label row.
	label := func(head string, body []string) []string {
		return append([]string{ansi.Truncate(head, width, "…")}, p.Flash(block, body)...)
	}
	done := terminalui.Green + "✓" + terminalui.Reset + terminalui.Dim + " answer" + terminalui.Undim
	switch block.Kind {
	case "message":
		return label(p.Route(block), p.Markdown(block.Body, width, false))
	case "start":
		head := terminalui.Green + "▶" + terminalui.Reset + terminalui.Dim + " started"
		if model := strings.TrimSpace(ansi.Strip(p.Inline(block.Label))); model != "" {
			head += " · " + model
		}
		return label(head+terminalui.Undim, p.Markdown(block.Body, width, false))
	case "final":
		// The answer is a card, so it stands apart from the work before it.
		inner := width - 4
		if inner < 8 {
			inner = width
		}
		title := done
		rows := p.Markdown(block.Body, inner, false)
		if width < 12 {
			return label(title, rows)
		}
		return liveActivityCard(title, p.Flash(block, rows), width, block.Flash)
	case "text":
		return p.Flash(block, p.Markdown(block.Body, width, false))
	}
	return p.Flash(block, p.Block(block, width))
}

// Flash highlights the text of a flashed block's rows, leaving padding and
// frames plain.
func (p *Painter) Flash(block Block, rows []string) []string {
	if !block.Flash {
		return rows
	}
	fill := p.Theme.SelectionBackground()
	flashed := make([]string, len(rows))
	for i, row := range rows {
		flashed[i] = fill + strings.ReplaceAll(row, terminalui.Reset, terminalui.Reset+fill) + "\x1b[49m"
	}
	return flashed
}

func Verb(verb string) string {
	if verb == "" {
		return ""
	}
	return VerbColor(verb) + "\x1b[1m" + verb + terminalui.Reset + strings.Repeat(" ", max(1, 7-ansi.StringWidth(verb)))
}

// cell is the width of a row's verb column: the column its adjacent
// operations share, or Verb's default column.
func (b Block) cell(verb string) int {
	if b.VerbColumn > 0 {
		return max(b.VerbColumn, ansi.StringWidth(verb)+1)
	}
	return ansi.StringWidth(Verb(verb))
}

// lead is a row's bold verb in color, padded to its column.
func (b Block) lead(color, verb string) string {
	if b.GroupHeader != "" && !b.EditOperation() && !b.GroupStart {
		return strings.Repeat(" ", b.cell(verb))
	}
	return color + "\x1b[1m" + verb + terminalui.Reset + strings.Repeat(" ", b.cell(verb)-ansi.StringWidth(verb))
}

func SummaryVerb(verb string) string {
	return VerbColor(verb) + verb + terminalui.Reset + " "
}

// Summary is the roster's one-line view of an agent's current activity.
// Width is the status column's available display width, without a bullet prefix.
func (p *Painter) Summary(blocks []Block, width int) string {
	for len(blocks) > 0 && blocks[len(blocks)-1].Kind == "filter" {
		blocks = blocks[:len(blocks)-1]
	}
	if len(blocks) == 0 {
		return ""
	}
	block := blocks[len(blocks)-1]
	more := ""
	if len(blocks) > 1 {
		more = terminalui.Dim + " · " + terminalui.Undim + Elision{Hidden: len(blocks) - 1, Unit: "more", Form: ElisionSuffix}.String()
	}

	switch block.Kind {
	case "progress":
		style := p.statusStyle(block.Live)
		return style + block.progressText(style) + terminalui.Reset
	case "summary":
		var headers []string
		for i := len(blocks) - 1; i >= 0 && blocks[i].Kind == "summary"; i-- {
			// Share the transcript's collapsed title, not its latest body line.
			// Roster timers already supply elapsed time separately.
			if label := blocks[i].Label; label != "" {
				headers = append(headers, ansi.Strip(p.Inline(label)))
				continue
			}
			section := blocks[i]
			section.Collapsed, section.Thought, section.Hovered = true, 0, false
			if rows := p.blockRows(section, max(1, width)+2); len(rows) > 0 {
				headers = append(headers, strings.TrimPrefix(ansi.Strip(rows[0]), "• "))
			}
		}
		slices.Reverse(headers)
		return p.statusStyle(block.Live) + strings.Join(headers, ", ") + terminalui.Reset
	case "final":
		text := p.MarkdownPreview(block.Body)
		// The roster's status glyph already marks a sent final answer.
		return text
	case "reads":
		var names []string
		for _, read := range block.Reads {
			name := read.Path
			if block.Verb != "Search" && block.Verb != "Web Search" {
				name = name[strings.LastIndex(name, "/")+1:]
			}
			names = append(names, name)
		}
		return SummaryVerb(block.Verb) + strings.Join(names, ", ") + more
	case "op":
		if block.VCS() {
			return p.vcsSummary(block) + more
		}
		detail := strings.TrimSpace(p.Label(block.Verb, block.Label))
		if detail == "" || strings.HasPrefix(ansi.Strip(detail), "·") {
			code, _, _ := strings.Cut(block.Code, "\n")
			if block.Fenced {
				code = strings.Join(p.Highlight(block.Lang, code), " ")
			} else {
				code = p.code(block.Verb, code)
			}
			detail = strings.TrimSpace(code + " " + detail)
		}
		if block.ExitCode != 0 && (block.Verb == "Run" || block.Verb == "Skill" || block.Verb == "Capture" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.Verb)) {
			detail += " " + terminalui.Red + fmt.Sprintf("(exit %d)", block.ExitCode) + terminalui.Reset
		}
		verb := block.Verb
		if block.Running {
			verb = "Running"
		}
		if block.Hook != nil {
			return block.runColor() + RowVerb(block) + terminalui.Reset + " " + detail + more
		}
		return SummaryVerb(verb) + detail + more
	case "message":
		text := p.MarkdownPreview(block.Body)
		if text == "" {
			text = strings.TrimSuffix(block.Verb, ":")
		}
		return p.messageDirection(block) + " " + text
	case "start":
		return terminalui.Green + "▶ Started" + terminalui.Reset + " " + p.Inline(block.Label)
	case "compaction":
		return terminalui.Amber + "◉ Context compacted" + terminalui.Reset
	case "error":
		return terminalui.Red + "✗ " + ErrorPreview(block.Body) + terminalui.Reset
	}
	return p.MarkdownPreview(block.Body)
}

// Route names both ends of a message, sender first.
func (p Painter) Route(block Block) string {
	return p.recipient(block.From) + terminalui.Dim + " → " + terminalui.Undim + p.recipient(block.To)
}

// liveActivityCard frames rows under a titled top edge. A glowing card was
// just opened from another pane.
func liveActivityCard(title string, rows []string, width int, glow bool) []string {
	return Card(title, "", rows, width, glow)
}

// Card frames a finished answer: a rounded green edge with its title on the
// top border and optional dim right text, such as a time.
func Card(title, right string, rows []string, width int, glow bool) []string {
	edge := "\x1b[38;2;80;120;90m"
	if glow {
		edge = "\x1b[1m" + terminalui.Green
	}
	inner := width - 4
	title = ansi.Truncate(title, max(0, width-6), "…")
	if right != "" && width-7-ansi.StringWidth(title)-ansi.StringWidth(right) >= 2 {
		right = " " + terminalui.Reset + terminalui.Dim + right + terminalui.Undim + edge + " "
	} else {
		right = ""
	}
	top := edge + "╭─ " + terminalui.Reset + title + edge + " " + strings.Repeat("─", max(0, width-5-ansi.StringWidth(title)-ansi.StringWidth(right))) + right + "╮" + terminalui.Reset
	lines := []string{ansi.Truncate(top, width, "")}
	for _, row := range rows {
		row = ansi.Truncate(row, inner, "…")
		lines = append(lines, edge+"│"+terminalui.Reset+" "+row+strings.Repeat(" ", max(0, inner-ansi.StringWidth(row)))+" "+edge+"│"+terminalui.Reset)
	}
	return append(lines, edge+"╰"+strings.Repeat("─", max(0, width-2))+"╯"+terminalui.Reset)
}

// Tree joins operation rows into one tree: ├ before each but the
// last, └ before the last, and a rail beside continuation rows.
func Tree(parts [][]string) []string {
	var lines []string
	for i, part := range parts {
		lead, rail := "├ ", "│ "
		if i == len(parts)-1 {
			lead, rail = "└ ", "  "
		}
		for k, line := range part {
			if k == 0 {
				lines = append(lines, terminalui.Dim+lead+terminalui.Undim+line)
			} else {
				lines = append(lines, terminalui.Dim+rail+terminalui.Undim+line)
			}
		}
	}
	return lines
}

// messageDirection is relative to the row's owner, not the transport recipient.
func (p Painter) messageDirection(block Block) string {
	direction, peer := "→ ", block.To
	if block.Owner == block.To {
		direction, peer = "← ", block.From
	}
	return terminalui.Dim + direction + terminalui.Undim + p.recipient(peer)
}

func ResultCount(count *int, verb string) string {
	if count == nil {
		return ""
	}
	unit := "results"
	if verb == "Inspect" {
		unit = "entries"
	}
	return terminalui.Dim + fmt.Sprintf(" (%d %s)", *count, unit) + terminalui.Undim
}

func runDuration(block Block, now time.Time) time.Duration {
	if block.Running && !block.Started.IsZero() {
		return now.Sub(block.Started)
	}
	return block.Duration
}

// RunElapsed shares the compact duration grammar used by native status timers.
func RunElapsed(block Block, now time.Time) string {
	elapsed := runDuration(block, now)
	if elapsed <= 3*time.Millisecond {
		return ""
	}
	if block.Running {
		return FormatLiveDuration(elapsed)
	}
	return FormatDuration(elapsed)
}

// FormatLiveDuration hides live elapsed counters until their first whole second.
func FormatLiveDuration(elapsed time.Duration) string {
	if elapsed < time.Second {
		return ""
	}
	return FormatDuration(elapsed)
}

func formatTimeout(value string) string {
	original := value
	if value == "" {
		return value
	}
	if before, ok := strings.CutSuffix(value, "d"); ok {
		value = before + "h"
		if duration, err := time.ParseDuration(value); err == nil && duration <= time.Duration(1<<63-1)/24 {
			return FormatDuration(duration * 24)
		}
	} else {
		if last := value[len(value)-1]; last >= '0' && last <= '9' || last == '.' {
			value += "s"
		}
		if duration, err := time.ParseDuration(value); err == nil {
			return FormatDuration(duration)
		}
	}
	return original
}

// FormatDuration uses milliseconds below one second and whole seconds thereafter.
// Trailing zero units are omitted. Negative durations display as zero.
func FormatDuration(elapsed time.Duration) string {
	elapsed = max(0, elapsed)
	if elapsed == 0 {
		return "0s"
	}
	var text string
	if elapsed < time.Second {
		text = elapsed.Truncate(time.Millisecond).String()
	} else {
		elapsed = elapsed.Truncate(time.Second)
		text = elapsed.String()
		if elapsed >= time.Minute && elapsed%time.Minute == 0 {
			text = strings.TrimSuffix(text, "0s")
			if elapsed%time.Hour == 0 {
				text = strings.TrimSuffix(text, "0m")
			}
		}
	}
	return text
}
