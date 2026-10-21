package terminalview

import (
	"cmp"
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/textsanitize"

	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/libraries/terminalui"

	"github.com/charmbracelet/x/ansi"
)

// dialogHighlightBytes bounds the content the dialog colors; larger content
// shows plain rather than stalling a frame.
const dialogHighlightBytes = 256 << 10

// DialogPage lays out a block's whole source and retained output. Source keeps
// the code gutter and output the dashed one, as in the feed; a read's content
// is source, numbered from the first line it read. Markdown bodies wrap to
// width; numbered lines wrap as they are shown.
func (p *Painter) DialogPage(block Block, width int) terminalui.DialogPage {
	width = max(8, width)
	view := activitymodel.OutputView{Lines: block.Tail, Dropped: block.TailOmitted, Done: !block.Running, Exited: block.Exited, Exit: block.ExitCode}
	if block.Hook != nil {
		view.Exited = false
	}
	if block.OutputSnapshot != nil {
		view = *block.OutputSnapshot
		if view.Released {
			view.Lines, view.Dropped = block.Tail, block.TailOmitted
		}
		block.Running = !view.Done
	}
	page := terminalui.DialogPage{Title: p.DialogPageTitle(block, p.now(), width), Live: block.Live || !view.Done}
	var detail []string
	if block.Hook != nil {
		detail = append(detail, textsanitize.Safe(block.Hook.HandlerType, false))
		if block.Hook.ExecutionMode == "async" {
			detail = append(detail, "async")
		}
	}
	if len(block.Reads) == 1 && len(block.Reads[0].Ranges) > 0 {
		detail = append(detail, lineRanges(block.Reads[0].Ranges))
	}
	if total := view.Dropped + len(view.Lines); total > 0 {
		detail = append(detail, lineCount(total))
	}
	switch {
	case view.Exited && view.Exit != 0:
		detail = append(detail, terminalui.Red+"exit "+strconv.Itoa(view.Exit)+terminalui.Reset+terminalui.Dim)
	case view.Exited && block.Verb == "Run":
		detail = append(detail, "exit 0")
	}
	page.Detail = terminalui.Dim + strings.Join(detail, " · ") + terminalui.Undim
	if len(detail) == 0 && block.DetailLines > 0 {
		page.Detail = terminalui.Dim + lineCount(block.DetailLines) + terminalui.Undim
	}
	if block.Skipped {
		page.Detail = terminalui.Dim + "skipped" + terminalui.Undim
	}
	add := page.Add
	if block.Approval != "" {
		label := approvalLabel(block.Approval)
		if label != "" {
			add(terminalui.DialogLine{Text: terminalui.Dim + "Approval: " + terminalui.Undim + label})
		}
		if ansi.Strip(label) != block.Approval {
			add(terminalui.DialogLine{Text: terminalui.Dim + textsanitize.Safe(block.Approval, false) + terminalui.Undim, Wrap: true})
		}
	}
	gap := func() {
		if len(page.Lines) > 0 {
			add(terminalui.DialogLine{})
		}
	}

	code := textsanitize.Safe(block.Code, false)
	page.Text = code
	if code != "" && (block.Path != "" || strings.Contains(code, "\n") || ansi.StringWidth(code) > width/2) {
		colored := strings.Split(code, "\n")
		if len(code) <= dialogHighlightBytes {
			colored = p.dialogHighlight(block, code)
		}
		for i, line := range colored {
			add(terminalui.DialogLine{Number: i + 1, Gutter: "│", Text: line})
		}
		page.Text = code
	}
	if block.Body != "" {
		page.Text = block.Body
		if block.Kind == "error" {
			page.Text = textsanitize.Safe(block.Body, false)
		}
		gap()
		if block.Kind == "error" {
			for line := range strings.SplitSeq(page.Text, "\n") {
				add(terminalui.DialogLine{Text: terminalui.Red + line + terminalui.Reset, Wrap: true})
			}
		} else {
			var rows []string
			if len(block.RequestedOperations) > 0 {
				operations := make([]Block, len(block.RequestedOperations))
				for i, value := range block.RequestedOperations {
					operations[i] = Block{Value: value}
				}
				for _, operation := range GroupOperations(operations) {
					rows = append(rows, p.Block(operation, width)...)
				}
				rows = append(rows, "")
			}
			rows = append(rows, p.Markdown(block.Body, width, block.Kind == "summary")...)
			for _, row := range rows {
				add(terminalui.DialogLine{Text: row})
			}
		}
	}
	var notes []string
	switch {
	case view.Released:
		notes = append(notes, "Full output was released; its last available lines follow.")
	case view.Dropped > 0:
		notes = append(notes, Elision{Hidden: view.Dropped, Form: ElisionEarlier}.Text()+" not retained")
	}
	if len(view.Lines) == 0 {
		switch {
		case !view.Done:
			notes = append(notes, "Waiting for output…")
		case len(page.Lines) == 0:
			notes = append(notes, "No output")
		}
	}
	if len(notes) > 0 || len(view.Lines) > 0 {
		gap()
	}
	for _, note := range notes {
		for _, row := range terminalui.Wrap(terminalui.Dim+note+terminalui.Undim, width, false) {
			add(terminalui.DialogLine{Text: row})
		}
	}
	if len(view.Lines) == 0 {
		return page
	}
	content := strings.Join(view.Lines, "\n")
	page.Text = content
	if block.MarkdownOutput() {
		for _, row := range p.Markdown(content, width, false) {
			add(terminalui.DialogLine{Text: row})
		}
		return page
	}
	colored, first, gutter := p.outputColors(block, view.Lines), view.Dropped+1, "┆"
	if block.ReadOutput() && len(block.Reads) == 1 {
		// A read's output is the file it read.
		gutter = "│"
		if ranges := block.Reads[0].Ranges; len(ranges) == 1 {
			if from, _, ok := strings.Cut(ranges[0], ":"); ok {
				if n, err := strconv.Atoi(from); err == nil && n > 0 {
					first = n + view.Dropped
				}
			}
		}
	}
	var identity uint64
	if p.CopySource && !p.LayoutOnly {
		identity = terminalui.CopyID(content, p.CopyScope)
	}
	for i, line := range colored {
		row := terminalui.DialogLine{Number: first + i, Gutter: gutter, Text: line}
		if p.CopySource && !p.LayoutOnly && ansi.Strip(line) != view.Lines[i] {
			row.CopySource = &terminalui.CopyFragment{ID: terminalui.CopyID(strconv.Itoa(i), identity), Prefix: view.Lines[i]}
		}
		add(row)
	}
	return page
}

