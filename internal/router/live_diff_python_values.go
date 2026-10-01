package router

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Python bytes are valid path operands but are not text buffers.
func (s *execSourceScope) pythonTextLiteral(node *sitter.Node) (string, bool) {
	if node == nil || node.Kind() != "string" {
		return "", false
	}
	text := s.text(node)
	quote := strings.IndexAny(text, "\"'")
	if quote < 0 || strings.ContainsAny(text[:quote], "bB") {
		return "", false
	}
	return s.literal(node)
}

// Text I/O accepts only these newline values. Resolve them without executing
// keyword expressions; a computed or duplicate setting is not predictable.
func (s *execSourceScope) pythonNewline(args []*sitter.Node) (*string, bool) {
	var newline *string
	seen := false
	for _, arg := range args {
		if arg.Kind() != "keyword_argument" || s.text(arg.ChildByFieldName("name")) != "newline" {
			continue
		}
		if seen {
			return nil, false
		}
		seen = true
		value := arg.ChildByFieldName("value")
		if s.text(value) == "None" {
			continue
		}
		text, ok := s.pythonTextLiteral(value)
		if !ok || text != "" && text != "\n" && text != "\r" && text != "\r\n" {
			return nil, false
		}
		newline = new(text)
	}
	return newline, true
}

// Collections are immutable derived values. Mutation remains unsupported, so
// aliases and helper parameters can safely share their bounded backing slices.
func (s *execSourceScope) pythonLiteralList(ctx context.Context, node *sitter.Node, state *liveDiffPythonState, depth int) ([]liveDiffPythonLoopItem, bool) {
	if node == nil || depth > 32 || ctx.Err() != nil || time.Now().After(s.input.deadline) {
		return nil, false
	}
	if node.Kind() == "parenthesized_expression" && node.NamedChildCount() == 1 {
		return s.pythonLiteralList(ctx, node.NamedChild(0), state, depth+1)
	}
	if node.Kind() == "identifier" {
		if items, ok := state.lists[s.text(node)]; ok {
			return items, true
		}
	}
	if pairs, ok := s.pythonDictionary(ctx, node, state, depth+1); ok {
		return liveDiffPythonDictionaryColumn(pairs, 0), true
	}
	if function, args := sourceCall(node); function != nil && function.Kind() == "attribute" {
		object := function.ChildByFieldName("object")
		switch s.text(function.ChildByFieldName("attribute")) {
		case "items", "keys", "values":
			if len(args) != 0 {
				return nil, false
			}
			pairs, ok := s.pythonDictionary(ctx, object, state, depth+1)
			if !ok {
				return nil, false
			}
			switch s.text(function.ChildByFieldName("attribute")) {
			case "keys":
				return liveDiffPythonDictionaryColumn(pairs, 0), true
			case "values":
				return liveDiffPythonDictionaryColumn(pairs, 1), true
			default:
				return pairs, true
			}
		case "split", "splitlines":
			return s.pythonSplit(ctx, function, args, state, depth+1)
		}
	}
	if node.Kind() != "list" && node.Kind() != "tuple" {
		return nil, false
	}
	var items []liveDiffPythonLoopItem
	bytes := 0
	for i := range node.NamedChildCount() {
		child := node.NamedChild(uint(i))
		if child.Kind() == "comment" {
			continue
		}
		if len(items) >= 256 {
			return nil, false
		}
		item := liveDiffPythonLoopItem{unpack: child.Kind() == "list" || child.Kind() == "tuple"}
		values := []*sitter.Node{child}
		if item.unpack {
			values = nil
			for j := range child.NamedChildCount() {
				if value := child.NamedChild(uint(j)); value.Kind() != "comment" {
					values = append(values, value)
				}
			}
		}
		for _, value := range values {
			text, ok := s.pythonString(ctx, value, state, depth+1)
			bytes += len(text)
			if !ok || bytes > liveDiffPreviewFileLimit {
				return nil, false
			}
			item.values = append(item.values, text)
		}
		items = append(items, item)
	}
	return items, true
}

func liveDiffPythonDictionaryColumn(pairs []liveDiffPythonLoopItem, column int) []liveDiffPythonLoopItem {
	items := make([]liveDiffPythonLoopItem, 0, len(pairs))
	for _, pair := range pairs {
		items = append(items, liveDiffPythonLoopItem{values: []string{pair.values[column]}})
	}
	return items
}

