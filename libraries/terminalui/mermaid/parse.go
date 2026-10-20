// Package mermaid renders an extended bounded Codex flowchart subset without I/O.
// Source: codex-rs/mermaid/src/parse.rs:15:217@[687a119f0fcaace47e1f1abcc77cec6c813fd6da] parse
// Source: codex-rs/mermaid/src/syntax.rs:9:105@[687a119f0fcaace47e1f1abcc77cec6c813fd6da] statements
package mermaid

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const (
	maxSource    = 16 * 1024
	maxNodes     = 16
	maxEdges     = 24
	maxLabel     = 40
	maxNodeLabel = 256
	maxCells     = 64 * 1024
)

type node struct {
	id, label, shape string
	declared         bool
}
type edge struct {
	from, to       int
	label          string
	source, target rune
	dashed         bool
}
type graph struct {
	direction string
	nodes     []node
	edges     []edge
}

func statements(source string) ([]string, bool) {
	var out []string
	for line := range strings.SplitSeq(source, "\n") {
		rest := strings.TrimSpace(line)
		if strings.HasPrefix(rest, "%%{") {
			return nil, false
		}
		if strings.HasPrefix(rest, "%%") {
			continue
		}
		for i, c := range rest {
			if c == '#' || c == '&' {
				j := i + 1
				for j < len(rest) && identByte(rest[j]) {
					j++
				}
				if j > i+1 && j < len(rest) && rest[j] == ';' {
					return nil, false
				}
			}
		}
		for rest != "" {
			quoted := false
			var close rune
			end := len(rest)
			for i, c := range rest {
				if c == '"' {
					quoted = !quoted
				} else if !quoted {
					if c == ';' && close == 0 {
						end = i
						break
					}
					if close == c {
						close = 0
					} else if close == 0 {
						switch c {
						case '[':
							close = ']'
						case '{':
							close = '}'
						case '|':
							close = '|'
						}
					}
				}
			}
			if s := strings.TrimSpace(rest[:end]); s != "" {
				out = append(out, s)
			}
			if end == len(rest) {
				break
			}
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	return out, true
}

func identByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func letter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func label(text string, node bool) (string, bool) {
	if strings.HasPrefix(text, "\"`") {
		return "", false
	}
	if strings.HasPrefix(text, "\"") {
		if len(text) < 2 || !strings.HasSuffix(text, "\"") {
			return "", false
		}
		text = text[1 : len(text)-1]
	} else if strings.ContainsAny(text, "[]{}|") {
		return "", false
	}
	if strings.Contains(text, "\"") {
		return "", false
	}
	limit := maxLabel
	if node {
		limit = maxNodeLabel
		text = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n").Replace(text)
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	sum := 0
	for i, c := range text {
		if node && c == '\n' {
			continue
		}
		if c == '<' && i+1 < len(text) && (letter(text[i+1]) || strings.ContainsRune("/!?", rune(text[i+1]))) {
			return "", false
		}
		w := ansi.StringWidth(string(c))
		if unicode.IsControl(c) || w == 0 || strings.ContainsRune("┌┐└┘├┤╪◄", c) {
			return "", false
		}
		sum += w
	}
	return text, sum <= limit && sum == ansi.StringWidth(text)
}

func delimited(text, close string) (string, string, bool) {
	if strings.HasPrefix(text, "\"") {
		end := strings.IndexByte(text[1:], '"')
		if end < 0 {
			return "", "", false
		}
		end += 2
		rest, ok := strings.CutPrefix(text[end:], close)
		return text[:end], rest, ok
	}
	a, b, ok := strings.Cut(text, close)
	return a, b, ok
}

func (g *graph) node(rest *string) (int, bool) {
	*rest = strings.TrimLeftFunc(*rest, unicode.IsSpace)
	n := 0
	for n < len(*rest) && identByte((*rest)[n]) {
		n++
	}
	if n == 0 || n > maxLabel || !letter((*rest)[0]) {
		return 0, false
	}
	id := (*rest)[:n]
	*rest = (*rest)[n:]
	switch id {
	case "end", "subgraph", "direction", "style", "class", "classDef", "linkStyle", "click":
		return 0, false
	}
	for _, open := range []string{"[(", "[[", "[/", "[\\", "{{"} {
		if strings.HasPrefix(*rest, open) {
			return 0, false
		}
	}
	index := -1
	for i, n := range g.nodes {
		if n.id == id {
			index = i
			break
		}
	}
	if index < 0 {
		if len(g.nodes) == maxNodes {
			return 0, false
		}
		index = len(g.nodes)
		g.nodes = append(g.nodes, node{id: id, label: id, shape: "rectangle"})
	}
	for _, shape := range []struct{ open, close, name string }{{"[", "]", "rectangle"}, {"{", "}", "decision"}, {"([", "])", "stadium"}} {
		if !strings.HasPrefix(*rest, shape.open) {
			continue
		}
		raw, remaining, ok := delimited((*rest)[len(shape.open):], shape.close)
		if !ok {
			return 0, false
		}
		text, ok := label(raw, true)
		if !ok {
			return 0, false
		}
		*rest = remaining
		node := &g.nodes[index]
		if node.declared && (node.label != text || node.shape != shape.name) {
			return 0, false
		}
		node.label, node.shape, node.declared = text, shape.name, true
		break
	}
	return index, true
}

func (g *graph) group(rest *string) ([]int, bool) {
	first, ok := g.node(rest)
	if !ok {
		return nil, false
	}
	out := []int{first}
	for {
		after, found := strings.CutPrefix(strings.TrimLeftFunc(*rest, unicode.IsSpace), "&")
		if !found {
			return out, true
		}
		if len(out) == maxEdges {
			return nil, false
		}
		*rest = after
		n, ok := g.node(rest)
		if !ok {
			return nil, false
		}
		out = append(out, n)
	}
}

func parse(source string) (graph, bool) {
	g := graph{direction: "TD"}
	if len(source) > maxSource {
		return g, false
	}
	stmts, ok := statements(source)
	if !ok || len(stmts) == 0 {
		return g, false
	}
	header := strings.Fields(stmts[0])
	if len(header) < 1 || len(header) > 2 || (header[0] != "flowchart" && header[0] != "graph") {
		return g, false
	}
	if len(header) == 2 {
		g.direction = header[1]
	}
	switch g.direction {
	case "TD", "TB", "BT", "LR", "RL":
	default:
		return g, false
	}
	for _, rest := range stmts[1:] {
		from, ok := g.group(&rest)
		if !ok {
			return g, false
		}
		for strings.TrimSpace(rest) != "" {
			rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
			e := edge{source: '─', target: '◄'}
			matched := false
			for _, style := range []struct {
				token          string
				source, target rune
				dashed         bool
			}{{"<-.->", '◄', '◄', true}, {"<-->", '◄', '◄', false}, {"-.->", '─', '◄', true}, {"-.-", '─', '─', true}, {"-->", '─', '◄', false}, {"---", '─', '─', false}} {
				after, found := strings.CutPrefix(rest, style.token)
				if !found {
					continue
				}
				if style.target == '─' && (strings.HasPrefix(after, "o") || strings.HasPrefix(after, "x")) {
					return g, false
				}
				e.source, e.target, e.dashed = style.source, style.target, style.dashed
				rest = strings.TrimLeftFunc(after, unicode.IsSpace)
				matched = true
				if after, found := strings.CutPrefix(rest, "|"); found {
					raw, remaining, ok := delimited(after, "|")
					if !ok {
						return g, false
					}
					e.label, ok = label(raw, false)
					if !ok {
						return g, false
					}
					rest = remaining
				}
				break
			}
			if !matched {
				stem := "--"
				after, found := strings.CutPrefix(rest, "--")
				if !found {
					after, found = strings.CutPrefix(rest, "-.")
					stem = ".-"
					e.dashed = true
				}
				if !found {
					return g, false
				}
				raw, remaining, ok := strings.Cut(after, stem)
				if !ok || raw == "" || strings.TrimLeftFunc(raw, unicode.IsSpace) == raw || strings.TrimRightFunc(raw, unicode.IsSpace) == raw {
					return g, false
				}
				rest, ok = strings.CutPrefix(remaining, ">")
				if !ok {
					return g, false
				}
				e.label, ok = label(strings.TrimSpace(raw), false)
				if !ok {
					return g, false
				}
			}
			to, ok := g.group(&rest)
			if !ok || len(from)*len(to) > maxEdges-len(g.edges) {
				return g, false
			}
			for _, a := range from {
				for _, b := range to {
					e.from, e.to = a, b
					g.edges = append(g.edges, e)
				}
			}
			from = to
		}
	}
	return g, len(g.nodes) > 0
}
