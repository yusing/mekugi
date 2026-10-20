package toolplugin

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	jsonlang "github.com/tree-sitter/tree-sitter-json/bindings/go"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"go.yaml.in/yaml/v3"
)

// Source: plugins/javascript_string.ts:8:64@[543de4f3] decodeJavaScriptStringLiteral
func decodeJSString(literal string) (string, bool) {
	if len(literal) < 2 || literal[0] != '\'' && literal[0] != '"' || literal[len(literal)-1] != literal[0] {
		return "", false
	}
	var out []uint16
	body := literal[1 : len(literal)-1]
	for len(body) > 0 {
		r, n := utf8.DecodeRuneInString(body)
		body = body[n:]
		if r == rune(literal[0]) || r == '\n' || r == '\r' {
			return "", false
		}
		if r != '\\' {
			out = append(out, utf16.Encode([]rune{r})...)
			continue
		}
		if len(body) == 0 {
			return "", false
		}
		r, n = utf8.DecodeRuneInString(body)
		body = body[n:]
		switch r {
		case '\r':
			body = strings.TrimPrefix(body, "\n")
			continue
		case '\n', '\u2028', '\u2029':
			continue
		case 'b':
			r = '\b'
		case 'f':
			r = '\f'
		case 'n':
			r = '\n'
		case 'r':
			r = '\r'
		case 't':
			r = '\t'
		case 'v':
			r = '\v'
		case '0':
			if len(body) > 0 && body[0] >= '0' && body[0] <= '9' {
				return "", false
			}
			r = 0
		case '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return "", false
		case 'x', 'u':
			width := 4
			if r == 'x' {
				width = 2
			}
			braced := r == 'u' && strings.HasPrefix(body, "{")
			if braced {
				body = body[1:]
				width = strings.IndexByte(body, '}')
				if width < 1 {
					return "", false
				}
			}
			if len(body) < width {
				return "", false
			}
			digits := body[:width]
			for _, digit := range digits {
				if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f' || digit >= 'A' && digit <= 'F') {
					return "", false
				}
			}
			v, err := strconv.ParseUint(digits, 16, 32)
			if err != nil || v > 0x10ffff {
				return "", false
			}
			r = rune(v)
			body = body[width:]
			if braced {
				body = body[1:]
			}
		}
		if r >= 0xd800 && r <= 0xdfff {
			out = append(out, uint16(r))
		} else {
			out = append(out, utf16.Encode([]rune{r})...)
		}
	}
	// ECMAScript permits lone UTF-16 surrogates. Keep those code units as
	// WTF-8 internally; outlineString emits their original JSON escapes rather
	// than inventing a U+FFFD module or JSON-property name.
	var decoded strings.Builder
	for i := 0; i < len(out); i++ {
		unit := out[i]
		if unit >= 0xd800 && unit <= 0xdbff && i+1 < len(out) && out[i+1] >= 0xdc00 && out[i+1] <= 0xdfff {
			decoded.WriteRune(utf16.DecodeRune(rune(unit), rune(out[i+1])))
			i++
			continue
		}
		if unit >= 0xd800 && unit <= 0xdfff {
			decoded.WriteByte(byte(0xe0 | unit>>12))
			decoded.WriteByte(byte(0x80 | (unit>>6)&0x3f))
			decoded.WriteByte(byte(0x80 | unit&0x3f))
		} else {
			decoded.WriteRune(rune(unit))
		}
	}
	return decoded.String(), true
}

