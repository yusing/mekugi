package router

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// execSourceScope derives path values from syntax, never from evaluation. Unknown
// expressions remain open; only derived paths are compared after execution.
type execSourceScope struct {
	input                       execProviderInput
	source                      []byte
	script                      string
	python                      bool
	vars                        map[string][]string
	segments                    map[string][]string // Raw strings, before cwd resolution, for path joins.
	pythonItems                 map[string][2]execPythonValues
	texts                       map[string]bool
	assigned                    map[string]int
	aliases                     map[string]string
	result                      execProviderResult
	iterations                  []string
	intentOnly                  bool              // Display classification: no filesystem or nested provider inspection.
	temporaryEnv                map[string]string // Display-only symbolic roots from explicit shell mktemp bindings.
	temporaryWrites             bool
	writes                      bool
	walkDepth, pathDepth, nodes int
}

func inspectExecSource(input execProviderInput, source, script string, language *sitter.Language, python bool) execProviderResult {
	if len(source) > maxExecProgramBytes {
		return execProviderResult{open: true, reason: "interpreter source exceeds capture bound"}
	}
	data := []byte(source)
	tree, err := parseSourceTree(data, language, func() bool { return time.Now().After(input.deadline) })
	if err != nil {
		return execProviderResult{open: true, reason: "interpreter parser unavailable"}
	}
	if tree == nil {
		return execProviderResult{open: true, reason: "interpreter parse deadline"}
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return execProviderResult{open: true, reason: "interpreter source could not be parsed"}
	}
	scan := execSourceScope{input: input, source: data, script: script, python: python, vars: make(map[string][]string), texts: make(map[string]bool), assigned: make(map[string]int), aliases: make(map[string]string)}
	scan.walk(tree.RootNode())
	// The command names its script. Retaining that bounded baseline also
	// distinguishes an unchanged source at the filesystem clock boundary.
	if script != "" && !input.authoredOnly {
		scan.result.scope = append(scan.result.scope, execProviderFiles([]string{script}, false))
	}
	if scan.writes && scan.result.open && !input.authoredOnly {
		for _, root := range scan.iterations {
			scan.result.scope = append(scan.result.scope, execProviderFiles([]string{root}, true))
		}
	}
	if scan.result.open {
		scan.result.reason = "unresolved interpreter targets"
	}
	return scan.result
}

func (s *execSourceScope) text(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	return node.Utf8Text(s.source)
}

func sourceCall(node *sitter.Node) (*sitter.Node, []*sitter.Node) {
	if node == nil || node.Kind() != "call" && node.Kind() != "call_expression" {
		return nil, nil
	}
	function := node.ChildByFieldName("function")
	args := node.ChildByFieldName("arguments")
	var values []*sitter.Node
	if args != nil {
		for i := range args.NamedChildCount() {
			values = append(values, args.NamedChild(uint(i)))
		}
	}
	return function, values
}

func (s *execSourceScope) literal(node *sitter.Node) (string, bool) {
	if node == nil {
		return "", false
	}
	if !s.python {
		value, ok := toolActivityStaticJavaScriptValue(node, s.source)
		str, isString := value.(string)
		return str, ok && isString
	}
	if node.Kind() != "string" {
		return "", false
	}
	text := s.text(node)
	raw := false
	for len(text) != 0 && text[0] != '\'' && text[0] != '"' {
		switch text[0] {
		case 'r', 'R':
			raw = true
		case 'b', 'B', 'u', 'U':
		default:
			return "", false
		}
		text = text[1:]
	}
	if len(text) < 2 {
		return "", false
	}
	quote := text[0]
	delim := 1
	if len(text) >= 6 && text[0] == text[1] && text[1] == text[2] {
		delim = 3
	}
	if !strings.HasSuffix(text, strings.Repeat(string(quote), delim)) {
		return "", false
	}
	text = text[delim : len(text)-delim]
	if raw {
		return text, true
	}
	var out strings.Builder
	for text != "" {
		if text[0] != '\\' {
			out.WriteByte(text[0])
			text = text[1:]
			continue
		}
		char, _, tail, err := strconv.UnquoteChar(text, quote)
		if err != nil {
			return "", false
		}
		out.WriteRune(char)
		text = tail
	}
	return out.String(), true
}