func (s *execSourceScope) pythonDictionary(ctx context.Context, node *sitter.Node, state *liveDiffPythonState, depth int) ([]liveDiffPythonLoopItem, bool) {
	if node == nil || depth > 32 || ctx.Err() != nil || time.Now().After(s.input.deadline) {
		return nil, false
	}
	if node.Kind() == "identifier" {
		pairs, ok := state.dicts[s.text(node)]
		return pairs, ok
	}
	if node.Kind() == "parenthesized_expression" && node.NamedChildCount() == 1 {
		return s.pythonDictionary(ctx, node.NamedChild(0), state, depth+1)
	}
	if node.Kind() != "dictionary" {
		return nil, false
	}
	var pairs []liveDiffPythonLoopItem
	bytes := 0
	for i := range node.NamedChildCount() {
		pair := node.NamedChild(uint(i))
		if pair.Kind() == "comment" {
			continue
		}
		if pair.Kind() != "pair" {
			return nil, false
		}
		key, hasKey := s.pythonTextLiteral(pair.ChildByFieldName("key"))
		value, hasValue := s.pythonTextLiteral(pair.ChildByFieldName("value"))
		if !hasKey || !hasValue {
			return nil, false
		}
		item := liveDiffPythonLoopItem{values: []string{key, value}, unpack: true}
		index := slices.IndexFunc(pairs, func(p liveDiffPythonLoopItem) bool { return p.values[0] == key })
		if index >= 0 {
			bytes -= len(pairs[index].values[1])
			bytes += len(value)
			pairs[index] = item
		} else {
			bytes += len(key) + len(value)
			if len(pairs) >= 256 {
				return nil, false
			}
			pairs = append(pairs, item)
		}
		if bytes > liveDiffPreviewFileLimit {
			return nil, false
		}
	}
	return pairs, true
}

func (s *execSourceScope) pythonRepeat(ctx context.Context, node *sitter.Node, state *liveDiffPythonState, arriving uint, depth int) (liveDiffPythonText, bool) {
	left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
	// Determine both types of the left operand before touching the right.
	// Read-expression memoization prevents a type probe from consuming twice.
	leftText, hasLeftText := s.previewPythonText(ctx, left, state, arriving, depth+1)
	leftInt, hasLeftInt := s.pythonInt(ctx, left, state, depth+1)
	rightText, hasRightText := s.previewPythonText(ctx, right, state, arriving, depth+1)
	rightInt, hasRightInt := s.pythonInt(ctx, right, state, depth+1)
	var value liveDiffPythonText
	var count int
	switch {
	case hasLeftText && hasRightInt:
		value, count = leftText, rightInt
	case hasLeftInt && hasRightText:
		value, count = rightText, leftInt
	default:
		return liveDiffPythonText{}, false
	}
	if value.tip != 0 || value.arriving || liveDiffArriving(node, arriving) {
		return liveDiffPythonText{}, false
	}
	count = max(count, 0)
	if len(value.content) != 0 && count > liveDiffPreviewFileLimit/len(value.content) {
		return liveDiffPythonText{}, false
	}
	if value.content == "" {
		count = 0
	}
	return liveDiffPythonText{content: strings.Repeat(value.content, count)}, true
}

func liveDiffPythonWhitespace(r rune) bool {
	return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
}

