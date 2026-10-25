package presentation

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

// Source: apps/mekugi/internal/router/vcs_display.go:32:676@[9822b997].
// vcsCall is a literal git, svn, or mchanges invocation that Activity shows
// as a VCS operation rather than a Run preview. Recognition reads literal
// words only, plus a quoted heredoc message; nothing is expanded, evaluated,
// or run.
type vcsCall struct {
	tool              string // git, svn, or mchanges
	kind              string // One of the vcs* kinds.
	format            string // A stat read's output form, such as numstat.
	dir               string // git -C directory, as written.
	subject           string // A commit's first message line, when literal.
	display           string // The operation paragraph.
	repositoryUnknown bool   // An env wrapper may override Git repository selection.
}

const (
	vcsCommit = "commit"
	vcsStage  = "stage"
	vcsDiff   = "diff"
	vcsStat   = "stat"
	vcsCheck  = "check"
	vcsStatus = "status"
)

// vcsStatement recognizes one VCS command. A commit may read its message
// from a quoted heredoc or here-string on stdin; discarded or merged stderr
// is transparent; any other redirection keeps the Run preview.
func vcsStatement(statement *syntax.Stmt) (vcsCall, bool) {
	statement = shellsyntax.UnwrapTimeout(statement)
	if statement == nil || statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
		return vcsCall{}, false
	}
	if inner := shellsyntax.UnwrapEnv(statement); inner != statement {
		call, ok := vcsStatement(inner)
		call.repositoryUnknown = true
		return call, ok
	}
	if inner := shellsyntax.UnwrapMRun(statement); inner != statement {
		return vcsStatement(inner)
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 || len(call.Args) == 0 {
		return vcsCall{}, false
	}
	var stdin *string
	for _, redirect := range statement.Redirs {
		switch {
		case redirect.N == nil && stdin == nil && (redirect.Op == syntax.Hdoc || redirect.Op == syntax.DashHdoc):
			body, ok := vcsHeredocBody(redirect)
			if !ok {
				return vcsCall{}, false
			}
			stdin = &body
		case redirect.N == nil && stdin == nil && redirect.Op == syntax.WordHdoc:
			body, ok := shellsyntax.Literal(redirect.Word)
			if !ok {
				return vcsCall{}, false
			}
			stdin = &body
		case !vcsStderrRedirect(redirect):
			return vcsCall{}, false
		}
	}
	argv := make([]string, len(call.Args))
	for i, arg := range call.Args {
		value, ok := vcsLiteral(arg)
		if !ok {
			value, ok = vcsCatSubstitution(arg)
		}
		if !ok {
			return vcsCall{}, false
		}
		argv[i] = value
	}
	switch filepath.Base(argv[0]) {
	case "git":
		return vcsGit(argv[1:], stdin)
	case "svn":
		return vcsSVN(argv[1:], stdin)
	case "mchanges":
		if stdin == nil {
			return vcsMChanges(argv[1:])
		}
	}
	return vcsCall{}, false
}

// vcsLiteral is a word's literal value. Beyond shellsyntax.Literal, it keeps
// revision syntax such as HEAD~2, whose tilde does not lead the word and
// so cannot expand; globs and brace expansions stay unrecognized.
func vcsLiteral(word *syntax.Word) (string, bool) {
	braces := slices.ContainsFunc(word.Parts, func(part syntax.WordPart) bool {
		lit, ok := part.(*syntax.Lit)
		return ok && strings.Contains(lit.Value, "{") && (strings.Contains(lit.Value, ",") || strings.Contains(lit.Value, ".."))
	})
	if braces {
		return "", false
	}
	if value, ok := shellsyntax.Literal(word); ok {
		return value, true
	}
	var value strings.Builder
	for i, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(part.Value, "*?[\\") || i == 0 && strings.HasPrefix(part.Value, "~") {
				return "", false
			}
			value.WriteString(part.Value)
		case *syntax.SglQuoted:
			value.WriteString(part.Value)
		default:
			return "", false
		}
	}
	return value.String(), true
}

