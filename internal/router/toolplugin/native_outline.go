package toolplugin

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"go/scanner"
	"go/token"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
	"github.com/yusing/mekugi/internal/gooutline"
	"github.com/yusing/mekugi/internal/sourcekind"
)

type sourceLines struct {
	text         string
	starts, ends []int
}

func newSourceLines(s string) sourceLines {
	l := sourceLines{text: s}
	for start := 0; start < len(s); {
		end := start
		for end < len(s) && s[end] != '\r' && s[end] != '\n' {
			end++
		}
		l.starts = append(l.starts, start)
		l.ends = append(l.ends, end)
		next := end
		if next < len(s) {
			next++
			if s[end] == '\r' && next < len(s) && s[next] == '\n' {
				next++
			}
		}
		start = next
	}
	return l
}
func (l sourceLines) at(offset int) int {
	return max(1, sort.Search(len(l.starts), func(i int) bool { return l.starts[i] > offset }))
}
func (l sourceLines) row(n int) (string, bool) {
	if n < 1 || n > len(l.starts) {
		return "", false
	}
	return l.text[l.starts[n-1]:l.ends[n-1]], true
}
func (l sourceLines) position(offset int) symbolPosition {
	line := l.at(offset) - 1
	character := 0
	for _, r := range l.text[l.starts[line]:offset] {
		character += utf16.RuneLen(r)
	}
	return symbolPosition{line, character}
}
func (l sourceLines) offset(p symbolPosition) (int, bool) {
	if p.Line < 0 || p.Line >= len(l.starts) || p.Character < 0 {
		return 0, false
	}
	offset := l.starts[p.Line]
	remaining := p.Character
	for offset < l.ends[p.Line] && remaining > 0 {
		r, n := utf8.DecodeRuneInString(l.text[offset:])
		remaining -= utf16.RuneLen(r)
		offset += n
	}
	return offset, remaining == 0
}

type outlineEntry struct {
	kind, name, receiver, pointer, valueType     string
	line, endLine, level, from, nameFrom, nameTo int
	complete                                     bool
}

func (e outlineEntry) MarshalJSON() ([]byte, error) {
	switch e.kind {
	case "method":
		return json.Marshal(struct {
			Kind     string        `json:"kind"`
			Name     outlineString `json:"name"`
			Receiver string        `json:"receiver"`
			Line     int           `json:"line"`
			End      int           `json:"line_end"`
		}{e.kind, outlineString(e.name), e.receiver, e.line, e.endLine}, jsontext.PreserveRawStrings(true), jsontext.AllowInvalidUTF8(true))
	case "json":
		return json.Marshal(struct {
			Kind      string        `json:"kind"`
			Pointer   outlineString `json:"pointer"`
			ValueType string        `json:"value_type"`
			Line      int           `json:"line"`
			End       int           `json:"line_end"`
		}{e.kind, outlineString(e.pointer), e.valueType, e.line, e.endLine}, jsontext.PreserveRawStrings(true), jsontext.AllowInvalidUTF8(true))
	case "heading":
		return json.Marshal(struct {
			Kind  string        `json:"kind"`
			Name  outlineString `json:"name"`
			Level int           `json:"level"`
			Line  int           `json:"line"`
			End   int           `json:"line_end"`
		}{e.kind, outlineString(e.name), e.level, e.line, e.endLine}, jsontext.PreserveRawStrings(true), jsontext.AllowInvalidUTF8(true))
	default:
		return json.Marshal(struct {
			Kind string        `json:"kind"`
			Name outlineString `json:"name"`
			Line int           `json:"line"`
			End  int           `json:"line_end"`
		}{e.kind, outlineString(e.name), e.line, e.endLine}, jsontext.PreserveRawStrings(true), jsontext.AllowInvalidUTF8(true))
	}
}

type parsedSource struct {
	source  string
	lines   sourceLines
	format  sourcekind.Format
	entries []outlineEntry
	tokens  []sourceToken
}
type sourceToken struct {
	from int
	text string
}