// Search rows supply their own file type; never color them as the shell
// program that produced them. Keep prefixes and copied bytes intact.
var dialogSearchRow = regexp.MustCompile(`^(.+?):([0-9]+):(.*)$`)

func (p *Painter) dialogSearchLine(block Block, line string) string {
	parts := dialogSearchRow.FindStringSubmatch(line)
	if parts == nil {
		// rg omits the filename when exactly one file was requested.
		_, target, ok := strings.Cut(block.Label, " in ")
		path, end, valid := markdownsyntax.CodeSpan(target, 0)
		number, content, numbered := strings.Cut(line, ":")
		n, err := strconv.Atoi(number)
		if !ok || !valid || end != len(target) || !numbered || err != nil || n < 1 {
			return line
		}
		if rows, err := p.Syntax.ColorSource(context.Background(), p.Theme, path, content+"\n"); err == nil && len(rows) == 1 {
			content = rows[0]
		}
		return terminalui.Dim + number + ":" + terminalui.Undim + content
	}
	content := parts[3]
	if rows, err := p.Syntax.ColorSource(context.Background(), p.Theme, parts[1], content+"\n"); err == nil && len(rows) == 1 {
		content = rows[0]
	}
	return terminalui.StyledPath(parts[1]) + terminalui.Dim + ":" + parts[2] + ":" + terminalui.Undim + content
}

// dialogLanguage uses the same source language for the title and body.
func dialogLanguage(block Block) string {
	if !block.Fenced && (block.Verb == "MCP" || strings.HasPrefix(block.Verb, "Tool")) {
		return "json"
	}
	if block.Lang == "" && block.Verb == "Run" {
		return "bash"
	}
	return block.Lang
}