// vcsStderrRedirect is stderr discarded or merged into the output the host
// already aggregates.
func vcsStderrRedirect(redirect *syntax.Redirect) bool {
	if redirect.N == nil || redirect.N.Value != "2" {
		return false
	}
	target, literal := shellsyntax.Literal(redirect.Word)
	return literal && (redirect.Op == syntax.RdrOut && target == "/dev/null" || redirect.Op == syntax.DplOut && target == "1")
}

// vcsHeredocBody is a heredoc's literal body. An unquoted delimiter allows
// expansions, so its body must be plain text without escapes.
func vcsHeredocBody(redirect *syntax.Redirect) (string, bool) {
	if redirect.Hdoc == nil || redirect.Word == nil {
		return "", false
	}
	var body strings.Builder
	for _, part := range redirect.Hdoc.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		body.WriteString(lit.Value)
	}
	quoted := slices.ContainsFunc(redirect.Word.Parts, func(part syntax.WordPart) bool {
		switch part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		}
		return false
	})
	if !quoted && strings.ContainsAny(body.String(), "\\`$") {
		return "", false
	}
	return body.String(), true
}

// vcsCatSubstitution reads the `"$(cat <<'EOF' … EOF)"` message idiom as the
// literal text it substitutes, without running cat.
func vcsCatSubstitution(word *syntax.Word) (string, bool) {
	parts := word.Parts
	if len(parts) == 1 {
		if quoted, ok := parts[0].(*syntax.DblQuoted); ok {
			parts = quoted.Parts
		}
	}
	if len(parts) != 1 {
		return "", false
	}
	substitution, ok := parts[0].(*syntax.CmdSubst)
	if !ok || len(substitution.Stmts) != 1 {
		return "", false
	}
	statement := substitution.Stmts[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 || len(call.Args) != 1 || len(statement.Redirs) != 1 ||
		statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
		return "", false
	}
	if name, literal := shellsyntax.Literal(call.Args[0]); !literal || name != "cat" {
		return "", false
	}
	redirect := statement.Redirs[0]
	if redirect.N != nil || redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc {
		return "", false
	}
	body, ok := vcsHeredocBody(redirect)
	// Command substitution drops trailing newlines.
	return strings.TrimRight(body, "\n"), ok
}

// vcsDisplay is an operation paragraph: the verb, a heading span, path spans
// after "in", then the source.
func vcsDisplay(verb, heading string, paths []string, source string) string {
	parts := []string{verb}
	if heading != "" {
		parts = append(parts, markdownsyntax.QuoteCode(heading))
		if len(paths) > 0 {
			parts = append(parts, "in", vcsPathSpans(paths))
		}
	}
	return strings.Join(parts, " ") + " · " + source
}

func vcsPathSpans(paths []string) string {
	const shown = 3
	spans := make([]string, 0, shown+1)
	for _, path := range paths[:min(len(paths), shown)] {
		spans = append(spans, markdownsyntax.QuoteCode(path))
	}
	if len(paths) > shown {
		spans = append(spans, fmt.Sprintf("+%d more", len(paths)-shown))
	}
	return strings.Join(spans, " ")
}

// vcsGit recognizes git commit, add, diff, show, and status after the
// global options that leave their output unchanged.
func vcsGit(args []string, stdin *string) (vcsCall, bool) {
	source, dir := "git", ""
	for len(args) > 0 {
		if args[0] == "--no-pager" {
			args = args[1:]
		} else if args[0] == "-C" && len(args) > 1 && args[1] != "" {
			if dir == "" || filepath.IsAbs(args[1]) {
				dir = args[1]
			} else {
				// Do not clean: a preceding -C can traverse a symlink,
				// so the next relative directory must resolve after it.
				dir += string(filepath.Separator) + args[1]
			}
			source += " -C " + args[1]
			args = args[2:]
		} else {
			break
		}
	}
	if len(args) == 0 {
		return vcsCall{}, false
	}
	call, ok := vcsCall{}, false
	switch args[0] {
	case "commit":
		call, ok = vcsCommitCall(args[1:], stdin, source, true)
	case "add":
		if stdin == nil {
			call, ok = vcsGitAdd(args[1:], source)
		}
	case "diff", "show":
		if stdin == nil {
			call, ok = vcsGitDiff(args[0] == "show", args[1:], source)
		}
	case "status":
		if stdin == nil {
			call, ok = vcsGitStatus(args[1:], source)
		}
	}
	call.tool, call.dir = "git", dir
	return call, ok
}