// Source: plugins/inspect_file_document.ts:146:210@[543de4f3] jsonOutline
func (s *parsedSource) jsonDocument(ctx context.Context) error {
	strict := *s
	strict.entries = nil
	strict.tokens = nil
	invalidOffset, err := strict.strictJSON(ctx)
	if err == nil {
		s.entries = strict.entries
		s.tokens = strict.tokens
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(jsonlang.Language())); err != nil {
		return err
	}
	data := []byte(s.source)
	if strings.HasPrefix(s.source, "\ufeff") {
		copy(data, []byte("   "))
	}
	tree := parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte {
		if offset >= len(data) {
			return nil
		}
		return data[offset:]
	}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return ctx.Err() != nil }})
	if tree == nil {
		return ctx.Err()
	}
	defer tree.Close()
	var value func(*sitter.Node, string)
	value = func(n *sitter.Node, pointer string) {
		kind := n.Kind()
		typ := kind
		switch kind {
		case "object", "array", "string", "number", "null":
		case "true", "false":
			typ = "boolean"
		default:
			return
		}
		from, to := int(n.StartByte()), int(n.EndByte())
		s.add("json", "", from, to, -1, -1, !n.HasError())
		e := &s.entries[len(s.entries)-1]
		e.pointer = pointer
		e.valueType = typ
		if kind == "object" {
			for _, pair := range nodeChildren(n) {
				if pair.Kind() != "pair" {
					continue
				}
				key := pair.ChildByFieldName("key")
				v := pair.ChildByFieldName("value")
				if key == nil || v == nil || key.HasError() {
					continue
				}
				name, ok := decodeNativeJSONString(s.text(key))
				if !ok {
					continue
				}
				valid := true
				for _, part := range nodeChildren(pair) {
					if part.StartByte() < v.StartByte() && part.IsError() {
						valid = false
					}
				}
				if valid {
					value(v, pointer+"/"+strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1"))
				}
			}
		}
		if kind == "array" {
			index := 0
			for i := uint(0); i < n.ChildCount(); i++ {
				child := n.Child(i)
				if child.Kind() == "," {
					index++
				} else {
					value(child, fmt.Sprintf("%s/%d", pointer, index))
				}
			}
		}
	}
	for _, n := range nodeChildren(tree.RootNode()) {
		switch n.Kind() {
		case "object", "array", "string", "number", "true", "false", "null":
			value(n, "")
		}
		if len(s.entries) > 0 {
			break
		}
	}
	var visit func(*sitter.Node)
	visit = func(n *sitter.Node) {
		if n.Kind() == "string" && !n.HasError() {
			if v, ok := decodeNativeJSONString(s.text(n)); ok {
				s.tokens = append(s.tokens, sourceToken{int(n.StartByte()) + 1, v})
			}
			return
		}
		for i := uint(0); i < n.ChildCount(); i++ {
			visit(n.Child(i))
		}
	}
	visit(tree.RootNode())
	s.parseError(invalidOffset)
	return nil
}

var yamlErrorLine = regexp.MustCompile(`line ([0-9]+)`)

// Source: plugins/inspect_file_document.ts:23:133@[543de4f3] markdownOutline
func (s *parsedSource) markdown() {
	endFront := 0
	if first, ok := s.lines.row(1); ok && strings.TrimPrefix(first, "\ufeff") == "---" {
		closing := 0
		for i := 2; i <= len(s.lines.starts); i++ {
			row, _ := s.lines.row(i)
			if row == "---" {
				closing = i
				break
			}
		}
		if closing > 0 {
			start := s.lines.starts[1]
			end := s.lines.starts[closing-1]
			endFront = len(s.source)
			if closing < len(s.lines.starts) {
				endFront = s.lines.starts[closing]
			}
			var doc yaml.Node
			err := yaml.Unmarshal([]byte(s.source[start:end]), &doc)
			if err != nil {
				line := 2
				if match := yamlErrorLine.FindStringSubmatch(err.Error()); len(match) > 1 {
					n, _ := strconv.Atoi(match[1])
					line = min(len(s.lines.starts), n+1)
				}
				s.parseError(s.lines.starts[line-1])
			}
			if len(doc.Content) > 0 && doc.Content[0].Kind == yaml.MappingNode {
				mapping := doc.Content[0]
				seen := map[string]bool{}
				for i := 0; i+1 < len(mapping.Content); i += 2 {
					key := mapping.Content[i]
					if key.Kind != yaml.ScalarNode || key.Tag == "!!null" || key.Value == "" {
						continue
					}
					line := key.Line + 1
					offset := s.lines.starts[line-1] + key.Column - 1
					to := offset + len(key.Value)
					if key.Style == yaml.DoubleQuotedStyle || key.Style == yaml.SingleQuotedStyle {
						quote := s.source[offset]
						to = offset + 1
						for to < len(s.source) {
							if s.source[to] == '\\' && quote == '"' {
								to += 2
								continue
							}
							if s.source[to] == quote {
								to++
								if quote == '\'' && to < len(s.source) && s.source[to] == quote {
									to++
									continue
								}
								break
							}
							to++
						}
					}
					to = min(to, len(s.source))
					s.add("frontmatter", key.Value, offset, to, -1, -1, true)
					if seen[key.Value] {
						s.parseError(offset)
					}
					seen[key.Value] = true
				}
			}
		}
	}
	data := []byte(s.source)
	// Goldmark's line scanner uses LF. Normalize without changing source offsets.
	for i := range data {
		if data[i] == '\r' && (i+1 == len(data) || data[i+1] != '\n') {
			data[i] = '\n'
		}
	}
	doc := goldmark.DefaultParser().Parse(text.NewReader(data))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := n.(*ast.Heading)
		if !entering || !ok || heading.Lines().Len() == 0 {
			return ast.WalkContinue, nil
		}
		segment := heading.Lines().At(0)
		offset := segment.Start
		line := s.lines.at(offset)
		// Goldmark's heading span is already inside its blockquote/list
		// container. Walk back over only its ATX marker, not the whole row.
		markerEnd := offset
		for markerEnd > s.lines.starts[line-1] && (s.source[markerEnd-1] == ' ' || s.source[markerEnd-1] == '\t') {
			markerEnd--
		}
		from := markerEnd
		for from > s.lines.starts[line-1] && s.source[from-1] == '#' {
			from--
		}
		if markerEnd-from != heading.Level || from < endFront {
			return ast.WalkContinue, nil
		}
		name := strings.TrimSpace(string(segment.Value(data)))
		if name != "" {
			s.add("heading", name, from, s.lines.ends[line-1], -1, -1, true)
			s.entries[len(s.entries)-1].level = heading.Level
		}
		return ast.WalkContinue, nil
	})
}