func (s *parsedSource) add(kind, name string, from, to, nameFrom, nameTo int, complete bool) {
	s.entries = append(s.entries, outlineEntry{kind: kind, name: name, from: from, line: s.lines.at(from), endLine: s.lines.at(max(from, to-1)), nameFrom: nameFrom, nameTo: nameTo, complete: complete})
}
func (s *parsedSource) parseError(offset int) {
	offset = min(offset, max(0, len(s.source)-1))
	line := s.lines.at(offset)
	for _, e := range s.entries {
		if e.kind == "parse_error" && e.line == line {
			return
		}
	}
	s.add("parse_error", "syntax error", offset, offset, -1, -1, false)
}
func parseNativeSource(ctx context.Context, source string, format sourcekind.Format) (*parsedSource, error) {
	s := &parsedSource{source: source, lines: newSourceLines(source), format: format}
	switch {
	case format.Language == "go":
		doc := gooutline.Parse(source)
		for _, e := range doc.Entries {
			s.add(e.Kind, e.Name, e.From, e.To, e.NameFrom, e.NameTo, e.Complete)
			s.entries[len(s.entries)-1].receiver = e.Receiver
		}
		for _, e := range doc.Errors {
			s.parseError(e.Offset)
		}
		var scan scanner.Scanner
		set := token.NewFileSet()
		f := set.AddFile("", -1, len(source))
		scan.Init(f, []byte(source), func(token.Position, string) {}, 0)
		for {
			pos, tok, lit := scan.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.IDENT {
				from := f.Offset(pos)
				s.tokens = append(s.tokens, sourceToken{from, lit})
			}
		}
	case format.Kind == "markdown":
		s.markdown()
	case format.Kind == "json":
		if err := s.jsonDocument(ctx); err != nil {
			return nil, err
		}
	default:
		var language *sitter.Language
		switch format.Language {
		case "python":
			language = sitter.NewLanguage(python.Language())
		case "rust":
			language = sitter.NewLanguage(rust.Language())
		case "javascript":
			language = sitter.NewLanguage(javascript.Language())
		case "typescript":
			if format.JSX {
				language = sitter.NewLanguage(typescript.LanguageTSX())
			} else {
				language = sitter.NewLanguage(typescript.LanguageTypescript())
			}
		default:
			return nil, fmt.Errorf("unsupported source language %q", format.Language)
		}
		parser := sitter.NewParser()
		defer parser.Close()
		if err := parser.SetLanguage(language); err != nil {
			return nil, err
		}
		// Preserve byte offsets while making bare CR and a BOM parser whitespace.
		data := []byte(source)
		if strings.HasPrefix(source, "\ufeff") {
			copy(data, []byte("   "))
		}
		for i := range data {
			if data[i] == '\r' && (i+1 == len(data) || data[i+1] != '\n') {
				data[i] = '\n'
			}
		}
		tree := parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte {
			if offset >= len(data) {
				return nil
			}
			return data[offset:]
		}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return ctx.Err() != nil }})
		if tree == nil {
			return nil, fmt.Errorf("source parsing canceled: %w", ctx.Err())
		}
		defer tree.Close()
		s.codeOutline(tree.RootNode())
		var visit func(*sitter.Node)
		visit = func(n *sitter.Node) {
			if n.IsError() || n.IsMissing() {
				s.parseError(int(n.StartByte()))
			}
			kind := n.Kind()
			if kind == "identifier" || kind == "property_identifier" || kind == "private_property_identifier" || kind == "type_identifier" || kind == "shorthand_property_identifier" || kind == "shorthand_property_identifier_pattern" {
				s.tokens = append(s.tokens, sourceToken{int(n.StartByte()), n.Utf8Text(data)})
			}
			for i := uint(0); i < n.ChildCount(); i++ {
				visit(n.Child(i))
			}
		}
		visit(tree.RootNode())
	}
	slices.SortStableFunc(s.entries, func(a, b outlineEntry) int { return a.from - b.from })
	return s, nil
}
func nodeChildren(n *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	if n != nil {
		for i := uint(0); i < n.NamedChildCount(); i++ {
			out = append(out, n.NamedChild(i))
		}
	}
	return out
}
func (s *parsedSource) text(n *sitter.Node) string {
	if n == nil {
		return ""
	}
	return s.source[n.StartByte():n.EndByte()]
}
func (s *parsedSource) addNode(kind string, n, span, name *sitter.Node, receiver string) {
	if name == nil {
		return
	}
	s.add(kind, s.text(name), int(span.StartByte()), int(span.EndByte()), int(name.StartByte()), int(name.EndByte()), !n.HasError() && !span.HasError())
	s.entries[len(s.entries)-1].receiver = receiver
}
func bindingNodes(n *sitter.Node) []*sitter.Node {
	if n == nil {
		return nil
	}
	switch n.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		return []*sitter.Node{n}
	case "pair_pattern":
		return bindingNodes(n.ChildByFieldName("value"))
	case "assignment_pattern", "object_assignment_pattern":
		return bindingNodes(n.ChildByFieldName("left"))
	case "attribute", "subscript", "type":
		return nil
	case "array_pattern", "object_pattern", "rest_pattern", "list_pattern", "tuple_pattern", "pattern_list", "list_splat_pattern", "dictionary_splat_pattern":
		var out []*sitter.Node
		for _, c := range nodeChildren(n) {
			out = append(out, bindingNodes(c)...)
		}
		return out
	}
	return nil
}