// Commit options that change neither what a commit records as its message
// nor what its output reports.
var (
	gitCommitFlags = []string{
		"--amend", "--no-edit", "-a", "--all", "-s", "--signoff", "--no-signoff", "-n", "--no-verify", "--verify",
		"--allow-empty", "--allow-empty-message", "--no-gpg-sign", "-o", "--only", "-i", "--include",
		"--no-status", "--status", "--no-post-rewrite", "-v", "--verbose", "--reset-author", "-q", "--quiet", "-S", "--gpg-sign",
	}
	gitCommitValueFlags = []string{"--author=", "--date=", "--cleanup=", "--trailer=", "--gpg-sign=", "--untracked-files=", "--pathspec-from-file=", "--fixup=", "--squash="}
	gitCommitShortFlags = "asnqvoi"
)

// vcsCommitCall recognizes a git or svn commit. Its heading is the first
// line of a literal message; a message from a file or the existing commit
// is named by the output instead.
func vcsCommitCall(args []string, stdin *string, source string, git bool) (vcsCall, bool) {
	var messages, paths []string
	file, hint := "", ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case arg == "--":
			paths = append(paths, args[i+1:]...)
			i = len(args)
		case arg == "-m" || arg == "--message":
			message, ok := value()
			if !ok {
				return vcsCall{}, false
			}
			messages = append(messages, message)
		case strings.HasPrefix(arg, "--message="):
			messages = append(messages, strings.TrimPrefix(arg, "--message="))
		case arg == "-F" || arg == "--file":
			path, ok := value()
			if !ok {
				return vcsCall{}, false
			}
			file = path
		case strings.HasPrefix(arg, "--file="):
			file = strings.TrimPrefix(arg, "--file=")
		case git && arg == "--amend":
			hint = " --amend"
		case git && slices.Contains(gitCommitFlags, arg):
		case git && slices.ContainsFunc(gitCommitValueFlags, func(prefix string) bool { return strings.HasPrefix(arg, prefix) }):
			if strings.HasPrefix(arg, "--fixup=") || strings.HasPrefix(arg, "--squash=") {
				hint += " " + arg
			}
		case git && len(arg) > 1 && arg[0] == '-' && arg[1] != '-':
			// A short option cluster, such as -am MESSAGE.
			for j := 1; j < len(arg); j++ {
				switch c := arg[j]; {
				case strings.IndexByte(gitCommitShortFlags, c) >= 0:
				case c == 'm' || c == 'F':
					rest, ok := arg[j+1:], true
					if rest == "" {
						rest, ok = value()
					}
					if !ok {
						return vcsCall{}, false
					}
					if c == 'm' {
						messages = append(messages, rest)
					} else {
						file = rest
					}
					j = len(arg)
				default:
					return vcsCall{}, false
				}
			}
		case !git && (arg == "-q" || arg == "--quiet" || arg == "--keep-changelists" || arg == "--no-unlock" || arg == "--include-externals"):
		case !git && (arg == "--depth" || arg == "--encoding" || arg == "--changelist" || arg == "--cl"):
			if _, ok := value(); !ok {
				return vcsCall{}, false
			}
		case strings.HasPrefix(arg, "-"):
			return vcsCall{}, false
		default:
			paths = append(paths, arg)
		}
	}
	// Stdin serves only as the message file; otherwise it is unaccounted input.
	if (file == "-") != (stdin != nil) {
		return vcsCall{}, false
	}
	message := ""
	switch {
	case len(messages) > 0:
		message = messages[0]
	case stdin != nil:
		message = *stdin
	}
	subject := ""
	for line := range strings.SplitSeq(message, "\n") {
		if subject = strings.TrimSpace(line); subject != "" {
			break
		}
	}
	call := vcsCall{kind: vcsCommit, subject: subject}
	call.display = vcsDisplay("Commit", subject, paths, source+hint)
	if !git {
		call.tool = "svn"
	}
	return call, true
}