func (s *execSourceScope) paths(node *sitter.Node) []string {
	s.pathDepth++
	defer func() { s.pathDepth-- }()
	if s.pathDepth > 256 {
		s.result.open = true
		return nil
	}
	if node == nil {
		return nil
	}
	if s.intentOnly && s.python && node.Kind() == "subscript" && s.text(node.ChildByFieldName("value")) == "os.environ" {
		if key, ok := s.literal(node.ChildByFieldName("subscript")); ok && s.temporaryEnv[key] != "" {
			return []string{s.temporaryEnv[key]}
		}
	}
	if value, ok := s.literal(node); ok {
		if s.intentOnly {
			return []string{value}
		}
		return []string{execProviderPath(s.input.cwd, value)}
	}
	if node.Kind() == "identifier" {
		if s.text(node) == "__dirname" && s.script != "" {
			return []string{filepath.Dir(s.script)}
		}
		return s.vars[s.text(node)]
	}
	if node.Kind() == "list" || node.Kind() == "tuple" || node.Kind() == "array" {
		var paths []string
		for i := range node.NamedChildCount() {
			values := s.paths(node.NamedChild(uint(i)))
			if len(values) == 0 {
				return nil
			}
			paths = append(paths, values...)
		}
		return paths
	}
	if s.text(node) == "import.meta.dirname" && s.script != "" {
		return []string{filepath.Dir(s.script)}
	}
	if node.Kind() == "parenthesized_expression" && node.NamedChildCount() == 1 {
		return s.paths(node.NamedChild(0))
	}
	if node.Kind() == "binary_operator" && s.text(node.ChildByFieldName("operator")) == "/" {
		left := s.paths(node.ChildByFieldName("left"))
		right := s.literalSegments(node.ChildByFieldName("right"), 0)
		if len(left) != 0 && len(right) > maxExecListingEntries/len(left) {
			s.result.open = true
			return nil
		}
		var paths []string
		for _, segment := range right {
			paths = append(paths, s.joinPaths(left, segment)...)
		}
		return paths
	}
	function, args := sourceCall(node)
	name := s.text(function)
	base := name[strings.LastIndexByte(name, '.')+1:]
	if alias := s.aliases[base]; alias != "" {
		base = alias
	}
	if (base == "Path" || base == "PurePath" || base == "open" || base == "file") && len(args) > 0 {
		if s.intentOnly && len(s.temporaryEnv) != 0 && (base == "Path" || base == "PurePath") && len(args) != 1 {
			// An additional constructor segment can override the temporary
			// root. Unsupported constructors must keep unknown edit intent.
			s.result.open = true
			return nil
		}
		return s.paths(args[0])
	}
	if base == "join" || base == "resolve" {
		var paths []string
		for i, arg := range args {
			if i == 0 {
				paths = s.paths(arg)
				continue
			}
			segment, ok := s.literal(arg)
			if !ok {
				return nil
			}
			paths = s.joinPaths(paths, segment)
		}
		return paths
	}
	if base == "glob" || base == "rglob" || base == "iglob" || base == "globSync" {
		if len(args) == 0 {
			return nil
		}
		pattern, ok := s.literal(args[0])
		if !ok {
			return nil
		}
		root := s.input.cwd
		if receiver := function.ChildByFieldName("object"); receiver != nil {
			if paths := s.paths(receiver); len(paths) == 1 {
				root = paths[0]
			}
		}
		if base == "rglob" {
			pattern = "**/" + pattern
		}
		return s.glob(root, pattern)
	}
	if slices.Contains([]string{"iterdir", "walk", "scandir", "listdir", "readdir", "readdirSync", "opendir"}, base) {
		var roots []string
		if len(args) > 0 {
			roots = s.paths(args[0])
		} else if function != nil {
			roots = s.paths(function.ChildByFieldName("object"))
		}
		var paths []string
		for _, root := range roots {
			paths = append(paths, s.glob(root, "**/*")...)
		}
		return paths
	}
	return nil
}