// Source: plugins/inspect_file_code.ts:141:449@[543de4f3] codeOutline
// Only declaration-owned bindings are projected. Never walk initializer bodies
// to discover names, or recursively collect methods from nested classes.
func (s *parsedSource) codeOutline(root *sitter.Node) {
	if s.format.Language == "rust" {
		s.rustDeclarations(root, "")
		return
	}
	for _, top := range nodeChildren(root) {
		s.codeDeclaration(top, top)
	}
}

// Rust attributes are siblings of their declarations, not declaration wrappers.
// Only source-file declarations and direct trait/impl/extern members are visited.
func (s *parsedSource) rustDeclarations(root *sitter.Node, receiver string) {
	children := nodeChildren(root)
	if root != nil && root.IsError() {
		// Recovery can flatten an unfinished body into ERROR alongside its local
		// declarations. Only declarations outside actual brace tokens are peers.
		children = nil
		depth := 0
		var recover func(*sitter.Node)
		recover = func(n *sitter.Node) {
			if n.IsError() {
				for i := uint(0); i < n.ChildCount(); i++ {
					recover(n.Child(i))
				}
				return
			}
			switch n.Kind() {
			case "{":
				depth++
			case "}":
				depth = max(0, depth-1)
			default:
				if depth == 0 && n.IsNamed() {
					children = append(children, n)
				}
			}
		}
		recover(root)
	}
	from := -1
	for _, n := range children {
		switch n.Kind() {
		case "attribute_item":
			if from < 0 {
				from = int(n.StartByte())
			}
			continue
		case "line_comment", "block_comment":
			continue
		}
		if from < 0 {
			from = int(n.StartByte())
		}
		s.rustDeclaration(n, from, receiver)
		from = -1
	}
}

func (s *parsedSource) rustDeclaration(n *sitter.Node, from int, receiver string) {
	add := func(kind string, name *sitter.Node) {
		if name != nil && !name.IsMissing() {
			s.add(kind, s.text(name), from, int(n.EndByte()), int(name.StartByte()), int(name.EndByte()), !n.HasError())
			s.entries[len(s.entries)-1].receiver = receiver
		}
	}
	switch n.Kind() {
	case "function_item", "function_signature_item":
		kind := "function"
		if receiver != "" {
			kind = "method"
		}
		add(kind, n.ChildByFieldName("name"))
	case "ERROR":
		s.rustDeclarations(n, receiver)
	case "foreign_mod_item":
		if receiver == "" {
			s.rustDeclarations(n.ChildByFieldName("body"), "")
		}
	case "impl_item":
		if receiver == "" {
			s.rustDeclarations(n.ChildByFieldName("body"), s.rustReceiver(n.ChildByFieldName("type")))
		}
	default:
		if receiver != "" {
			return // Associated constants/types and nested declarations are not methods.
		}
		switch n.Kind() {
		case "struct_item", "enum_item", "union_item", "type_item", "trait_item":
			name := n.ChildByFieldName("name")
			add("type", name)
			if n.Kind() == "trait_item" && name != nil {
				s.rustDeclarations(n.ChildByFieldName("body"), s.text(name))
			}
		case "const_item":
			add("constant", n.ChildByFieldName("name"))
		case "static_item":
			add("variable", n.ChildByFieldName("name"))
		case "mod_item":
			add("module", n.ChildByFieldName("name"))
		case "macro_definition":
			add("macro", n.ChildByFieldName("name"))
		case "extern_crate_declaration":
			name := n.ChildByFieldName("alias")
			if name == nil {
				name = n.ChildByFieldName("name")
			}
			add("import", name)
		case "use_declaration":
			pathName := func(path *sitter.Node) *sitter.Node {
				if path != nil && path.Kind() == "scoped_identifier" {
					return path.ChildByFieldName("name")
				}
				return path
			}
			var visit func(*sitter.Node, *sitter.Node)
			visit = func(argument, scope *sitter.Node) {
				if argument == nil {
					return
				}
				switch argument.Kind() {
				case "use_as_clause":
					add("import", argument.ChildByFieldName("alias"))
				case "scoped_identifier":
					name := argument.ChildByFieldName("name")
					if name != nil && name.Kind() == "self" {
						name = pathName(argument.ChildByFieldName("path"))
					}
					add("import", name)
				case "self":
					if scope != nil {
						add("import", scope)
					} else {
						add("import", argument)
					}
				case "identifier", "super", "crate":
					add("import", argument)
				case "use_wildcard":
					s.add("import", "*", from, int(n.EndByte()), -1, -1, !n.HasError())
				case "scoped_use_list":
					visit(argument.ChildByFieldName("list"), pathName(argument.ChildByFieldName("path")))
				case "use_list":
					for _, child := range nodeChildren(argument) {
						visit(child, scope)
					}
				}
			}
			visit(n.ChildByFieldName("argument"), nil)
		}
	}
}