// vcsGitAdd recognizes staging. Interactive and dry-run forms stay Run.
func vcsGitAdd(args []string, source string) (vcsCall, bool) {
	var paths, flags []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			paths = append(paths, args[i+1:]...)
			i = len(args)
		case arg == "-A" || arg == "--all" || arg == "-u" || arg == "--update" || arg == "--no-all" || arg == "--ignore-removal":
			flags = append(flags, arg)
		case arg == "-f" || arg == "--force" || arg == "-v" || arg == "--verbose" || arg == "--renormalize" || arg == "--sparse" || arg == "-N" || arg == "--intent-to-add":
		case strings.HasPrefix(arg, "-"):
			return vcsCall{}, false
		default:
			paths = append(paths, arg)
		}
	}
	if len(flags) > 0 {
		source += " add " + strings.Join(flags, " ")
	}
	if len(paths) == 0 {
		if len(flags) == 0 {
			return vcsCall{}, false // Stages nothing.
		}
		return vcsCall{kind: vcsStage, display: "Stage · " + source}, true
	}
	return vcsCall{kind: vcsStage, display: "Stage " + vcsPathSpans(paths) + " · " + source}, true
}

// Diff output forms that summarize files rather than print patches.
var gitStatFormats = map[string]string{"--stat": "stat", "--numstat": "numstat", "--shortstat": "shortstat", "--name-only": "name-only", "--name-status": "name-status"}

// vcsGitDiff recognizes git diff and show: a patch, a stat form, or a
// whitespace check. Options that write files or run external diff programs
// stay Run.
func vcsGitDiff(show bool, args []string, source string) (vcsCall, bool) {
	var revisions, paths []string
	cached, check, format := false, false, ""
	copies, summary := false, false
	for i, arg := range args {
		if arg == "--" {
			paths = append(paths, args[i+1:]...)
			break
		}
		name, _, _ := strings.Cut(arg, "=")
		switch {
		case !show && (arg == "--cached" || arg == "--staged"):
			cached = true
		case arg == "--check":
			check = true
		case arg == "-C" || arg == "--find-copies" || arg == "--find-copies-harder":
			copies = true
		case arg == "--summary":
			summary = true
		case gitStatFormats[name] != "" && (name == arg || name == "--stat"):
			if format != "" && format != gitStatFormats[name] {
				return vcsCall{}, false
			}
			format = gitStatFormats[name]
		case name == "--output" || name == "--ext-diff" || name == "--textconv" || name == "--dirstat" || name == "-X" ||
			name == "--compact-summary" || name == "--raw" || name == "-z" || strings.HasPrefix(arg, "--output"):
			return vcsCall{}, false
		case slices.Contains([]string{"-p", "--patch", "-u", "-s", "--no-patch", "--no-renames", "-M", "--find-renames", "-w", "-b", "--ignore-all-space", "--ignore-space-change", "--ignore-space-at-eol", "--ignore-cr-at-eol", "--ignore-blank-lines", "--minimal", "--patience", "--histogram", "--no-ext-diff", "--no-textconv", "--no-color", "--exit-code", "--quiet", "--relative", "--binary"}, arg):
		case strings.HasPrefix(arg, "--color=") && slices.Contains([]string{"--color=always", "--color=never", "--color=auto"}, arg):
		case strings.HasPrefix(arg, "-"):
			// Unknown output formats can turn metadata into apparent paths
			// or invalidate patch headers. Keep their literal Run/output.
			return vcsCall{}, false
		case show && strings.Contains(arg, ":"):
			return vcsCall{}, false // REV:PATH reads a file.
		default:
			revisions = append(revisions, arg)
		}
	}
	if copies && !summary && (format == "numstat" || format == "stat") {
		return vcsCall{}, false // These forms cannot distinguish a copy from a move.
	}
	scope := strings.Join(revisions, " ")
	switch {
	case show:
		scope = cmp.Or(scope, "HEAD")
		source += " show"
	case cached && scope != "":
		scope = "staged vs " + scope
	case cached:
		scope = "staged"
	case scope == "":
		scope = "working tree"
	}
	call := vcsCall{kind: vcsDiff, format: format}
	switch {
	case check:
		call.kind = vcsCheck
		if !show {
			source += " diff"
		}
		source += " --check"
		call.display = vcsDisplay("Check", scope, paths, source)
		return call, true
	case format != "":
		call.kind = vcsStat
		source += " --" + format
	}
	call.display = vcsDisplay("Diff", scope, paths, source)
	return call, true
}

