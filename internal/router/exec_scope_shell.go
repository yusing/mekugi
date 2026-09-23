package router

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// execClass orders how much of a command's file effect is known statically.
// A program takes the most open class of its statements.
type execClass int

const (
	execNeutral execClass = iota
	execDeclared
	execScoped
	execOpaque
)

func (c execClass) String() string {
	switch c {
	case execNeutral:
		return "neutral"
	case execDeclared:
		return "declared"
	case execScoped:
		return "scoped"
	default:
		return "opaque"
	}
}

// execOperand is an absolute path, or a shell glob expanded read-only at
// capture time.
type execOperand struct {
	Path          string
	Glob          bool `json:",omitzero"`
	AbsoluteInput bool `json:",omitzero"`
}

type execScopeKind string

const (
	// execScopeFile captures each operand path itself.
	execScopeFile execScopeKind = "file"
	// execScopeTree captures each operand, and every file below it when it is
	// a directory.
	execScopeTree execScopeKind = "tree"
	// execScopeInto resolves coreutils destination rules at capture time:
	// each source maps to Dest, or to Dest/basename(source) when Dest is a
	// directory.
	execScopeInto execScopeKind = "into"
)

// execScopeEntry is one statically derived part of a command's write scope.
// Destinations depend on filesystem state, so they resolve at capture time.
type execScopeEntry struct {
	Origin   string `json:",omitempty"`
	Kind     execScopeKind
	Operands []execOperand
	Dest     string `json:",omitempty"`
	// DestDir forces directory semantics (-t, or several sources).
	DestDir bool `json:",omitzero"`
	// NoTarget forbids directory semantics (-T).
	NoTarget      bool `json:",omitzero"`
	NoDereference bool `json:",omitzero"`
	// Recursive maps whole source trees to the destination.
	Recursive bool `json:",omitzero"`
	// Sources marks sources as changed too (mv).
	Sources bool `json:",omitzero"`
	// Copies marks targets that receive their source's content (cp, install).
	Copies bool `json:",omitzero"`
	// Backup adds DEST~ style backups written by -b.
	Backup string `json:",omitempty"`
	// Through marks writes that follow an existing symlink to its target.
	Through bool `json:",omitzero"`
}

type execPlan struct {
	Class  execClass
	Labels []string
	Scope  []execScopeEntry
	Reason string
	// Programs are the statements whose effects are not derived, in order.
	Programs []execProgram
}

// execProgram names an undeclared statement for effect attribution.
type execProgram struct {
	Label string
	// Direct marks statements whose targets and content the agent chose:
	// interpreter programs, inline shell, and file operations with operands
	// only known at run time. Other programs are tool-managed.
	Direct bool `json:",omitzero"`
}

func (p *execPlan) raise(class execClass, reason string) {
	if class > p.Class {
		p.Class = class
	}
	if reason != "" && !strings.Contains(p.Reason, reason) {
		if p.Reason != "" {
			p.Reason += "; "
		}
		p.Reason += reason
	}
}

func (p *execPlan) label(name string) {
	if !slices.Contains(p.Labels, name) {
		p.Labels = append(p.Labels, name)
	}
}

func (p *execPlan) add(entry execScopeEntry) {
	p.Scope = append(p.Scope, entry)
}

func classifyExecShellWithin(command, workdir, shell string, deadline time.Time, depth int) execPlan {
	plan := execPlan{}
	switch shellsyntax.InterpreterIdentity(shell) {
	case "bash", "sh":
	case "":
		plan.raise(execOpaque, "session shell is unknown")
		return plan
	default:
		plan.raise(execOpaque, "shell "+shell+" is not parsed")
		return plan
	}
	if !filepath.IsAbs(workdir) {
		plan.raise(execOpaque, "relative operands require an absolute working directory")
		return plan
	}
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		plan.raise(execOpaque, "command does not parse as bash")
		return plan
	}
	// A substitution anywhere, including heredoc bodies and test clauses,
	// runs a command whose effect is not inspected.
	substitution := false
	syntax.Walk(program, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			substitution = true
		}
		return !substitution
	})
	if substitution {
		plan.raise(execOpaque, "command substitution")
		return plan
	}
	walker := execShellWalker{cwd: filepath.Clean(workdir), plan: &plan, deadline: deadline, depth: depth}
	walker.stmts(program.Stmts)
	return plan
}

// execShellWalker resolves operands against the directory the shell is known
// to be in. cwd is empty once a cd may or may not have taken effect; relative
// operands are then opaque.
type execShellWalker struct {
	depth    int
	deadline time.Time
	stdin    string
	cwd      string
	plan     *execPlan
	// moves counts cd commands walked, so a list can tell that the directory
	// may have changed even when it returns to the same path.
	moves int
	// program attributes an undeclared current statement.
	program execProgram
	// functions are shell functions defined earlier in the command.
	functions []string
}

// opaque marks the current statement as undeclared. Walking continues, so
// later statements still contribute their declared scope and labels.
func (w *execShellWalker) opaque(reason string) {
	w.plan.raise(execOpaque, reason)
	program := w.program
	if program.Label == "" {
		program = execProgram{Label: "shell", Direct: true}
	}
	if !slices.Contains(w.plan.Programs, program) {
		w.plan.Programs = append(w.plan.Programs, program)
	}
}

// stmts walks a list. A cd takes effect for later list items only when its
// failure ends the shell, as in cd dir || exit; otherwise later items may run
// in either directory.
func (w *execShellWalker) stmts(stmts []*syntax.Stmt) {
	for _, stmt := range stmts {
		moves := w.moves
		w.stmt(stmt)
		if w.moves != moves && !execGuardedCd(stmt) {
			w.cwd = ""
		}
	}
}

// loseDirectory records that a construct may have changed directory in ways
// the walker does not follow.
func (w *execShellWalker) loseDirectory() {
	w.cwd = ""
	w.moves++
}

// repeated walks a body that may run any number of times. A cd in one pass
// moves the next, so a moving body is walked again without a directory.
func (w *execShellWalker) repeated(walk func(*execShellWalker)) {
	moves := w.moves
	walk(w)
	if w.moves != moves {
		w.cwd = ""
		walk(w)
	}
}

// execGuardedCd matches cd DIR || exit and cd DIR || return.
func execGuardedCd(stmt *syntax.Stmt) bool {
	binary, ok := stmt.Cmd.(*syntax.BinaryCmd)
	if !ok || binary.Op != syntax.OrStmt {
		return false
	}
	name := func(stmt *syntax.Stmt) string {
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 || len(stmt.Redirs) != 0 || stmt.Background || stmt.Negated {
			return ""
		}
		value, _ := shellCatLiteral(call.Args[0])
		return value
	}
	exit := name(binary.Y)
	return name(binary.X) == "cd" && (exit == "exit" || exit == "return")
}