// Receivers are type navigation names, never raw type expressions. In particular,
// generic arguments and array lengths can contain values or entire Rust bodies.
func (s *parsedSource) rustReceiver(n *sitter.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "identifier", "type_identifier", "primitive_type", "self", "super", "crate":
		return s.text(n)
	case "generic_type":
		return s.rustReceiver(n.ChildByFieldName("type"))
	case "scoped_type_identifier", "scoped_identifier":
		return s.rustReceiver(n.ChildByFieldName("path")) + "::" + s.rustReceiver(n.ChildByFieldName("name"))
	case "reference_type":
		return "&" + s.rustReceiver(n.ChildByFieldName("type"))
	case "pointer_type":
		return "*" + s.rustReceiver(n.ChildByFieldName("type"))
	case "array_type":
		return "[" + s.rustReceiver(n.ChildByFieldName("element")) + "]"
	case "tuple_type":
		var types []string
		for _, child := range nodeChildren(n) {
			types = append(types, s.rustReceiver(child))
		}
		return "(" + strings.Join(types, ", ") + ")"
	case "unit_type":
		return "()"
	case "dynamic_type", "abstract_type":
		return s.rustReceiver(n.ChildByFieldName("trait"))
	case "function_type":
		if trait := n.ChildByFieldName("trait"); trait != nil {
			return s.rustReceiver(trait)
		}
		return "fn"
	case "bracketed_type":
		return "<" + s.rustReceiver(n.NamedChild(0)) + ">"
	case "qualified_type":
		return s.rustReceiver(n.ChildByFieldName("type")) + " as " + s.rustReceiver(n.ChildByFieldName("alias"))
	case "bounded_type":
		var types []string
		for _, child := range nodeChildren(n) {
			if child.Kind() != "lifetime" && child.Kind() != "use_bounds" {
				types = append(types, s.rustReceiver(child))
			}
		}
		return strings.Join(types, " + ")
	case "never_type":
		return "!"
	}
	return "_"
}
func (s *parsedSource) codeDeclaration(n, span *sitter.Node) {
	kind := n.Kind()
	if kind == "export_statement" || kind == "ambient_declaration" || kind == "decorated_definition" {
		for _, child := range nodeChildren(n) {
			if child.Kind() != "decorator" {
				s.codeDeclaration(child, span)
			}
		}
		return
	}
	switch kind {
	case "import_statement", "import_from_statement":
		if s.format.Language == "python" {
			for _, c := range nodeChildren(n) {
				if c.Kind() == "dotted_name" || c.Kind() == "aliased_import" {
					if module := n.ChildByFieldName("module_name"); module != nil && module.StartByte() == c.StartByte() {
						continue
					}
					name := c.ChildByFieldName("alias")
					if name == nil {
						name = c.NamedChild(0)
					}
					s.addNode("import", n, n, name, "")
				}
			}
		} else {
			var names []*sitter.Node
			var collect func(*sitter.Node)
			collect = func(c *sitter.Node) {
				switch c.Kind() {
				case "import_specifier":
					name := c.ChildByFieldName("alias")
					if name == nil {
						name = c.ChildByFieldName("name")
					}
					if name != nil {
						names = append(names, name)
					}
					return
				case "namespace_import":
					for _, v := range nodeChildren(c) {
						if v.Kind() == "identifier" {
							names = append(names, v)
						}
					}
					return
				case "identifier":
					names = append(names, c)
					return
				case "string":
					return
				}
				for _, v := range nodeChildren(c) {
					collect(v)
				}
			}
			for _, c := range nodeChildren(n) {
				if c.Kind() == "import_clause" {
					collect(c)
				}
			}
			for _, name := range names {
				s.addNode("import", n, span, name, "")
			}
			if len(names) == 0 {
				if source := n.ChildByFieldName("source"); source != nil {
					if value, ok := decodeJSString(s.text(source)); ok {
						s.add("import", value, int(span.StartByte()), int(span.EndByte()), -1, -1, !n.HasError())
					}
				}
			}
		}
	case "lexical_declaration", "variable_declaration":
		entryKind := "variable"
		if n.Child(0) != nil && n.Child(0).Kind() == "const" {
			entryKind = "constant"
		}
		for _, d := range nodeChildren(n) {
			if d.Kind() == "variable_declarator" {
				for _, name := range bindingNodes(d.ChildByFieldName("name")) {
					s.addNode(entryKind, n, span, name, "")
				}
			}
		}
	case "function_declaration", "generator_function_declaration", "function_signature", "function_definition":
		s.addNode("function", n, span, n.ChildByFieldName("name"), "")
	case "class_declaration", "abstract_class_declaration", "class_definition":
		name := n.ChildByFieldName("name")
		s.addNode("class", n, span, name, "")
		receiver := s.text(name)
		for _, candidate := range nodeChildren(n.ChildByFieldName("body")) {
			method := candidate
			if method.Kind() == "decorated_definition" {
				method = method.ChildByFieldName("definition")
				if method == nil {
					continue
				}
			}
			if method.Kind() == "method_definition" || method.Kind() == "method_signature" || method.Kind() == "abstract_method_signature" || method.Kind() == "function_definition" {
				s.addNode("method", method, candidate, method.ChildByFieldName("name"), receiver)
			}
		}
	case "type_alias_declaration", "interface_declaration", "enum_declaration", "type_alias_statement":
		s.addNode("type", n, span, n.ChildByFieldName("name"), "")
	case "expression_statement":
		if s.format.Language == "python" {
			for _, c := range nodeChildren(n) {
				s.pythonAssignment(c, span)
			}
		}
	case "assignment":
		if s.format.Language == "python" {
			s.pythonAssignment(n, span)
		}
	case "ERROR": // Recovery may leave otherwise complete top-level declarations here.
		for _, c := range nodeChildren(n) {
			s.codeDeclaration(c, c)
		}
	}
}
func (s *parsedSource) pythonAssignment(n, span *sitter.Node) {
	if n.Kind() != "assignment" {
		return
	}
	for _, name := range bindingNodes(n.ChildByFieldName("left")) {
		s.addNode("variable", n, span, name, "")
	}
	if right := n.ChildByFieldName("right"); right != nil {
		s.pythonAssignment(right, span)
	}
}

// outlineString serializes the otherwise unrepresentable lone-surrogate names
// accepted by ECMAScript and JSON without corrupting them into replacement text.
type outlineString string

func (s outlineString) MarshalJSON() ([]byte, error) {
	value := string(s)
	if utf8.ValidString(value) {
		return json.Marshal(value)
	}
	var out strings.Builder
	out.WriteByte('"')
	for len(value) > 0 {
		if len(value) >= 3 && value[0] == 0xed && value[1] >= 0xa0 && value[1] <= 0xbf && value[2] >= 0x80 && value[2] <= 0xbf {
			unit := uint16(value[0]&15)<<12 | uint16(value[1]&63)<<6 | uint16(value[2]&63)
			fmt.Fprintf(&out, `\u%04x`, unit)
			value = value[3:]
			continue
		}
		r, n := utf8.DecodeRuneInString(value)
		encoded, err := json.Marshal(string(r))
		if err != nil {
			return nil, err
		}
		out.Write(encoded[1 : len(encoded)-1])
		value = value[n:]
	}
	out.WriteByte('"')
	return []byte(out.String()), nil
}