func decodeNativeJSONString(literal string) (string, bool) {
	if !jsontext.Value(literal).IsValid(jsontext.AllowInvalidUTF8(true)) {
		return "", false
	}
	return decodeJSString(literal)
}

// Tree-sitter supplies recovery only. JSON's authoritative grammar is the
// standard decoder, which rejects comments/multiple roots and accepts exponent
// signs. Duplicate properties and escaped lone surrogates remain source facts.
func (s *parsedSource) strictJSON(ctx context.Context) (int, error) {
	base := 0
	if strings.HasPrefix(s.source, "\ufeff") {
		base = 3
	}
	decoder := jsontext.NewDecoder(strings.NewReader(s.source[base:]), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	tokenStart := func() int {
		offset := base + int(decoder.InputOffset())
		for offset < len(s.source) && strings.ContainsRune(" \t\r\n,:", rune(s.source[offset])) {
			offset++
		}
		return offset
	}
	var value func(string) error
	readString := func(from int) (string, error) {
		_, err := decoder.ReadToken()
		if err != nil {
			return "", err
		}
		to := base + int(decoder.InputOffset())
		name, ok := decodeJSString(s.source[from:to])
		if !ok {
			return "", errors.New("invalid JSON string")
		}
		s.tokens = append(s.tokens, sourceToken{from + 1, name})
		return name, nil
	}
	value = func(pointer string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind := decoder.PeekKind()
		from := tokenStart()
		typ := ""
		switch kind {
		case '{':
			typ = "object"
		case '[':
			typ = "array"
		case '"':
			typ = "string"
		case '0':
			typ = "number"
		case 't', 'f':
			typ = "boolean"
		case 'n':
			typ = "null"
		default:
			_, err := decoder.ReadToken()
			return err
		}
		index := len(s.entries)
		s.add("json", "", from, from, -1, -1, true)
		s.entries[index].pointer = pointer
		s.entries[index].valueType = typ
		if kind == '"' {
			if _, err := readString(from); err != nil {
				return err
			}
		} else {
			if _, err := decoder.ReadToken(); err != nil {
				return err
			}
		}
		if kind == '{' {
			for decoder.PeekKind() != '}' {
				keyFrom := tokenStart()
				key, err := readString(keyFrom)
				if err != nil {
					return err
				}
				segment := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
				if err := value(pointer + "/" + segment); err != nil {
					return err
				}
			}
			if _, err := decoder.ReadToken(); err != nil {
				return err
			}
		}
		if kind == '[' {
			for i := 0; decoder.PeekKind() != ']'; i++ {
				if err := value(fmt.Sprintf("%s/%d", pointer, i)); err != nil {
					return err
				}
			}
			if _, err := decoder.ReadToken(); err != nil {
				return err
			}
		}
		s.entries[index].endLine = s.lines.at(max(from, base+int(decoder.InputOffset())-1))
		return nil
	}
	err := value("")
	if err == nil {
		nextStart := tokenStart()
		_, err = decoder.ReadToken()
		if errors.Is(err, io.EOF) {
			return 0, nil
		}
		if err == nil {
			return nextStart, errors.New("multiple JSON values")
		}
	}
	offset := base + int(decoder.InputOffset())
	if syntax, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		offset = base + int(syntax.ByteOffset)
	}
	return offset, err
}
