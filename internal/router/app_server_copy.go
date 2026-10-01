package router

import (
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Source: codex-rs/tui/src/chatwidget/copy_picker.rs:10:105@1cc7e236.
// Reuse the transcript and OSC 52 owner rather than a second clipboard backend.
func (u *appServerUI) showCopyPicker() {
	u.deleteDraftRange(0, len(u.draft))
	var response string
	for _, entry := range slices.Backward(u.view.entries) {
		n := entry.native
		if n == nil || n.thread != u.thread || entry.Agent != "Main" {
			continue
		}
		if entry.Kind == "native_journal" && len(entry.journalItems) > 0 {
			var parts []string
			for _, item := range entry.journalItems {
				parts = append(parts, item.Text)
			}
			response = strings.Join(parts, "\n\n")
		} else if entry.Kind == "text" && (n.phase == "item/completed" || entry.journal != nil) {
			response = entry.Text
		}
		if strings.TrimSpace(response) != "" {
			break
		}
	}
	if strings.TrimSpace(response) == "" {
		u.setNotice("No agent response to copy", true)
		return
	}
	u.cancelPickerScan()
	p := &u.picker
	p.modal, p.open, p.selected, p.top = "copy", true, 0, 0
	p.target, p.problem, p.loading = composerTarget{}, "", false
	p.choices = responseCopyChoices(response)
}

func (u *appServerUI) copyPickerKey(key string) bool {
	p := &u.picker
	switch key {
	case "\x1b[200~", "\x1b[201~":
		return false
	case "\x1b", "\x03":
		p.modal, p.open, p.choices = "", false, nil
	case "\x1b[A", "\x1bOA", "\x10":
		p.selected = (p.selected + len(p.choices) - 1) % len(p.choices)
	case "\x1b[B", "\x1bOB", "\x0e":
		p.selected = (p.selected + 1) % len(p.choices)
	case "\x1b[5~":
		p.selected = max(0, p.selected-8)
	case "\x1b[6~":
		p.selected = min(len(p.choices)-1, p.selected+8)
	case "\x1b[H", "\x1bOH":
		p.selected = 0
	case "\x1b[F", "\x1bOF":
		p.selected = len(p.choices) - 1
	case "\r":
		if u.shell == nil {
			u.setNotice("Terminal clipboard unavailable", true)
		} else {
			u.shell.copyText(p.choices[p.selected].copyText)
		}
		p.modal, p.open, p.choices = "", false, nil
	}
	return true
}

// Source: codex-rs/tui/src/markdown.rs:171:249@1cc7e236 extract_copy_targets.
func responseCopyChoices(source string) []composerChoice {
	var choices []composerChoice
	add := func(label, value string) {
		preview := ""
		for line := range strings.SplitSeq(value, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				preview = pickerTruncateName(line, 72)
				break
			}
		}
		choices = append(choices, composerChoice{name: label, description: preview, copyText: value})
	}
	add("Whole response", source)
	data := []byte(source)
	quotes := &copyQuoteParser{BlockParser: parser.NewBlockquoteParser(), spans: make(map[ast.Node][2]int)}
	markdown := goldmark.DefaultParser()
	markdown.AddOptions(parser.WithBlockParsers(util.Prioritized(quotes, 799)))
	root := markdown.Parse(text.NewReader(data))
	quoteDepth := 0
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		switch n := node.(type) {
		case *ast.Blockquote:
			if entering {
				if quoteDepth == 0 {
					if value := copyQuoteSource(n, data, quotes.spans[n]); strings.TrimSpace(value) != "" {
						add("Blockquote", value)
					}
				}
				quoteDepth++
			} else {
				quoteDepth--
			}
		case *ast.FencedCodeBlock:
			if entering {
				label := "Code block"
				language := string(n.Language(data))
				if end := strings.IndexAny(language, ", \t"); end >= 0 {
					language = language[:end]
				}
				if language != "" {
					label = language + " code"
				}
				add(label, string(n.Lines().Value(data)))
			}
		}
		return ast.WalkContinue, nil
	})
	return choices
}

// Retain source boundaries before the parser consumes container prefixes. AST
// line segments alone lose fences and list-contained quote markers.
type copyQuoteParser struct {
	parser.BlockParser
	spans map[ast.Node][2]int
}

func (p *copyQuoteParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	_, position := reader.Position()
	node, state := p.BlockParser.Open(parent, reader, pc)
	if node != nil {
		p.spans[node] = [2]int{position.Start, position.Stop}
	}
	return node, state
}

func (p *copyQuoteParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	_, position := reader.Position()
	state := p.BlockParser.Continue(node, reader, pc)
	if state&parser.Continue != 0 {
		span := p.spans[node]
		span[1] = position.Stop
		p.spans[node] = span
	}
	return state
}

func copyQuoteSource(quote *ast.Blockquote, source []byte, span [2]int) string {
	prose := false
	_ = ast.Walk(quote, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		// Lazy paragraph continuations need not carry another quote marker.
		if n.Type() == ast.TypeBlock {
			for i := 0; i < n.Lines().Len(); i++ {
				span[1] = max(span[1], n.Lines().At(i).Stop)
			}
		}
		switch n := n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if strings.TrimSpace(string(n.Value(source))) != "" {
				prose = true
			}
		case *ast.String:
			if strings.TrimSpace(string(n.Value)) != "" {
				prose = true
			}
		}
		return ast.WalkContinue, nil
	})
	if !prose {
		return ""
	}
	if span[1] > 0 && source[span[1]-1] != '\n' {
		if next := strings.IndexByte(string(source[span[1]:]), '\n'); next >= 0 {
			span[1] += next + 1
		} else {
			span[1] = len(source)
		}
	}
	var out strings.Builder
	for line := range strings.SplitAfterSeq(string(source[span[0]:span[1]]), "\n") {
		if stripped, ok := strings.CutPrefix(strings.TrimLeft(line, " "), ">"); ok {
			line = strings.TrimPrefix(stripped, " ")
		}
		out.WriteString(line)
	}
	return out.String()
}