func (w *execShellWalker) stmt(stmt *syntax.Stmt) {
	w.program = execProgram{}
	w.stdin = ""
	for _, redirect := range stmt.Redirs {
		if body, ok := liveDiffShellHeredoc(redirect, false); ok {
			w.stdin = body
		}
	}
	if stmt.Background || stmt.Coprocess || stmt.Disown {
		// The job outlives the command, so its writes are not this call's.
		w.program = execProgram{Label: "background job"}
		if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Args) != 0 {
			if name, literal := shellCatLiteral(call.Args[0]); literal {
				w.program.Label = execProgramLabel(shellsyntax.InterpreterIdentity(name), call.Args[1:]) + " (background)"
			}
		}
		w.opaque("background process")
		return
	}
	writes := len(w.plan.Scope)
	for _, redirect := range stmt.Redirs {
		w.redirect(redirect)
	}
	if len(w.plan.Scope) != writes {
		label := "shell"
		if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Args) != 0 {
			if name, literal := shellCatLiteral(call.Args[0]); literal {
				label = shellsyntax.InterpreterIdentity(name)
			}
		}
		w.plan.label(label)
	}
	switch command := stmt.Cmd.(type) {
	case nil:
	case *syntax.CallExpr:
		w.call(command)
	case *syntax.BinaryCmd:
		// Both sides may run, so the scope is their union.
		switch command.Op {
		case syntax.Pipe, syntax.PipeAll:
			if w.producerPipeline(command) {
				return
			}
			// Pipeline sides run in subshells, so a cd there does not leak.
			w.subshell(func(inner *execShellWalker) { inner.stmt(command.X) })
			w.subshell(func(inner *execShellWalker) { inner.stmt(command.Y) })
		case syntax.AndStmt:
			// The right side runs only after the left side succeeded,
			// including any cd in it.
			w.stmt(command.X)
			w.stmt(command.Y)
		default:
			if execGuardedCd(stmt) {
				// exit or return ends the shell when cd fails.
				w.stmt(command.X)
				return
			}
			// The right side runs after the left side failed, possibly
			// after a cd in it took effect.
			moves := w.moves
			w.stmt(command.X)
			if w.moves != moves {
				w.cwd = ""
			}
			w.stmt(command.Y)
		}
	case *syntax.Block:
		w.stmts(command.Stmts)
	case *syntax.Subshell:
		w.subshell(func(inner *execShellWalker) { inner.stmts(command.Stmts) })
	case *syntax.IfClause:
		// Every branch may run; the scope is a superset, never a prediction.
		// Conditions of later clauses run after earlier ones failed.
		conditions := *w
		moves := w.moves
		for clause := command; clause != nil; clause = clause.Else {
			conditions.stmts(clause.Cond)
			branch := conditions
			branch.stmts(clause.Then)
			moves = max(moves, branch.moves)
		}
		if moves != w.moves {
			w.cwd, w.moves = "", moves
		}
	case *syntax.ForClause:
		if loop, ok := command.Loop.(*syntax.CStyleLoop); ok {
			for _, expression := range []syntax.ArithmExpr{loop.Init, loop.Cond, loop.Post} {
				if expression != nil && !execArithmStatic(expression) {
					w.opaque("dynamic loop")
				}
			}
		}
		w.repeated(func(inner *execShellWalker) { inner.stmts(command.Do) })
	case *syntax.WhileClause:
		w.repeated(func(inner *execShellWalker) {
			inner.stmts(command.Cond)
			inner.stmts(command.Do)
		})
	case *syntax.CaseClause:
		moves := w.moves
		for _, item := range command.Items {
			branch := *w
			branch.stmts(item.Stmts)
			moves = max(moves, branch.moves)
		}
		if moves != w.moves {
			w.cwd, w.moves = "", moves
		}
	case *syntax.FuncDecl:
		// A function body runs wherever it is called, so it is walked
		// without a directory; its calls are undeclared.
		body := execShellWalker{plan: w.plan}
		body.stmt(command.Body)
		w.functions = append(w.functions, command.Name.Value)
	case *syntax.TimeClause:
		if command.Stmt != nil {
			w.stmt(command.Stmt)
		}
	case *syntax.ArithmCmd, *syntax.LetClause:
		// Arithmetic assigns shell variables only.
	case *syntax.TestClause:
	case *syntax.DeclClause:
		for _, assign := range command.Args {
			if assign.Value != nil && !execWordStatic(assign.Value) || assign.Array != nil {
				w.opaque("dynamic declaration")
			}
			if execResolutionVariable(assign) {
				w.opaque("command resolution changes")
			}
		}
	default:
		w.opaque("unsupported shell construct")
		w.loseDirectory()
	}
}

func execArithmStatic(expression syntax.ArithmExpr) bool {
	static := true
	syntax.Walk(expression, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.CmdSubst); ok {
			static = false
		}
		return static
	})
	return static
}

// execResolutionVariable reports assignments that change which program a
// later command name runs.
func execResolutionVariable(assign *syntax.Assign) bool {
	return assign.Name != nil && (assign.Name.Value == "PATH" || assign.Name.Value == "BASH_ENV" || assign.Name.Value == "ENV")
}

func (w *execShellWalker) subshell(walk func(*execShellWalker)) {
	inner := execShellWalker{cwd: w.cwd, plan: w.plan, functions: w.functions, deadline: w.deadline, depth: w.depth}
	walk(&inner)
}

func (w *execShellWalker) redirect(redirect *syntax.Redirect) {
	if redirect.N != nil && !isDigits(redirect.N.Value) {
		w.opaque("named file descriptor")
		return
	}
	switch redirect.Op {
	case syntax.RdrIn, syntax.WordHdoc:
		if !execWordStatic(redirect.Word) {
			w.opaque("dynamic input")
		}
	case syntax.Hdoc, syntax.DashHdoc:
		if redirect.Hdoc != nil && !execHeredocStatic(redirect) {
			w.opaque("dynamic input")
		}
	case syntax.DplIn, syntax.DplOut:
		value, ok := shellCatLiteral(redirect.Word)
		if !ok || value != "-" && !isDigits(value) {
			w.opaque("dynamic descriptor duplication")
		}
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
		operand, ok := w.operand(redirect.Word)
		if !ok || operand.Glob {
			w.opaque("dynamic redirection target")
			return
		}
		if strings.HasPrefix(operand.Path, "/dev/") {
			return
		}
		w.plan.raise(execDeclared, "")
		w.plan.add(execScopeEntry{Kind: execScopeFile, Operands: []execOperand{operand}, Through: true})
	default:
		w.opaque("unsupported redirection")
	}
}

func isDigits(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == ""
}

// execHeredocStatic accepts heredoc bodies without expansions that could run
// commands. Quoted delimiters disable expansion entirely.
func execHeredocStatic(redirect *syntax.Redirect) bool {
	for _, part := range redirect.Word.Parts {
		switch part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		}
	}
	for _, part := range redirect.Hdoc.Parts {
		switch part.(type) {
		case *syntax.Lit, *syntax.ParamExp:
		default:
			return false
		}
	}
	return true
}

// execWordStatic reports whether a word cannot run a command when expanded.
// Parameter expansion is allowed here because only its value is unknown.
func execWordStatic(word *syntax.Word) bool {
	if word == nil {
		return true
	}
	static := true
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp:
			static = false
		}
		return static
	})
	return static
}

// operand resolves a literal or glob word against the walker's directory.
// Parameters, command substitution, tilde, and brace expansion are dynamic.
func (w *execShellWalker) operand(word *syntax.Word) (execOperand, bool) {
	value, glob, ok := execWordOperand(word)
	if !ok || value == "" {
		return execOperand{}, false
	}
	absolute := filepath.IsAbs(value)
	if !filepath.IsAbs(value) {
		if w.cwd == "" {
			return execOperand{}, false
		}
		value = filepath.Join(w.cwd, value)
	}
	if !glob {
		value = filepath.Clean(value)
	}
	return execOperand{Path: value, Glob: glob, AbsoluteInput: absolute}, true
}

// execDotOperand matches operands whose trailing . or .. names directory
// contents rather than an entry, which destination rules do not model.
func execDotOperand(word *syntax.Word) bool {
	value, _, _ := execWordOperand(word)
	value = strings.TrimRight(value, "/")
	return value == "." || value == ".." || strings.HasSuffix(value, "/.") || strings.HasSuffix(value, "/..")
}