// literalSegments preserves relative and absolute string values separately from
// filesystem operands. It never evaluates expressions or reads the filesystem.
func (s *execSourceScope) literalSegments(node *sitter.Node, depth int) []string {
	if node == nil || depth > 256 {
		return nil
	}
	if value, ok := s.literal(node); ok {
		return []string{value}
	}
	if node.Kind() == "identifier" {
		return s.segments[s.text(node)]
	}
	if node.Kind() != "list" && node.Kind() != "tuple" && node.Kind() != "array" {
		return nil
	}
	var values []string
	for i := range node.NamedChildCount() {
		part := s.literalSegments(node.NamedChild(uint(i)), depth+1)
		if len(part) == 0 || len(values)+len(part) > maxExecListingEntries {
			return nil
		}
		values = append(values, part...)
	}
	return values
}

func joinExecPaths(paths []string, segment string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if filepath.IsAbs(segment) {
			result = append(result, filepath.Clean(segment))
		} else {
			result = append(result, filepath.Join(path, segment))
		}
	}
	return result
}

func (s *execSourceScope) joinPaths(paths []string, segment string) []string {
	for _, path := range paths {
		if strings.ContainsRune(path, '\x00') && slices.Contains(strings.Split(filepath.ToSlash(segment), "/"), "..") {
			// Parent traversal cannot turn a symbolic root into a concrete path.
			s.result.open = true
			return nil
		}
	}
	return joinExecPaths(paths, segment)
}

func (s *execSourceScope) glob(root, pattern string) []string {
	if s.intentOnly || !filepath.IsAbs(root) {
		return nil
	}
	if filepath.IsAbs(pattern) {
		root = string(filepath.Separator)
		pattern = strings.TrimPrefix(pattern, root)
	}
	expression, ok := parseExecIgnoreLine(pattern)
	if !ok {
		s.result.open = true
		return nil
	}
	s.iterations = append(s.iterations, root)
	var paths []string
	entries := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		entries++
		if entries > maxExecListingEntries || time.Now().After(s.input.deadline) {
			s.result.open = true
			return filepath.SkipAll
		}
		if err != nil {
			s.result.open = true
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if expression.pattern.MatchString(filepath.ToSlash(rel)) {
			paths = append(paths, path)
		}
		return nil
	})
	return paths
}

func (s *execSourceScope) add(node *sitter.Node, tree bool) {
	s.writes = true
	paths := s.paths(node)
	if len(paths) == 0 || slices.Contains(paths, "") {
		s.result.open = true
		return
	}
	if s.intentOnly && len(s.temporaryEnv) != 0 {
		var ordinary []string
		for _, path := range paths {
			temporary := false
			for _, root := range s.temporaryEnv {
				if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
					temporary = true
					break
				}
			}
			if temporary {
				s.temporaryWrites = true
			} else if strings.ContainsRune(path, '\x00') {
				s.result.open = true
			} else {
				ordinary = append(ordinary, path)
			}
		}
		paths = ordinary
		if len(paths) == 0 {
			return
		}
	}
	s.result.scope = append(s.result.scope, execProviderFiles(paths, tree))
}