func (s *execSourceScope) pythonTextTransform(ctx context.Context, method string, object *sitter.Node, args []*sitter.Node, state *liveDiffPythonState, arriving uint, depth int) (liveDiffPythonText, bool) {
	switch method {
	case "strip", "lstrip", "rstrip", "lower", "upper", "join":
	default:
		return liveDiffPythonText{}, false
	}
	positional, ok := liveDiffPythonArguments(s, args)
	if !ok {
		return liveDiffPythonText{}, false
	}
	value, ok := s.previewPythonText(ctx, object, state, arriving, depth+1)
	if !ok || value.tip != 0 || value.arriving || liveDiffArriving(object, arriving) {
		return liveDiffPythonText{}, false
	}
	switch method {
	case "strip", "lstrip", "rstrip":
		if len(positional) > 1 {
			return liveDiffPythonText{}, false
		}
		trim := liveDiffPythonWhitespace
		if len(positional) == 1 && s.text(positional[0]) != "None" {
			chars, ok := s.pythonString(ctx, positional[0], state, depth+1)
			if !ok {
				return liveDiffPythonText{}, false
			}
			trim = func(r rune) bool { return strings.ContainsRune(chars, r) }
		}
		switch method {
		case "strip":
			value.content = strings.TrimFunc(value.content, trim)
		case "lstrip":
			value.content = strings.TrimLeftFunc(value.content, trim)
		case "rstrip":
			value.content = strings.TrimRightFunc(value.content, trim)
		}
		return value, true
	case "lower", "upper":
		if len(positional) != 0 {
			return liveDiffPythonText{}, false
		}
		for _, r := range value.content {
			if r >= utf8.RuneSelf {
				return liveDiffPythonText{}, false
			}
		}
		if method == "lower" {
			value.content = strings.ToLower(value.content)
		} else {
			value.content = strings.ToUpper(value.content)
		}
		return value, true
	case "join":
		if len(positional) != 1 {
			return liveDiffPythonText{}, false
		}
		items, ok := s.pythonLiteralList(ctx, positional[0], state, depth+1)
		if !ok {
			return liveDiffPythonText{}, false
		}
		var parts []string
		bytes := 0
		for _, item := range items {
			if item.unpack || len(item.values) != 1 {
				return liveDiffPythonText{}, false
			}
			bytes += len(item.values[0])
			if len(parts) != 0 {
				bytes += len(value.content)
			}
			if bytes > liveDiffPreviewFileLimit {
				return liveDiffPythonText{}, false
			}
			parts = append(parts, item.values[0])
		}
		return liveDiffPythonText{content: strings.Join(parts, value.content)}, true
	}
	return liveDiffPythonText{}, false
}

func (s *execSourceScope) pythonSplit(ctx context.Context, function *sitter.Node, args []*sitter.Node, state *liveDiffPythonState, depth int) ([]liveDiffPythonLoopItem, bool) {
	text, ok := s.pythonString(ctx, function.ChildByFieldName("object"), state, depth+1)
	positional, valid := liveDiffPythonArguments(s, args)
	if !ok || !valid {
		return nil, false
	}
	var parts []string
	if s.text(function.ChildByFieldName("attribute")) == "splitlines" {
		if len(positional) > 1 {
			return nil, false
		}
		keep := false
		if len(positional) == 1 {
			switch s.text(positional[0]) {
			case "True":
				keep = true
			case "False":
			default:
				return nil, false
			}
		}
		start := 0
		for at, r := range text {
			if r != '\n' && r != '\r' && r != '\v' && r != '\f' && !(r >= 0x1c && r <= 0x1e) && r != '\u0085' && r != '\u2028' && r != '\u2029' {
				continue
			}
			if at < start {
				continue // LF following a consumed CRLF.
			}
			end := at + utf8.RuneLen(r)
			if r == '\r' && end < len(text) && text[end] == '\n' {
				end++
			}
			stop := at
			if keep {
				stop = end
			}
			parts = append(parts, text[start:stop])
			start = end
			if len(parts) > 256 {
				return nil, false
			}
		}
		if start < len(text) {
			parts = append(parts, text[start:])
		}
	} else {
		if len(positional) > 2 {
			return nil, false
		}
		maxsplit := -1
		if len(positional) == 2 {
			if maxsplit, ok = s.pythonInt(ctx, positional[1], state, depth+1); !ok {
				return nil, false
			}
		}
		if len(positional) == 0 || s.text(positional[0]) == "None" {
			remaining := strings.TrimLeftFunc(text, liveDiffPythonWhitespace)
			for remaining != "" {
				if maxsplit == 0 {
					parts = append(parts, remaining)
					break
				}
				at := strings.IndexFunc(remaining, liveDiffPythonWhitespace)
				if at < 0 {
					parts = append(parts, remaining)
					break
				}
				parts = append(parts, remaining[:at])
				remaining = strings.TrimLeftFunc(remaining[at:], liveDiffPythonWhitespace)
				if maxsplit > 0 {
					maxsplit--
				}
				if len(parts) > 256 {
					return nil, false
				}
			}
		} else {
			separator, ok := s.pythonString(ctx, positional[0], state, depth+1)
			if !ok || separator == "" {
				return nil, false
			}
			count := strings.Count(text, separator)
			if maxsplit >= 0 {
				count = min(count, maxsplit)
			}
			if count >= 256 {
				return nil, false
			}
			parts = strings.SplitN(text, separator, count+1)
		}
	}
	if len(parts) > 256 {
		return nil, false
	}
	items := make([]liveDiffPythonLoopItem, 0, len(parts))
	for _, part := range parts {
		items = append(items, liveDiffPythonLoopItem{values: []string{part}})
	}
	return items, true
}