// execWordOperand returns a word's literal value, or a filepath.Match pattern
// when unquoted glob characters appear. Quoted text is escaped in patterns.
func execWordOperand(word *syntax.Word) (value string, glob bool, ok bool) {
	if word == nil {
		return "", false, false
	}
	var pattern strings.Builder
	escape := func(text string) {
		for _, char := range text {
			if strings.ContainsRune(`*?[\`, char) {
				pattern.WriteByte('\\')
			}
			pattern.WriteRune(char)
		}
	}
	for index, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false, false
			}
			escape(part.Value)
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				literal, isLiteral := inner.(*syntax.Lit)
				if !isLiteral {
					return "", false, false
				}
				text, err := expand.Literal(&expand.Config{}, &syntax.Word{Parts: []syntax.WordPart{&syntax.DblQuoted{Parts: []syntax.WordPart{literal}}}})
				if err != nil {
					return "", false, false
				}
				escape(text)
			}
		case *syntax.Lit:
			text := part.Value
			if index == 0 && strings.HasPrefix(text, "~") {
				return "", false, false
			}
			if strings.ContainsAny(text, "{}") {
				return "", false, false
			}
			if strings.ContainsAny(text, "*?[") {
				// Character classes and globstar have no filepath.Match
				// form; bash negation is [! where Go uses [^.
				if strings.Contains(text, "[[:") || strings.Contains(text, "**") {
					return "", false, false
				}
				glob = true
				pattern.WriteString(strings.ReplaceAll(text, "[!", "[^"))
				continue
			}
			literal, err := expand.Literal(&expand.Config{}, &syntax.Word{Parts: []syntax.WordPart{part}})
			if err != nil {
				return "", false, false
			}
			escape(literal)
		default:
			return "", false, false
		}
	}
	if glob {
		return pattern.String(), true, true
	}
	literal, err := expand.Literal(&expand.Config{}, word)
	if err != nil {
		return "", false, false
	}
	return literal, false, true
}

// literalArgs returns all words as static strings, for options and scripts.
func literalArgs(words []*syntax.Word) ([]string, bool) {
	values := make([]string, 0, len(words))
	for _, word := range words {
		value, glob, ok := execWordOperand(word)
		if !ok {
			return nil, false
		}
		if glob {
			// Keep the literal spelling; callers that need operands resolve
			// them through operand().
			value = word.Lit()
			if value == "" {
				return nil, false
			}
		}
		values = append(values, value)
	}
	return values, true
}

func (w *execShellWalker) call(call *syntax.CallExpr) {
	for _, assign := range call.Assigns {
		if assign.Value != nil && !execWordStatic(assign.Value) || assign.Array != nil {
			w.opaque("dynamic assignment")
			return
		}
		if execResolutionVariable(assign) {
			w.opaque("command resolution changes")
			return
		}
	}
	if len(call.Args) == 0 {
		return
	}
	name, literal := shellCatLiteral(call.Args[0])
	if !literal {
		w.opaque("dynamic command name")
		return
	}
	args := call.Args[1:]
	for {
		switch shellsyntax.InterpreterIdentity(name) {
		case "command", "builtin", "exec":
			if len(args) == 0 {
				return
			}
			next, ok := shellCatLiteral(args[0])
			if !ok || strings.HasPrefix(next, "-") {
				w.opaque("dynamic command name")
				return
			}
			name, args = next, args[1:]
			continue
		case "env":
			for len(args) != 0 {
				next, ok := shellCatLiteral(args[0])
				if !ok || strings.HasPrefix(next, "-") {
					w.opaque("env options are not parsed")
					return
				}
				if !strings.Contains(next, "=") {
					break
				}
				if variable, _, _ := strings.Cut(next, "="); variable == "PATH" {
					w.opaque("command resolution changes")
					return
				}
				args = args[1:]
			}
			if len(args) == 0 {
				return
			}
			next, ok := shellCatLiteral(args[0])
			if !ok {
				w.opaque("dynamic command name")
				return
			}
			name, args = next, args[1:]
			continue
		}
		break
	}
	identity := shellsyntax.InterpreterIdentity(name)
	w.program = execProgram{Label: execProgramLabel(identity, args), Direct: execProgramDirect(identity, args)}
	if strings.Contains(name, "/") && filepath.Dir(name) != "/bin" && filepath.Dir(name) != "/usr/bin" {
		// A path names a program of its own, not the system tool.
		w.program.Direct = false
		w.opaque(name + " is not a system tool")
		return
	}
	if slices.Contains(w.functions, name) {
		w.program = execProgram{Label: name, Direct: true}
		w.opaque("shell function")
		w.loseDirectory()
		return
	}
	if identity == "find" && w.findProvider(args) {
		return
	}
	if !w.provider(identity, args) {
		w.command(identity, name, args)
	}
}

// execSubcommandTools are labeled with their subcommand, as in go generate.
var execSubcommandTools = map[string]int{
	"go": 1, "git": 1, "npm": 1, "pnpm": 1, "yarn": 1, "bun": 1, "deno": 1, "cargo": 1, "uv": 1,
	"poetry": 1, "pip": 1, "pip3": 1, "hg": 1, "svn": 1, "jj": 1, "npx": 1, "bunx": 1, "docker": 1,
	"dotnet": 1, "mvn": 1, "gradle": 1, "rustup": 1, "pdm": 1, "hatch": 1, "conda": 1, "brew": 1,
}

// execProgramLabel names a program by its identity and, for tools with
// subcommands, the literal subcommand words that select the behavior.
func execProgramLabel(identity string, args []*syntax.Word) string {
	if execSubcommandTools[identity] == 0 {
		return identity
	}
	var label strings.Builder
	label.WriteString(identity)
	for _, arg := range args {
		value, literal := shellCatLiteral(arg)
		if !literal {
			break
		}
		if strings.HasPrefix(value, "-") {
			continue
		}
		label.WriteString(" " + value)
		if !(identity == "go" && (value == "mod" || value == "work") || identity == "git" && value == "stash") {
			break
		}
	}
	return label.String()
}

// execDiscarding are VCS subcommands that overwrite or delete local work on
// the paths the agent names; the discarded content is the agent's to review.
var execDiscarding = map[string]bool{
	"git restore": true, "git checkout": true, "git reset": true, "git clean": true, "git stash": true,
	"git stash push": true, "git stash save": true, "git apply": true, "svn revert": true, "hg revert": true,
}

// execPackageSubcommands are the package-manager forms of bun and deno.
var execPackageSubcommands = map[string]bool{
	"install": true, "i": true, "add": true, "remove": true, "rm": true, "update": true, "upgrade": true,
	"link": true, "unlink": true, "pm": true, "outdated": true, "fmt": true, "lint": true, "cache": true,
	"compile": true, "bundle": true, "build": true,
}

func execProgramDirect(identity string, args []*syntax.Word) bool {
	switch {
	case strings.HasPrefix(identity, "python"), identity == "node", identity == "perl", identity == "ruby",
		identity == "eval", execFileOperations[identity]:
		return true
	case identity == "bun" || identity == "deno":
		subcommand := strings.TrimPrefix(execProgramLabel(identity, args), identity+" ")
		return !execPackageSubcommands[subcommand]
	case identity == "bash" || identity == "sh" || identity == "zsh" || identity == "dash":
		return slices.ContainsFunc(args, func(arg *syntax.Word) bool {
			value, _ := shellCatLiteral(arg)
			return strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "--") && strings.Contains(value, "c")
		})
	}
	label := execProgramLabel(identity, args)
	if label == "git checkout" {
		// Only the pathspec form discards work; a branch switch brings in history.
		return slices.ContainsFunc(args, func(arg *syntax.Word) bool {
			value, _ := shellCatLiteral(arg)
			return value == "--"
		})
	}
	return execDiscarding[label]
}

// execFileOperations are the commands whose writes the classifier derives
// when their operands are literal.
var execFileOperations = map[string]bool{
	"cp": true, "install": true, "mv": true, "ln": true, "rm": true, "touch": true, "truncate": true,
	"tee": true, "unlink": true, "shred": true, "sed": true, "sort": true, "awk": true, "gawk": true,
	"mawk": true, "find": true, "uniq": true, "xxd": true, "cd": true,
}

func (w *execShellWalker) command(identity, name string, args []*syntax.Word) {
	switch identity {
	case "eval", "source", ".", "pushd", "popd":
		w.opaque(identity + " runs unparsed shell")
		w.loseDirectory()
	case "cd":
		w.cd(args)
	case "cp", "install":
		w.copyLike(identity, args, false)
	case "mv":
		w.copyLike(identity, args, true)
	case "ln":
		w.link(args)
	case "rm":
		w.remove(args)
	case "touch", "truncate", "tee", "unlink", "shred":
		w.fileOperands(identity, args)
	case "sed":
		w.sed(args)
	case "perl":
		w.perl(args)
	case "sort":
		w.sort(args)
	case "git":
		w.git(args)
	case "svn":
		w.svn(args)
	case "awk", "gawk", "mawk":
		w.awk(identity, args)
	case "find":
		w.find(args)
	case "uniq", "xxd":
		operands, ok := literalArgs(args)
		count := 0
		for _, operand := range operands {
			if !strings.HasPrefix(operand, "-") {
				count++
			}
		}
		if !ok || count > 1 {
			w.opaque(identity + " with an output operand")
		}
	default:
		if execNeutralCommand(identity, args) {
			for _, arg := range args {
				if !execWordStatic(arg) {
					w.opaque("command substitution")
					return
				}
			}
			return
		}
		w.opaque(name + " is not recognized")
	}
}

// execNeutralCommands never write files through their own operands. Write
// redirections are classified separately.
var execNeutralCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "wc": true, "ls": true, "tree": true, "nl": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "fd": true, "ag": true, "ack": true,
	"stat": true, "file": true, "du": true, "df": true, "pwd": true, "echo": true, "printf": true,
	"true": true, "false": true, "test": true, "[": true, "sleep": true, "date": true, "which": true,
	"whoami": true, "id": true, "uname": true, "hostname": true, "basename": true, "dirname": true,
	"realpath": true, "readlink": true, "cut": true, "tr": true, "paste": true, "column": true,
	"diff": true, "cmp": true, "comm": true, "jq": true, "od": true, "hexdump": true, "strings": true,
	"md5sum": true, "sha1sum": true, "sha256sum": true, "sha512sum": true, "base64": true,
	"printenv": true, "ps": true, "pgrep": true, "seq": true, "type": true,
	"less": true, "more": true, "bat": true, "tac": true, "rev": true, "fold": true, "fmt": true,
	"expand": true, "unexpand": true, "mkdir": true, "chmod": true, "chown": true, "chgrp": true,
	"export": true, "set": true, "unset": true, "local": true,
	"declare": true, "readonly": true, "exit": true, "return": true, ":": true, "kill": true,
	"rmdir": true, "tput": true, "clear": true, "nproc": true, "getconf": true, "locale": true,
	"mread": true, "mcat": true, "msymbol": true, "inspect_file": true, "mchanges": true,
}

// execNeutralWriters lists options of otherwise neutral commands that write
// files or run other programs. A listed long option also matches its
// --name=value form; a listed short flag matches inside a bundle.
var execNeutralWriters = map[string]struct {
	short string
	long  []string
}{
	"fd":   {short: "xX", long: []string{"--exec", "--exec-batch"}},
	"rg":   {long: []string{"--pre"}},
	"ag":   {long: []string{"--pager"}},
	"ack":  {long: []string{"--pager"}},
	"bat":  {long: []string{"--pager"}},
	"tree": {short: "o"},
	"less": {short: "oO", long: []string{"--log-file", "--LOG-FILE", "--lesskey-file"}},
	"more": {short: "oO", long: []string{"--log-file", "--LOG-FILE"}},
	"file": {short: "C", long: []string{"--compile"}},
}

func execNeutralCommand(identity string, args []*syntax.Word) bool {
	if execNeutralCommands[identity] {
		writers, checked := execNeutralWriters[identity]
		if !checked {
			return true
		}
		values, ok := literalArgs(args)
		if !ok {
			return false
		}
		for _, value := range values {
			if value == "--" {
				break
			}
			if long, isLong := strings.CutPrefix(value, "--"); isLong {
				name, _, _ := strings.Cut(long, "=")
				if slices.Contains(writers.long, "--"+name) {
					return false
				}
				continue
			}
			// less runs +commands at startup.
			if strings.HasPrefix(value, "+") && (identity == "less" || identity == "more") {
				return false
			}
			if strings.HasPrefix(value, "-") && strings.ContainsAny(value[1:], writers.short) && writers.short != "" {
				return false
			}
		}
		return true
	}
	values, ok := literalArgs(args)
	if !ok {
		return false
	}
	switch identity {
	case "go":
		if len(values) == 0 {
			return false
		}
		switch values[0] {
		case "version", "list", "doc":
			return true
		case "env":
			return !slices.ContainsFunc(values[1:], func(value string) bool { return value == "-w" || value == "-u" })
		}
	case "hg":
		if len(values) != 0 {
			switch values[0] {
			case "status", "st", "log", "diff", "annotate", "blame", "root", "id", "identify", "branch", "paths":
				return len(values) < 2 || values[0] != "branch"
			case "cat":
				return !slices.ContainsFunc(values[1:], func(value string) bool {
					return value == "-o" || strings.HasPrefix(value, "-o") || strings.HasPrefix(value, "--output")
				})
			}
		}
	case "jj":
		if len(values) != 0 {
			switch values[0] {
			case "log", "diff", "status", "st", "show", "root":
				return true
			}
		}
	}
	return false
}

// cd moves the walker as if it succeeded. A directory that is not a literal
// path, including cd - and a bare cd, leaves the directory unknown.
func (w *execShellWalker) cd(args []*syntax.Word) {
	w.moves++
	if len(args) != 1 {
		w.cwd = ""
		return
	}
	if value, _ := shellCatLiteral(args[0]); value == "-" || strings.HasPrefix(value, "-") {
		w.cwd = ""
		return
	}
	operand, ok := w.operand(args[0])
	if !ok || operand.Glob {
		w.cwd = ""
		return
	}
	w.cwd = operand.Path
}

// execOptions describes one tool's GNU-style options. Short flags in args take
// a value; long options map to whether they take a value.
type execOptions struct {
	flags string
	args  string
	long  map[string]execLongOption
}

type execLongOption uint8

const (
	execLongFlag execLongOption = iota
	execLongValue
	// execLongOptional accepts a value only in --name=value form.
	execLongOptional
)

type execParsedOptions struct {
	set      map[string]string
	operands []*syntax.Word
}

func (o execParsedOptions) has(names ...string) bool {
	for _, name := range names {
		if _, ok := o.set[name]; ok {
			return true
		}
	}
	return false
}

func (o execParsedOptions) value(names ...string) (string, bool) {
	for _, name := range names {
		if value, ok := o.set[name]; ok {
			return value, true
		}
	}
	return "", false
}

// parse accepts GNU option permutation. An unknown option fails, because its
// effect on the write scope is unknown.
func (o execOptions) parse(words []*syntax.Word) (execParsedOptions, bool) {
	parsed := execParsedOptions{set: make(map[string]string)}
	for index := 0; index < len(words); index++ {
		word := words[index]
		text, literal := shellCatLiteral(word)
		if !literal || text == "-" || !strings.HasPrefix(text, "-") {
			parsed.operands = append(parsed.operands, word)
			continue
		}
		if text == "--" {
			parsed.operands = append(parsed.operands, words[index+1:]...)
			break
		}
		if long, ok := strings.CutPrefix(text, "--"); ok {
			name, value, hasValue := strings.Cut(long, "=")
			kind, known := o.long[name]
			if !known {
				return parsed, false
			}
			switch kind {
			case execLongFlag:
				if hasValue {
					return parsed, false
				}
			case execLongValue:
				if !hasValue {
					if index+1 >= len(words) {
						return parsed, false
					}
					next, ok := shellCatLiteral(words[index+1])
					if !ok {
						return parsed, false
					}
					value = next
					index++
				}
			}
			parsed.set["--"+name] = value
			continue
		}
		for offset := 1; offset < len(text); offset++ {
			flag := text[offset : offset+1]
			switch {
			case strings.Contains(o.flags, flag):
				parsed.set["-"+flag] = ""
			case strings.Contains(o.args, flag):
				value := text[offset+1:]
				if value == "" {
					if index+1 >= len(words) {
						return parsed, false
					}
					next, ok := shellCatLiteral(words[index+1])
					if !ok {
						return parsed, false
					}
					value = next
					index++
				}
				parsed.set["-"+flag] = value
				offset = len(text)
			default:
				return parsed, false
			}
		}
	}
	return parsed, true
}

var (
	execCopyOptions = execOptions{
		flags: "abdfHilLnPpRrsTuvxDcC",
		args:  "Stmog",
		long: map[string]execLongOption{
			"archive": execLongFlag, "backup": execLongOptional, "force": execLongFlag,
			"interactive": execLongFlag, "link": execLongFlag, "dereference": execLongFlag,
			"no-clobber": execLongFlag, "no-dereference": execLongFlag, "preserve": execLongOptional,
			"no-preserve": execLongValue, "parents": execLongFlag, "recursive": execLongFlag,
			"reflink": execLongOptional, "remove-destination": execLongFlag, "sparse": execLongValue,
			"strip-trailing-slashes": execLongFlag, "symbolic-link": execLongFlag,
			"suffix": execLongValue, "target-directory": execLongValue,
			"no-target-directory": execLongFlag, "update": execLongOptional, "verbose": execLongFlag,
			"one-file-system": execLongFlag, "copy-contents": execLongFlag, "debug": execLongFlag,
			"attributes-only": execLongFlag, "mode": execLongValue, "owner": execLongValue,
			"group": execLongValue, "compare": execLongFlag, "preserve-timestamps": execLongFlag,
			"strip": execLongFlag, "exchange": execLongFlag,
		},
	}
	execRemoveOptions = execOptions{
		flags: "fiIrRdv",
		long: map[string]execLongOption{
			"force": execLongFlag, "interactive": execLongOptional, "one-file-system": execLongFlag,
			"no-preserve-root": execLongFlag, "preserve-root": execLongOptional,
			"recursive": execLongFlag, "dir": execLongFlag, "verbose": execLongFlag,
		},
	}
	execLinkOptions = execOptions{
		flags: "bdFfinLPrsTv",
		args:  "St",
		long: map[string]execLongOption{
			"backup": execLongOptional, "directory": execLongFlag, "force": execLongFlag,
			"interactive": execLongFlag, "logical": execLongFlag, "no-dereference": execLongFlag,
			"physical": execLongFlag, "relative": execLongFlag, "symbolic": execLongFlag,
			"suffix": execLongValue, "target-directory": execLongValue,
			"no-target-directory": execLongFlag, "verbose": execLongFlag,
		},
	}
	execFileOptions = map[string]execOptions{
		"touch": {flags: "acfhm", args: "drt", long: map[string]execLongOption{
			"no-create": execLongFlag, "date": execLongValue, "reference": execLongValue,
			"no-dereference": execLongFlag, "time": execLongValue,
		}},
		"truncate": {flags: "co", args: "sr", long: map[string]execLongOption{
			"no-create": execLongFlag, "io-blocks": execLongFlag, "reference": execLongValue, "size": execLongValue,
		}},
		"tee": {flags: "aip", long: map[string]execLongOption{
			"append": execLongFlag, "ignore-interrupts": execLongFlag, "output-error": execLongOptional,
		}},
		"unlink": {long: map[string]execLongOption{}},
		"shred":  {flags: "fuvxz", args: "ns", long: map[string]execLongOption{"remove": execLongOptional, "force": execLongFlag, "zero": execLongFlag}},
	}
)

func (w *execShellWalker) operands(words []*syntax.Word) ([]execOperand, bool) {
	operands := make([]execOperand, 0, len(words))
	for _, word := range words {
		operand, ok := w.operand(word)
		if !ok {
			return nil, false
		}
		operands = append(operands, operand)
	}
	return operands, true
}

// execBackupSuffix returns the simple backup suffix of -b style options. A
// bare -b and numbered or existing control depend on VERSION_CONTROL and on
// backups already present, so they are not modeled.
func execBackupSuffix(options execParsedOptions) (string, bool) {
	if options.has("-b") {
		return "", false
	}
	control, ok := options.value("--backup")
	switch {
	case !ok, control == "none", control == "off":
		return "", true
	case control != "simple" && control != "never":
		return "", false
	}
	if suffix, ok := options.value("-S", "--suffix"); ok && suffix != "" {
		return suffix, true
	}
	return "~", true
}

func (w *execShellWalker) copyLike(identity string, args []*syntax.Word, move bool) {
	options, ok := execCopyOptions.parse(args)
	if !ok {
		w.opaque(identity + " options are not parsed")
		return
	}
	if identity == "install" && options.has("-d", "--directory") {
		return
	}
	if options.has("--parents") {
		// Targets keep each source's spelled relative path, which the
		// absolute operands no longer carry.
		w.opaque(identity + " --parents")
		return
	}
	operands, ok := w.operands(options.operands)
	if !ok {
		w.opaque("dynamic " + identity + " operand")
		return
	}
	if slices.ContainsFunc(options.operands, execDotOperand) {
		w.opaque(identity + " of directory contents")
		return
	}
	backup, ok := execBackupSuffix(options)
	if !ok {
		w.opaque(identity + " backup control is not modeled")
		return
	}
	entry := execScopeEntry{
		Kind:      execScopeInto,
		NoTarget:  options.has("-T", "--no-target-directory"),
		Recursive: move || options.has("-r", "-R", "-a", "--recursive", "--archive"),
		Sources:   move,
		Copies:    !move,
		Backup:    backup,
		// cp opens an existing destination; install and mv replace it.
		Through: identity == "cp" && !options.has("--remove-destination", "-l", "--link", "-s", "--symbolic-link"),
	}
	if target, ok := options.value("-t", "--target-directory"); ok {
		operand, ok := w.operand(&syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: target}}})
		if !ok || operand.Glob {
			w.opaque("dynamic target directory")
			return
		}
		entry.Dest, entry.DestDir, entry.Operands = operand.Path, true, operands
	} else {
		if len(operands) < 2 || operands[len(operands)-1].Glob {
			w.opaque(identity + " without a literal destination")
			return
		}
		entry.Dest, entry.Operands = operands[len(operands)-1].Path, operands[:len(operands)-1]
		entry.DestDir = len(entry.Operands) > 1
	}
	if len(entry.Operands) == 0 {
		w.opaque(identity + " without sources")
		return
	}
	w.plan.raise(execDeclared, "")
	w.plan.label(identity)
	w.plan.add(entry)
}

func (w *execShellWalker) link(args []*syntax.Word) {
	options, ok := execLinkOptions.parse(args)
	if !ok {
		w.opaque("ln options are not parsed")
		return
	}
	operands, ok := w.operands(options.operands)
	if !ok || len(operands) == 0 {
		w.opaque("dynamic ln operand")
		return
	}
	backup, ok := execBackupSuffix(options)
	if !ok {
		w.opaque("ln backup control is not modeled")
		return
	}
	if slices.ContainsFunc(options.operands, execDotOperand) {
		w.opaque("ln of a directory name")
		return
	}
	// Only the link names change; ln never writes its targets. A lone target
	// links into the current directory.
	entry := execScopeEntry{Kind: execScopeInto, NoTarget: options.has("-T", "--no-target-directory"), NoDereference: options.has("-n", "--no-dereference"), Backup: backup}
	switch target, hasTarget := options.value("-t", "--target-directory"); {
	case hasTarget:
		operand, ok := w.operand(&syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: target}}})
		if !ok || operand.Glob {
			w.opaque("dynamic target directory")
			return
		}
		entry.Dest, entry.DestDir, entry.Operands = operand.Path, true, operands
	case len(operands) == 1:
		if w.cwd == "" {
			w.opaque("ln into an unknown directory")
			return
		}
		entry.Dest, entry.DestDir, entry.Operands = w.cwd, true, operands
	default:
		if operands[len(operands)-1].Glob {
			w.opaque("ln without a literal destination")
			return
		}
		entry.Dest, entry.Operands = operands[len(operands)-1].Path, operands[:len(operands)-1]
		entry.DestDir = len(entry.Operands) > 1
	}
	w.plan.raise(execDeclared, "")
	w.plan.label("ln")
	w.plan.add(entry)
}

func (w *execShellWalker) remove(args []*syntax.Word) {
	options, ok := execRemoveOptions.parse(args)
	if !ok {
		w.opaque("rm options are not parsed")
		return
	}
	operands, ok := w.operands(options.operands)
	if !ok {
		w.opaque("dynamic rm operand")
		return
	}
	kind := execScopeFile
	if options.has("-r", "-R", "--recursive") {
		kind = execScopeTree
	}
	w.plan.raise(execDeclared, "")
	w.plan.label("rm")
	w.plan.add(execScopeEntry{Kind: kind, Operands: operands})
}

func (w *execShellWalker) fileOperands(identity string, args []*syntax.Word) {
	options, ok := execFileOptions[identity].parse(args)
	if !ok {
		w.opaque(identity + " options are not parsed")
		return
	}
	operands, ok := w.operands(options.operands)
	if !ok {
		w.opaque("dynamic " + identity + " operand")
		return
	}
	if len(operands) == 0 {
		return
	}
	w.plan.raise(execDeclared, "")
	w.plan.label(identity)
	// unlink removes the name itself; the others open it and follow links
	// unless touch -h asks otherwise.
	through := identity != "unlink" && !options.has("-h", "--no-dereference")
	w.plan.add(execScopeEntry{Kind: execScopeFile, Operands: operands, Through: through})
}

func (w *execShellWalker) sort(args []*syntax.Word) {
	values, ok := literalArgs(args)
	if !ok {
		w.opaque("dynamic sort operand")
		return
	}
	var outputs []string
	for index := 0; index < len(values); index++ {
		value := values[index]
		if value == "--" {
			break
		}
		next := func() (string, bool) {
			if index+1 >= len(values) {
				return "", false
			}
			index++
			return values[index], true
		}
		if long, ok := strings.CutPrefix(value, "--"); ok {
			name, output, hasValue := strings.Cut(long, "=")
			switch {
			case name == "compress-program":
				w.opaque("sort --compress-program runs a program")
				return
			case name == "output" && !hasValue:
				output, hasValue = next()
				fallthrough
			case name == "output":
				if !hasValue {
					w.opaque("sort --output without a value")
					return
				}
				outputs = append(outputs, output)
			}
			continue
		}
		if !strings.HasPrefix(value, "-") {
			continue
		}
		// In a short bundle, the first option that takes a value consumes
		// the rest of the word or the next word.
		for offset := 1; offset < len(value); offset++ {
			flag := value[offset]
			if !strings.ContainsRune("koStT", rune(flag)) {
				continue
			}
			argument := value[offset+1:]
			if argument == "" {
				argument, _ = next()
			}
			if flag == 'o' {
				outputs = append(outputs, argument)
			}
			break
		}
	}
	for _, output := range outputs {
		operand, ok := w.operand(&syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Value: output}}})
		if !ok {
			w.opaque("dynamic sort output")
			return
		}
		w.plan.raise(execDeclared, "")
		w.plan.label("sort")
		w.plan.add(execScopeEntry{Kind: execScopeFile, Operands: []execOperand{operand}, Through: true})
	}
}

// execAwkFlags are awk options without an effect on files. Profiling, dumps,
// extensions, and program files are left out and make the command opaque.
var execAwkFlags = []string{
	"-b", "-c", "-C", "-N", "-n", "-O", "-P", "-r", "-s", "-S", "-t", "-M", "-V", "-h",
	"--characters-as-bytes", "--traditional", "--copyright", "--use-lc-numeric", "--optimize",
	"--no-optimize", "--posix", "--re-interval", "--sandbox", "--version", "--help",
}

func (w *execShellWalker) awk(identity string, args []*syntax.Word) {
	var programs []string
	index := 0
	for ; index < len(args); index++ {
		value, literal := shellCatLiteral(args[index])
		if !literal {
			break
		}
		if value == "--" {
			index++
			break
		}
		if value == "-" || !strings.HasPrefix(value, "-") {
			break
		}
		name, attached, hasAttached := strings.Cut(value, "=")
		switch {
		case slices.Contains(execAwkFlags, value):
		case len(value) > 2 && (value[:2] == "-F" || value[:2] == "-v"):
		case value == "-F" || value == "-v" || value == "--field-separator" || value == "--assign":
			index++
		case name == "--field-separator" || name == "--assign":
		case value == "-e" || value == "--source":
			if index+1 >= len(args) {
				w.opaque(identity + " program is not literal")
				return
			}
			program, literal := shellCatLiteral(args[index+1])
			if !literal {
				w.opaque(identity + " program is not literal")
				return
			}
			programs = append(programs, program)
			index++
		case name == "--source" && hasAttached:
			programs = append(programs, attached)
		default:
			w.opaque(identity + " option " + name + " is not modeled")
			return
		}
	}
	if len(programs) == 0 {
		if index >= len(args) {
			w.opaque(identity + " without a program")
			return
		}
		program, literal := shellCatLiteral(args[index])
		if !literal {
			w.opaque(identity + " program is not literal")
			return
		}
		programs = append(programs, program)
	}
	for _, program := range programs {
		// Output redirection, pipes, system(), and @load can write anywhere.
		if strings.ContainsAny(program, ">|@") || strings.Contains(program, "system") {
			w.opaque(identity + " program may write files")
			return
		}
	}
}

func (w *execShellWalker) find(args []*syntax.Word) {
	values, ok := literalArgs(args)
	if !ok {
		w.opaque("dynamic find operand")
		return
	}
	for _, value := range values {
		switch value {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
			w.opaque("find " + value)
			return
		}
	}
}

// sed -i is declared when its script cannot write other files or run commands.
func (w *execShellWalker) sed(args []*syntax.Word) {
	var scripts []string
	var files []*syntax.Word
	inPlace, suffix := false, ""
	sandbox := false
	for index := 0; index < len(args); index++ {
		text, literal := shellCatLiteral(args[index])
		if !literal {
			if len(scripts) == 0 {
				w.opaque("dynamic sed script")
				return
			}
			files = append(files, args[index])
			continue
		}
		switch {
		case text == "--":
			files = append(files, args[index+1:]...)
			index = len(args)
		case text == "-e" || text == "--expression" || text == "-f" || text == "--file" || text == "-l" || text == "--line-length":
			if index+1 >= len(args) {
				w.opaque("sed option without a value")
				return
			}
			value, ok := shellCatLiteral(args[index+1])
			if !ok {
				w.opaque("dynamic sed script")
				return
			}
			index++
			switch text {
			case "-e", "--expression":
				scripts = append(scripts, value)
			case "-f", "--file":
				w.opaque("sed script file is not inspected")
				return
			}
		case strings.HasPrefix(text, "--expression="):
			scripts = append(scripts, strings.TrimPrefix(text, "--expression="))
		case strings.HasPrefix(text, "--file="):
			w.opaque("sed script file is not inspected")
			return
		case text == "--in-place" || strings.HasPrefix(text, "--in-place="):
			inPlace, suffix = true, strings.TrimPrefix(strings.TrimPrefix(text, "--in-place"), "=")
		case text == "--sandbox":
			sandbox = true
		case text == "--follow-symlinks":
			w.opaque("sed --follow-symlinks writes through links")
			return
		case slices.Contains([]string{"-n", "--quiet", "--silent", "-E", "-r", "--regexp-extended", "-s", "--separate", "-u", "--unbuffered", "-z", "--null-data", "--posix", "--debug"}, text):
		case strings.HasPrefix(text, "--"):
			w.opaque("sed option " + text + " is not modeled")
			return
		case strings.HasPrefix(text, "-") && len(text) > 1 && !strings.HasPrefix(text, "--"):
			// Bundled short flags; -i consumes the rest as its suffix.
			for offset := 1; offset < len(text); offset++ {
				switch text[offset] {
				case 'n', 'E', 'r', 's', 'u', 'z':
				case 'i':
					inPlace, suffix = true, text[offset+1:]
					offset = len(text)
				case 'e':
					if offset+1 != len(text) || index+1 >= len(args) {
						w.opaque("sed -e without a literal script")
						return
					}
					value, ok := shellCatLiteral(args[index+1])
					if !ok {
						w.opaque("dynamic sed script")
						return
					}
					scripts = append(scripts, value)
					index++
				default:
					w.opaque("sed options are not parsed")
					return
				}
			}
		default:
			if len(scripts) == 0 {
				scripts = append(scripts, text)
			} else {
				files = append(files, args[index])
			}
		}
	}
	writes := false
	for _, script := range scripts {
		scriptWrites, known := sedScriptWrites(script)
		if !known {
			w.opaque("sed script is not parsed")
			return
		}
		writes = writes || scriptWrites
	}
	if writes && !sandbox {
		// A w, W, or e command writes or runs outside the operands.
		if !inPlace {
			w.opaque("sed script writes files")
			return
		}
		w.plan.raise(execScoped, "sed script writes other files")
	}
	if !inPlace {
		return
	}
	operands, ok := w.operands(files)
	if !ok || len(operands) == 0 {
		w.opaque("sed -i without literal files")
		return
	}
	w.plan.raise(execDeclared, "")
	w.plan.label("sed")
	w.plan.add(execScopeEntry{Kind: execScopeFile, Operands: operands})
	if suffix != "" {
		w.addBackups(operands, suffix)
	}
}

// addBackups records in-place backup names. A suffix with * replaces the
// original file operand, as in GNU sed and perl.
func (w *execShellWalker) addBackups(operands []execOperand, suffix string) {
	backups := make([]execOperand, 0, len(operands))
	for _, operand := range operands {
		if operand.Glob {
			matches, err := filepath.Glob(operand.Path)
			if err != nil || len(matches) > maxExecGlobMatches {
				w.opaque("backup glob exceeds capture bounds")
				continue
			}
			for _, path := range matches {
				w.addBackups([]execOperand{{Path: path, AbsoluteInput: operand.AbsoluteInput}}, suffix)
			}
			continue
		}
		name := operand.Path + suffix
		if strings.Contains(suffix, "*") {
			spelling := operand.Path
			if !operand.AbsoluteInput {
				var err error
				spelling, err = filepath.Rel(w.cwd, operand.Path)
				if err != nil {
					w.opaque("backup in an unknown directory")
					return
				}
			}
			name = strings.ReplaceAll(suffix, "*", spelling)
			if !filepath.IsAbs(name) {
				name = filepath.Join(w.cwd, name)
			}
		}
		backups = append(backups, execOperand{Path: filepath.Clean(name)})
	}
	w.plan.add(execScopeEntry{Kind: execScopeFile, Operands: backups})
}

// sedScriptWrites scans GNU sed commands for w, W, e, and s///w or s///e.
// known is false for syntax this scanner does not model.
func sedScriptWrites(script string) (writes, known bool) {
	index := 0
	skipSpace := func() {
		for index < len(script) && strings.ContainsRune(" \t\n;", rune(script[index])) {
			index++
		}
	}
	skipAddress := func() bool {
		for range 2 {
			if index >= len(script) {
				return true
			}
			switch char := script[index]; {
			case char >= '0' && char <= '9':
				for index < len(script) && (script[index] >= '0' && script[index] <= '9' || script[index] == '~') {
					index++
				}
			case char == '$':
				index++
			case char == '/' || char == '\\':
				delimiter := byte('/')
				if char == '\\' {
					if index+1 >= len(script) {
						return false
					}
					delimiter = script[index+1]
					index++
				}
				index++
				for index < len(script) && script[index] != delimiter {
					if script[index] == '\\' {
						index++
					}
					index++
				}
				if index >= len(script) {
					return false
				}
				index++
				for index < len(script) && (script[index] == 'I' || script[index] == 'M') {
					index++
				}
			default:
				return true
			}
			if index < len(script) && script[index] == ',' {
				index++
				continue
			}
			return true
		}
		return true
	}
	readText := func() {
		for index < len(script) && script[index] != '\n' {
			index++
		}
	}
	// GNU sed ends labels at a semicolon too, so later commands stay visible.
	readLabel := func() {
		for index < len(script) && script[index] != '\n' && script[index] != ';' {
			index++
		}
	}
	for {
		skipSpace()
		if index >= len(script) {
			return writes, true
		}
		if !skipAddress() {
			return false, false
		}
		for index < len(script) && (script[index] == ' ' || script[index] == '!') {
			index++
		}
		if index >= len(script) {
			return false, false
		}
		command := script[index]
		index++
		switch command {
		case '{', '}', '=', 'd', 'D', 'g', 'G', 'h', 'H', 'l', 'n', 'N', 'p', 'P', 'x', 'z', 'F':
		case 'q', 'Q', 'L':
			for index < len(script) && script[index] >= '0' && script[index] <= '9' {
				index++
			}
		case ':', 'b', 't', 'T':
			readLabel()
		case '#', 'r', 'R', 'a', 'i', 'c', 'v':
			readText()
		case 'w', 'W', 'e':
			writes = true
			readText()
		case 's', 'y':
			if index >= len(script) {
				return false, false
			}
			delimiter := script[index]
			index++
			for range 2 {
				for index < len(script) && script[index] != delimiter {
					if script[index] == '\\' {
						index++
					}
					index++
				}
				if index >= len(script) {
					return false, false
				}
				index++
			}
			if command == 's' {
				for index < len(script) && strings.ContainsRune("gpiImMe0123456789w", rune(script[index])) {
					if script[index] == 'w' || script[index] == 'e' {
						writes = true
						if script[index] == 'w' {
							readText()
							break
						}
					}
					index++
				}
			}
		default:
			return false, false
		}
	}
}

// perl -pi is declared only for substitution programs. Anything else can
// open, rename, or run arbitrary files.
func (w *execShellWalker) perl(args []*syntax.Word) {
	values, ok := literalArgs(args)
	if !ok {
		w.opaque("dynamic perl operand")
		return
	}
	inPlace, suffix := false, ""
	var programs []string
	index := 0
	for ; index < len(values); index++ {
		value := values[index]
		if value == "--" {
			index++
			break
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			break
		}
		for offset := 1; offset < len(value); offset++ {
			switch flag := value[offset]; flag {
			case 'p', 'n', 'w', 's', 'a', 'l', 'C', 'W', 'X':
				if flag == 'l' || flag == 'C' {
					for offset+1 < len(value) && value[offset+1] >= '0' && value[offset+1] <= '9' {
						offset++
					}
				}
			case '0':
				for offset+1 < len(value) && strings.ContainsRune("0123456789abcdefABCDEFx", rune(value[offset+1])) {
					offset++
				}
			case 'i':
				inPlace, suffix = true, value[offset+1:]
				offset = len(value)
			case 'e', 'E':
				if offset+1 < len(value) {
					programs = append(programs, value[offset+1:])
				} else if index+1 < len(values) {
					programs = append(programs, values[index+1])
					index++
				} else {
					w.opaque("perl -e without a program")
					return
				}
				offset = len(value)
			default:
				w.opaque("perl options are not parsed")
				return
			}
		}
	}
	if len(programs) == 0 {
		w.opaque("perl program is not inspected")
		return
	}
	files, ok := w.operands(args[index:])
	if !ok {
		w.opaque("dynamic perl operand")
		return
	}
	if !inPlace || len(files) == 0 {
		w.opaque("perl program is not inspected")
		return
	}
	declared := true
	for _, program := range programs {
		declared = declared && perlSubstitutionOnly(program)
	}
	if declared {
		w.plan.raise(execDeclared, "")
	} else {
		w.plan.raise(execScoped, "perl -i program is not only substitutions")
	}
	w.plan.label("perl")
	w.plan.add(execScopeEntry{Kind: execScopeFile, Operands: files})
	if suffix != "" {
		w.addBackups(files, suffix)
	}
}

// perlSubstitutionOnly accepts s///, tr///, and y/// expressions separated by
// semicolons, without the e flag or interpolated code.
func perlSubstitutionOnly(program string) bool {
	// Interpolated blocks and regex code blocks run arbitrary code.
	for _, code := range []string{"${", "@{", "(?{", "(??{"} {
		if strings.Contains(program, code) {
			return false
		}
	}
	index := 0
	closing := map[byte]byte{'{': '}', '(': ')', '[': ']', '<': '>'}
	readDelimited := func(open byte) bool {
		closeWith, bracketed := closing[open]
		if !bracketed {
			closeWith = open
		}
		depth := 0
		for index < len(program) {
			char := program[index]
			index++
			switch {
			case char == '\\':
				index++
			case bracketed && char == open:
				depth++
			case char == closeWith:
				if depth == 0 {
					return true
				}
				depth--
			}
		}
		return false
	}
	seen := false
	for {
		for index < len(program) && strings.ContainsRune(" \t\n;", rune(program[index])) {
			index++
		}
		if index >= len(program) {
			return seen
		}
		operator := ""
		for _, candidate := range []string{"tr", "s", "y"} {
			if strings.HasPrefix(program[index:], candidate) {
				operator = candidate
				break
			}
		}
		if operator == "" {
			return false
		}
		index += len(operator)
		if index >= len(program) {
			return false
		}
		open := program[index]
		if open == ' ' || open >= 'a' && open <= 'z' || open >= 'A' && open <= 'Z' || open >= '0' && open <= '9' || open == '_' {
			return false
		}
		index++
		if !readDelimited(open) {
			return false
		}
		if _, bracketed := closing[open]; bracketed {
			for index < len(program) && program[index] == ' ' {
				index++
			}
			if index >= len(program) {
				return false
			}
			open = program[index]
			index++
		}
		if !readDelimited(open) {
			return false
		}
		for index < len(program) && (program[index] >= 'a' && program[index] <= 'z') {
			if program[index] == 'e' && operator == "s" {
				return false
			}
			index++
		}
		seen = true
	}
}

// Git global options that only change the repository or output presentation.
// Each -C is relative to the previous one. -c can install hooks, pagers, or
// filters for the command itself, so it is not accepted.
func gitSubcommand(values []string) (subcommand string, rest []string, cwd []string, ok bool) {
	for index := 0; index < len(values); index++ {
		value := values[index]
		switch {
		case value == "-C":
			if index+1 >= len(values) {
				return "", nil, nil, false
			}
			cwd = append(cwd, values[index+1])
			index++
		case value == "--no-pager" || value == "--no-optional-locks" || value == "-P" ||
			strings.HasPrefix(value, "--git-dir=") || strings.HasPrefix(value, "--work-tree=") ||
			value == "--no-replace-objects" || value == "--literal-pathspecs":
			if strings.HasPrefix(value, "--git-dir=") || strings.HasPrefix(value, "--work-tree=") {
				return "", nil, nil, false
			}
		case strings.HasPrefix(value, "-"):
			return "", nil, nil, false
		default:
			return value, values[index+1:], cwd, true
		}
	}
	return "", nil, nil, false
}

var gitReadOnlySubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "rev-parse": true, "ls-files": true,
	"grep": true, "blame": true, "describe": true, "show-ref": true, "cat-file": true,
	"merge-base": true, "rev-list": true, "shortlog": true, "ls-tree": true, "for-each-ref": true,
	"name-rev": true, "whatchanged": true, "check-ignore": true, "check-attr": true, "var": true,
	"help": true, "version": true, "count-objects": true, "cherry": true, "range-diff": true,
}

func (w *execShellWalker) git(args []*syntax.Word) {
	values, ok := literalArgs(args)
	if !ok {
		w.opaque("dynamic git operand")
		return
	}
	subcommand, rest, cwd, ok := gitSubcommand(values)
	if !ok {
		w.opaque("git options are not parsed")
		return
	}
	inner := *w
	for _, directory := range cwd {
		if !filepath.IsAbs(directory) {
			if inner.cwd == "" {
				w.opaque("git -C in an unknown directory")
				return
			}
			directory = filepath.Join(inner.cwd, directory)
		}
		inner.cwd = filepath.Clean(directory)
	}
	restWords := args[len(args)-len(rest):]
	switch subcommand {
	case "diff", "log", "show", "format-patch":
		for _, value := range rest {
			if value == "-o" || strings.HasPrefix(value, "--output") || subcommand == "format-patch" {
				w.opaque("git " + subcommand + " writes output files")
				return
			}
		}
		return
	case "branch", "tag", "remote", "config", "stash", "worktree":
		// Listing forms are read-only; anything else changes refs or
		// configuration, which is outside the worktree, or worktree files
		// (stash), which later slices scope.
		if subcommand == "stash" && (len(rest) == 0 || rest[0] != "list" && rest[0] != "show") {
			w.opaque("git stash changes worktree files")
		}
		if subcommand == "worktree" && (len(rest) == 0 || rest[0] != "list") {
			w.opaque("git worktree changes files")
		}
		// Configuration can install hooks and filters that later commands run.
		if subcommand == "config" && !slices.ContainsFunc(rest, func(value string) bool {
			return strings.HasPrefix(value, "--get") || value == "--list" || value == "-l" || value == "get" || value == "list"
		}) {
			w.opaque("git config changes command behavior")
		}
		return
	case "grep":
		if slices.ContainsFunc(rest, func(value string) bool {
			return value == "-O" || strings.HasPrefix(value, "-O") || strings.HasPrefix(value, "--open-files-in-pager")
		}) {
			w.opaque("git grep runs a pager")
		}
		return
	case "commit", "am", "merge", "rebase":
		// Hooks and merges can rewrite worktree files.
		w.opaque("git " + subcommand + " may run hooks or merge files")
		return
	case "add", "fetch", "reset":
		if subcommand == "reset" {
			for _, value := range rest {
				if value == "--hard" || value == "--merge" || value == "--keep" || value == "-p" || value == "--patch" {
					w.opaque("git reset changes worktree files")
					return
				}
			}
			if len(rest) > 0 && slices.Contains(rest, "--") {
				return
			}
		}
		return
	case "restore":
		staged, worktree := false, false
		for _, value := range rest {
			switch value {
			case "--staged", "-S":
				staged = true
			case "--worktree", "-W":
				worktree = true
			}
		}
		if staged && !worktree {
			return
		}
		w.opaque("git restore changes worktree files")
		return
	case "mv":
		options, ok := execOptions{flags: "fknv", long: map[string]execLongOption{"force": execLongFlag, "dry-run": execLongFlag, "verbose": execLongFlag}}.parse(restWords)
		if !ok || options.has("-n", "--dry-run") {
			if !ok {
				w.opaque("git mv options are not parsed")
			}
			return
		}
		operands, ok := inner.operands(options.operands)
		if !ok || len(operands) < 2 || slices.ContainsFunc(options.operands, execGitPathspecMagic) {
			w.opaque("git mv without literal operands")
			return
		}
		w.plan.raise(execDeclared, "")
		w.plan.label("git mv")
		w.plan.add(execScopeEntry{
			Kind: execScopeInto, Operands: operands[:len(operands)-1], Dest: operands[len(operands)-1].Path,
			DestDir: len(operands) > 2, Recursive: true, Sources: true,
		})
		return
	case "rm":
		options, ok := execOptions{flags: "fnqr", long: map[string]execLongOption{
			"force": execLongFlag, "dry-run": execLongFlag, "quiet": execLongFlag, "cached": execLongFlag,
			"ignore-unmatch": execLongFlag, "sparse": execLongFlag, "pathspec-from-file": execLongValue,
		}}.parse(restWords)
		if !ok || options.has("--pathspec-from-file") {
			w.opaque("git rm options are not parsed")
			return
		}
		if options.has("--cached", "-n", "--dry-run") {
			return
		}
		operands, ok := inner.operands(options.operands)
		if !ok || slices.ContainsFunc(options.operands, execGitPathspecMagic) {
			w.opaque("git rm pathspec is not literal")
			return
		}
		kind := execScopeFile
		if options.has("-r") {
			kind = execScopeTree
		}
		w.plan.raise(execDeclared, "")
		w.plan.label("git rm")
		w.plan.add(execScopeEntry{Kind: kind, Operands: operands})
		return
	}
	if gitReadOnlySubcommands[subcommand] {
		return
	}
	w.opaque("git " + subcommand + " may change worktree files")
}

// execGitPathspecMagic matches operands git reads as patterns even when the
// shell passes them quoted: wildcards match across directories, and a leading
// colon starts pathspec magic.
func execGitPathspecMagic(word *syntax.Word) bool {
	value, glob, _ := execWordOperand(word)
	if glob {
		return true
	}
	return strings.HasPrefix(value, ":") || strings.ContainsAny(value, "*?[")
}

func (w *execShellWalker) svn(args []*syntax.Word) {
	values, ok := literalArgs(args)
	if !ok || len(values) == 0 {
		w.opaque("dynamic svn operand")
		return
	}
	subcommand := values[0]
	switch subcommand {
	case "status", "st", "stat", "info", "log", "diff", "di", "cat", "list", "ls", "blame", "praise", "annotate", "ann", "propget", "pg", "proplist", "pl", "help", "--version":
		return
	case "delete", "del", "remove", "rm", "move", "mv", "rename", "ren":
	default:
		w.opaque("svn " + subcommand + " may change worktree files")
		return
	}
	options, ok := execOptions{flags: "qm", args: "mF", long: map[string]execLongOption{
		"force": execLongFlag, "quiet": execLongFlag, "keep-local": execLongFlag, "parents": execLongFlag,
		"message": execLongValue, "file": execLongValue,
	}}.parse(args[1:])
	if !ok {
		w.opaque("svn options are not parsed")
		return
	}
	if options.has("--keep-local") {
		return
	}
	var local []*syntax.Word
	for _, word := range options.operands {
		if value, _ := shellCatLiteral(word); !strings.Contains(value, "://") {
			local = append(local, word)
		}
	}
	operands, ok := w.operands(local)
	if !ok || slices.ContainsFunc(operands, func(operand execOperand) bool { return operand.Glob }) {
		w.opaque("svn operand is not literal")
		return
	}
	if len(operands) == 0 {
		return
	}
	w.plan.raise(execDeclared, "")
	w.plan.label("svn " + subcommand)
	switch subcommand {
	case "move", "mv", "rename", "ren":
		if len(operands) < 2 {
			w.opaque("svn move without a destination")
			return
		}
		w.plan.add(execScopeEntry{
			Kind: execScopeInto, Operands: operands[:len(operands)-1], Dest: operands[len(operands)-1].Path,
			DestDir: len(operands) > 2, Recursive: true, Sources: true,
		})
	default:
		w.plan.add(execScopeEntry{Kind: execScopeTree, Operands: operands})
	}
}