func (s *execSourceScope) walk(node *sitter.Node) {
	s.nodes++
	s.walkDepth++
	defer func() { s.walkDepth-- }()
	if s.walkDepth > 256 || s.nodes > 50_000 {
		s.result.open = true
		return
	}
	if node == nil {
		return
	}
	if s.intentOnly && len(s.temporaryEnv) != 0 {
		// Proof is limited to straight-line source with an unmodified os
		// module and environment. A rebinding or nested scope invalidates it.
		if node.Kind() == "function_definition" || node.Kind() == "class_definition" || node.Kind() == "if_statement" || node.Kind() == "for_statement" || node.Kind() == "while_statement" || node.Kind() == "try_statement" {
			s.result.open = true
		}
		if node.Kind() == "assignment" || node.Kind() == "augmented_assignment" {
			left := s.text(node.ChildByFieldName("left"))
			if left == "os" || left == "Path" || left == "PurePath" || left == "open" || s.aliases[left] == "Path" || s.aliases[left] == "PurePath" || strings.HasPrefix(left, "os.") {
				s.result.open = true
			}
		}
		if function, _ := sourceCall(node); function != nil && strings.HasPrefix(s.text(function), "os.environ.") {
			s.result.open = true
		}
		if node.Kind() == "attribute" && s.text(node) == "os.environ" {
			parent := node.Parent()
			if parent == nil || parent.Kind() != "subscript" {
				// An environment object that escapes a direct key read can be
				// mutated through aliases. Do not claim it still names mktemp.
				s.result.open = true
			}
		}
		if node.Kind() == "identifier" && s.text(node) == "os" {
			parent := node.Parent()
			if parent == nil || parent.Kind() != "attribute" && parent.Kind() != "dotted_name" {
				s.result.open = true
			}
		}
		if node.Kind() == "aliased_import" && s.text(node.ChildByFieldName("name")) == "os" || node.Kind() == "import_from_statement" && s.text(node.ChildByFieldName("module_name")) == "os" {
			s.result.open = true
		}
	}
	if time.Now().After(s.input.deadline) {
		s.result.open = true
		return
	}
	if node.Kind() == "import_specifier" || node.Kind() == "aliased_import" {
		name, alias := node.ChildByFieldName("name"), node.ChildByFieldName("alias")
		if name != nil && alias != nil {
			s.aliases[s.text(alias)] = s.text(name)
		}
	}
	if node.Kind() == "pair_pattern" {
		key, value := node.ChildByFieldName("key"), node.ChildByFieldName("value")
		if key != nil && value != nil && value.Kind() == "identifier" {
			s.aliases[s.text(value)] = s.text(key)
		}
	}
	if node.Kind() == "augmented_assignment" {
		if s.python {
			left := node.ChildByFieldName("left")
			if left != nil && left.Kind() == "subscript" {
				delete(s.pythonItems, s.text(left.ChildByFieldName("value")))
			}
		}
		if left := node.ChildByFieldName("left"); left != nil && left.Kind() == "identifier" {
			name := s.text(left)
			delete(s.vars, name)
			delete(s.texts, name)
			delete(s.segments, name)
			delete(s.pythonItems, name)
		}
	}
	if node.Kind() == "assignment" || node.Kind() == "variable_declarator" {
		left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
		if !s.python {
			left, right = node.ChildByFieldName("name"), node.ChildByFieldName("value")
		}
		if left != nil && left.Kind() == "identifier" {
			name := s.text(left)
			s.assigned[name]++
			// A top-level Python assignment replaces the value before the next
			// statement. Keep each write's scope as we walk; reassignment need
			// not discard literal paths in ordinary sequential rewrite scripts.
			statement := node.Parent()
			straightLine := s.python && statement != nil && statement.Kind() == "expression_statement" &&
				statement.Parent() != nil && statement.Parent().Kind() == "module"
			if s.assigned[name] == 1 || straightLine {
				text := s.python && s.pythonText(right, 0)
				segments := s.literalSegments(right, 0)
				paths := s.paths(right)
				if s.segments == nil {
					s.segments = make(map[string][]string)
				}
				s.segments[name] = segments
				s.vars[name] = paths
				s.texts[name] = text
				if s.python {
					delete(s.pythonItems, name)
					if items, ok := s.pythonDictionaryItems(right); ok {
						if s.pythonItems == nil {
							s.pythonItems = make(map[string][2]execPythonValues)
						}
						s.pythonItems[name] = items
					}
				}
			} else {
				delete(s.vars, name)
				delete(s.texts, name)
				delete(s.segments, name)
				delete(s.pythonItems, name)
			}
		}
		if s.python && left != nil && left.Kind() == "subscript" {
			delete(s.pythonItems, s.text(left.ChildByFieldName("value")))
		}
		if s.python && right != nil && right.Kind() == "identifier" {
			// Do not retain a snapshot once the mutable dictionary is aliased.
			delete(s.pythonItems, s.text(right))
		}
	}
	if node.Kind() == "for_statement" || node.Kind() == "for_in_statement" {
		left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
		if left != nil && left.Kind() == "identifier" {
			segments := s.literalSegments(right, 0)
			paths := s.paths(right)
			if s.segments == nil {
				s.segments = make(map[string][]string)
			}
			s.segments[s.text(left)] = segments
			s.vars[s.text(left)] = paths
			delete(s.texts, s.text(left))
			delete(s.pythonItems, s.text(left))
		} else if s.python && left != nil {
			s.bindPythonItems(left, right)
		}
	}
	if function, args := sourceCall(node); function != nil {
		s.call(function, args)
	}
	for i := range node.NamedChildCount() {
		s.walk(node.NamedChild(uint(i)))
	}
}