// vcsGitStatus recognizes git status in its long, short, and porcelain v1
// forms.
func vcsGitStatus(args []string, source string) (vcsCall, bool) {
	var paths []string
	for i, arg := range args {
		if arg == "--" {
			paths = append(paths, args[i+1:]...)
			break
		}
		name, _, _ := strings.Cut(arg, "=")
		switch {
		case arg == "--porcelain=v1" || slices.Contains([]string{"--short", "--branch", "--long", "--porcelain", "--untracked-files",
			"--ignored", "--ahead-behind", "--no-ahead-behind", "--renames", "--no-renames", "--find-renames", "--show-stash", "--ignore-submodules"}, name):
			if name == "--porcelain" && arg != name && arg != "--porcelain=v1" {
				return vcsCall{}, false
			}
		case len(arg) > 1 && arg[0] == '-' && arg[1] != '-':
			for j := 1; j < len(arg); j++ {
				if arg[j] == 'u' {
					break // -u takes its mode attached.
				}
				if arg[j] != 's' && arg[j] != 'b' {
					return vcsCall{}, false
				}
			}
		case strings.HasPrefix(arg, "-"):
			return vcsCall{}, false
		default:
			paths = append(paths, arg)
		}
	}
	return vcsCall{kind: vcsStatus, display: vcsDisplay("Status", "working tree", paths, source)}, true
}

// vcsSVN recognizes svn commit, diff, and status.
func vcsSVN(args []string, stdin *string) (vcsCall, bool) {
	if len(args) == 0 {
		return vcsCall{}, false
	}
	call, ok := vcsCall{}, false
	switch args[0] {
	case "commit", "ci":
		call, ok = vcsCommitCall(args[1:], stdin, "svn", false)
	case "diff", "di":
		if stdin == nil {
			call, ok = vcsSVNDiff(args[1:])
		}
	case "status", "stat", "st":
		if stdin == nil {
			call, ok = vcsSVNStatus(args[1:])
		}
	}
	call.tool = "svn"
	return call, ok
}

func vcsSVNDiff(args []string) (vcsCall, bool) {
	var paths []string
	scope, summarize := "working copy", false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func(attached string) (string, bool) {
			if attached != "" {
				return attached, true
			}
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case arg == "--summarize":
			summarize = true
		case strings.HasPrefix(arg, "-c") || arg == "--change":
			change, ok := value(strings.TrimPrefix(strings.TrimPrefix(arg, "--change"), "-c"))
			if !ok {
				return vcsCall{}, false
			}
			scope = "r" + strings.TrimPrefix(change, "r")
		case strings.HasPrefix(arg, "-r") || arg == "--revision":
			revision, ok := value(strings.TrimPrefix(strings.TrimPrefix(arg, "--revision"), "-r"))
			if !ok {
				return vcsCall{}, false
			}
			scope = "r" + strings.TrimPrefix(revision, "r")
		case arg == "-x" || arg == "--extensions" || arg == "--depth":
			if _, ok := value(""); !ok {
				return vcsCall{}, false
			}
		case slices.Contains([]string{"--git", "--internal-diff", "--ignore-properties", "--properties-only", "--patch-compatible",
			"--notice-ancestry", "--show-copies-as-adds", "--no-diff-added", "--no-diff-deleted", "--ignore-externals"}, arg):
		case strings.HasPrefix(arg, "-"):
			return vcsCall{}, false
		default:
			paths = append(paths, arg)
		}
	}
	call, source := vcsCall{kind: vcsDiff}, "svn"
	if summarize {
		call.kind, call.format = vcsStat, "summarize"
		source += " --summarize"
	}
	call.display = vcsDisplay("Diff", scope, paths, source)
	return call, true
}