func (p *Painter) dialogHighlight(block Block, source string) []string {
	lang := dialogLanguage(block)
	if path := cmp.Or(block.SyntaxPath, block.Path); path != "" && !p.LayoutOnly {
		var rows []string
		var err error
		if strings.EqualFold(lang, "diff") {
			rows, err = p.Syntax.ColorDiffPath(context.Background(), p.Theme, path, source)
		} else {
			rows, err = p.Syntax.ColorSource(context.Background(), p.Theme, path, source+"\n")
		}
		if err == nil && len(rows) > 0 {
			return rows
		}
	}
	if !strings.EqualFold(lang, "diff") {
		source += "\n" // The source renderer consumes one line terminator; diff does not.
	}
	return p.Highlight(lang, source)
}

// dialogTarget styles what the operation acted on.
func dialogTarget(p *Painter, block Block) string {
	switch {
	case block.BatchExit:
		return p.Label(block.Verb, block.Label)
	case block.Kind == "reads":
		var paths []string
		for _, read := range block.Reads {
			paths = append(paths, terminalui.StyledPath(textsanitize.Safe(read.Path, false)))
		}
		return strings.Join(paths, ", ")
	case block.Path != "":
		return terminalui.StyledPath(textsanitize.Safe(block.Path, false))
	case block.Code != "":
		first, _, _ := strings.Cut(textsanitize.Safe(block.Code, false), "\n")
		if len(first) <= dialogHighlightBytes {
			return strings.Join(p.dialogHighlight(block, first), " ")
		}
		return first
	}
	return strings.TrimSpace(p.Label(block.Verb, block.Label))
}

func (p *Painter) DialogPageTitle(block Block, now time.Time, width int) string {
	if block.OutputSnapshot != nil {
		block.Running = !block.OutputSnapshot.Done
	}
	verb := RowVerb(block)
	if verb == "" {
		switch block.Kind {
		case "summary":
			verb, _ = p.thinkingHeader(block, max(1, width-7))
		case "final":
			verb = "Answer"
		case "error":
			verb = "Error"
		default:
			verb = "Message"
		}
	}
	title := VerbColor(block.Verb) + "\x1b[1m" + verb + terminalui.Reset
	if block.Hook != nil {
		title = block.runColor() + "\x1b[1m" + verb + terminalui.Reset
	}
	if target := dialogTarget(p, block); target != "" {
		title += terminalui.Dim + " · " + terminalui.Undim + target
	}
	if outcome := approvalLabel(block.Approval); outcome != "" {
		title += terminalui.Dim + " · " + terminalui.Undim + outcome
	}
	if elapsed := RunElapsed(block, now); elapsed != "" {
		title += terminalui.Dim + " · " + elapsed + terminalui.Undim
	}
	return title
}

// DetailDocument paints an admitted page and optional local-file source metadata.
// Source: apps/mekugi/internal/router/output_dialog.go:453:480@563f0c37.
func (p *Painter) DetailDocument(detail presentation.DetailPage, output *activitymodel.OutputView, file *presentation.DetailFile, width int) terminalui.DialogPage {
	if detail.ModelVisible {
		page := terminalui.DialogPage{Title: "Model-visible", Detail: "Latest Mekugi-prepared content"}
		if detail.Prepared == nil {
			page.Text = "Unavailable: no retained Mekugi-prepared content for this item."
		} else {
			page.Text = detail.Prepared.Text
			if detail.Prepared.Cell {
				page.Detail += " · whole execution cell " + textsanitize.Safe(detail.Prepared.ID, false)
			}
		}
		for line := range strings.SplitSeq(textsanitize.Safe(page.Text, false), "\n") {
			page.Add(terminalui.DialogLine{Text: line, Wrap: true})
		}
		return page
	}
	page := p.DialogPage(Block{Value: detail.Value, OutputSnapshot: output}, width)
	if file == nil {
		return page
	}
	page.Lines = append([]terminalui.DialogLine{{Text: terminalui.StyledPath(textsanitize.Safe(file.Path, false)), Wrap: true}, {}}, page.Lines...)
	if detail.Kind != "error" {
		page.Text = file.Source
		for i := range page.Lines {
			line := &page.Lines[i]
			if line.Number > 0 && line.Number >= file.First && line.Number <= file.Last {
				line.GutterStyle = p.Theme.Accent() + p.Theme.SelectionBackground()
			}
		}
	}
	return page
}