func (s *execSourceScope) call(function *sitter.Node, args []*sitter.Node) {
	name := s.text(function)
	base := name[strings.LastIndexByte(name, '.')+1:]
	if alias := s.aliases[base]; alias != "" {
		base = alias
	}
	if s.intentOnly {
		// Capture deliberately over-approximates ambiguous methods such as
		// list.remove and stream.write. Display only recognizes file-writing
		// APIs; those conservative guesses must not hide ordinary commands.
		if s.python {
			switch base {
			case "open":
				if name != "open" && len(s.paths(function.ChildByFieldName("object"))) == 0 {
					return
				}
			case "write_text", "write_bytes":
			default:
				return
			}
		} else {
			switch strings.TrimSuffix(base, "Sync") {
			case "writeFile", "appendFile", "truncate", "createWriteStream":
			case "open":
				if name != "fs.open" && name != "fs.openSync" && name != "fs.promises.open" {
					return
				}
			default:
				if name != "Bun.write" {
					return
				}
			}
		}
	}
	arg := func(index int) *sitter.Node {
		if index < len(args) {
			return args[index]
		}
		return nil
	}
	if base == "chdir" {
		paths := s.paths(arg(0))
		if len(paths) == 1 {
			s.input.cwd = paths[0]
		} else {
			s.input.cwd = ""
			s.result.open = true
		}
		return
	}
	if base == "eval" || base == "Function" || base == "import" {
		s.result.open = true
	}
	if s.text(function) == "os.system" || strings.HasPrefix(name, "subprocess.") || slices.Contains([]string{"exec", "execSync", "execFile", "execFileSync", "spawn", "spawnSync"}, base) {
		s.subprocess(args)
		return
	}
	if base == "require" {
		if _, ok := s.literal(arg(0)); !ok {
			s.result.open = true
		}
	}
	if s.python {
		s.pythonCall(function, base, args)
		return
	}
	base = strings.TrimSuffix(base, "Sync")
	switch base {
	case "writeFile", "appendFile", "truncate", "mkdir", "rmdir", "rm", "unlink", "createWriteStream":
		s.add(arg(0), base == "rm" || base == "rmdir")
	case "open":
		mode, ok := s.literal(arg(1))
		if ok && strings.ContainsAny(mode, "wax+") {
			s.add(arg(0), false)
		} else if !ok {
			s.result.open = true
		}
	case "rename":
		s.add(arg(0), true)
		s.add(arg(1), true)
	case "copyFile", "cp", "symlink", "link":
		s.add(arg(1), base == "cp")
	case "write", "delete":
		if name == "Bun.write" {
			s.add(arg(0), false)
		} else {
			s.add(function.ChildByFieldName("object"), false)
		}
	}
}

func (s *execSourceScope) subprocess(args []*sitter.Node) {
	if s.intentOnly {
		return
	}
	if len(args) == 0 {
		s.result.open = true
		return
	}
	var command string
	if value, ok := s.literal(args[0]); ok {
		command = value
		// execFile/spawn use a separate argv array rather than shell text.
		if len(args) > 1 && (args[1].Kind() == "array" || args[1].Kind() == "list") {
			command = shellQuoteArgument(value)
			for i := range args[1].NamedChildCount() {
				part, ok := s.literal(args[1].NamedChild(uint(i)))
				if !ok {
					s.result.open = true
					return
				}
				command += " " + shellQuoteArgument(part)
			}
		}
	} else if args[0].Kind() == "list" || args[0].Kind() == "array" || args[0].Kind() == "tuple" {
		for i := range args[0].NamedChildCount() {
			part, ok := s.literal(args[0].NamedChild(uint(i)))
			if !ok {
				s.result.open = true
				return
			}
			command += " " + shellQuoteArgument(part)
		}
	} else {
		s.result.open = true
		return
	}
	plan := classifyExecShellSource(command, s.input.cwd, "bash", s.input.deadline, s.input.depth+1, s.input.changes, s.input.authoredOnly)
	s.result.scope = append(s.result.scope, plan.Scope...)
	s.result.programs = append(s.result.programs, plan.Programs...)
	s.result.open = s.result.open || plan.Class == execOpaque || plan.Reason != ""
	s.writes = s.writes || plan.Class != execNeutral
}