func vcsSVNStatus(args []string) (vcsCall, bool) {
	var paths []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-q" || arg == "--quiet" || arg == "--no-ignore" || arg == "--ignore-externals":
		case arg == "--depth" || arg == "--changelist" || arg == "--cl":
			if i++; i >= len(args) {
				return vcsCall{}, false
			}
		case strings.HasPrefix(arg, "-"):
			return vcsCall{}, false // -u and -v add columns; --xml changes the form.
		default:
			paths = append(paths, arg)
		}
	}
	return vcsCall{kind: vcsStatus, display: vcsDisplay("Status", "working copy", paths, "svn")}, true
}

// vcsMChanges recognizes mchanges patch and summary reads. Listings and
// history keep their own rows; revert and apply write files.
func vcsMChanges(args []string) (vcsCall, bool) {
	var ids, paths []string
	var source strings.Builder
	source.WriteString("mchanges")
	call := vcsCall{tool: "mchanges", kind: vcsDiff}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			paths = append(paths, args[i+1:]...)
			i = len(args)
		case arg == "--summary":
			call.kind, call.format = vcsStat, "summary"
			source.WriteString(" --summary")
		case arg == "--net":
			source.WriteString(" --net")
		case arg == "--mine":
		case arg == "--workspace" || arg == "--max-tokens":
			if i++; i >= len(args) {
				return vcsCall{}, false
			}
		case strings.HasPrefix(arg, "--workspace=") || strings.HasPrefix(arg, "--max-tokens="):
		case strings.HasPrefix(arg, "-") || i == 0 && (arg == "revert" || arg == "apply"):
			return vcsCall{}, false
		default:
			ids = append(ids, arg)
		}
	}
	call.display = vcsDisplay("Diff", cmp.Or(strings.Join(ids, " "), "mine"), paths, source.String())
	return call, true
}

// vcsChain is a statement's top-level && operands, in order.
func vcsChain(statement *syntax.Stmt) []*syntax.Stmt {
	if binary, ok := statement.Cmd.(*syntax.BinaryCmd); ok && binary.Op == syntax.AndStmt && len(statement.Redirs) == 0 &&
		!statement.Background && !statement.Negated && !statement.Coprocess && !statement.Disown {
		return append(vcsChain(binary.X), vcsChain(binary.Y)...)
	}
	return []*syntax.Stmt{statement}
}

// VCSOutputSource recognizes a tracked segment whose observed output has change rows.
func VCSOutputSource(source string) bool {
	program, err := syntax.NewParser().Parse(strings.NewReader(source), "")
	if err != nil || len(program.Stmts) != 1 {
		return false
	}
	call, ok := vcsStatement(program.Stmts[0])
	return ok && call.kind != vcsStage && call.kind != vcsCheck
}

// VCSStatement describes literal source without evaluating it or establishing execution.
func VCSStatement(statement *syntax.Stmt) (string, bool) {
	call, ok := vcsStatement(statement)
	return call.display, ok
}

// VCSPreservesDirectory reports a literal command that cannot change its parent shell cwd.
func VCSPreservesDirectory(source string) bool {
	program, err := syntax.NewParser().Parse(strings.NewReader(source), "")
	if err != nil || len(program.Stmts) != 1 {
		return false
	}
	_, ok := vcsStatement(program.Stmts[0])
	return ok
}
