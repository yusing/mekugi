package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Source: codex-rs/tui/src/chatwidget/copy_picker.rs:10:105@1cc7e236.
// Reuse the transcript and OSC 52 owner rather than a second clipboard backend.
func (u *appServerUI) showCopyPicker() {
	u.deleteDraftRange(0, len(u.Text))
	var response string
	for _, entry := range slices.Backward(u.attached.source.Entries) {
		n := entry.Source.Native
		if n == nil || n.Thread != u.Thread || entry.Source.Agent != "Main" {
			continue
		}
		if entry.Source.Kind == "text" && n.Phase == "item/completed" {
			response = entry.Source.Text
		}
		if strings.TrimSpace(response) != "" {
			break
		}
	}
	if strings.TrimSpace(response) == "" {
		u.setNotice("No agent response to copy", true)
		return
	}
	u.picker.cancelScan()
	p := &u.picker
	u.openMenu("copy")
	p.Target, p.Problem, p.Loading = completion.Target{}, "", false
	p.Choices, p.CopyTexts = nil, nil
	for _, choice := range responseCopyChoices(response) {
		p.Choices = append(p.Choices, choice.Choice)
		p.CopyTexts = append(p.CopyTexts, choice.copyText)
	}
}

// Source: codex-rs/tui/src/markdown.rs:171:249@1cc7e236 extract_copy_targets.
type copyChoice struct {
	completion.Choice
	copyText string
}

func responseCopyChoices(source string) []copyChoice {
	var choices []copyChoice
	add := func(label, value string) {
		preview := ""
		for line := range strings.SplitSeq(value, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				preview = terminalui.TruncateGraphemes(line, 72)
				break
			}
		}
		choices = append(choices, copyChoice{Choice: completion.Choice{ID: uint64(len(choices) + 1), Name: label, Description: preview}, copyText: value})
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
