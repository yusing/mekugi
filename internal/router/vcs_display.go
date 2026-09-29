package router

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"mvdan.cc/sh/v3/syntax"
)

// vcsCall is a literal git, svn, or mchanges invocation that Activity shows
// as a VCS operation rather than a Run preview. Recognition reads literal
// words only, plus a quoted heredoc message; nothing is expanded, evaluated,
// or run.
type vcsCall struct {
	tool    string // git, svn, or mchanges
	kind    string // One of the vcs* kinds.
	format  string // A stat read's output form, such as numstat.
	dir     string // git -C directory, as written.
	subject string // A commit's first message line, when literal.
	display string // The operation paragraph.
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
	if statement == nil || statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
		return vcsCall{}, false
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
			body, ok := shellCatLiteral(redirect.Word)
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

// vcsLiteral is a word's literal value. Beyond shellCatLiteral, it keeps
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
	if value, ok := shellCatLiteral(word); ok {
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
	target, literal := shellCatLiteral(redirect.Word)
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
	if name, literal := shellCatLiteral(call.Args[0]); !literal || name != "cat" {
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
		parts = append(parts, commentaryCode(heading))
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
		spans = append(spans, commentaryCode(path))
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
	source := "mchanges"
	call := vcsCall{tool: "mchanges", kind: vcsDiff}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			paths = append(paths, args[i+1:]...)
			i = len(args)
		case arg == "--summary":
			call.kind, call.format = vcsStat, "summary"
			source += " --summary"
		case arg == "--net":
			source += " --net"
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
	call.display = vcsDisplay("Diff", cmp.Or(strings.Join(ids, " "), "mine"), paths, source)
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

// vcsSegment reports a tracked segment whose output vcsOutputRows reads.
func vcsSegment(source string) bool {
	program, err := syntax.NewParser().Parse(strings.NewReader(source), "")
	if err != nil || len(program.Stmts) != 1 {
		return false
	}
	call, ok := vcsStatement(program.Stmts[0])
	return ok && call.kind != vcsStage && call.kind != vcsCheck
}

// commandOutputRows reads a successful command's output as change rows when
// it is a change history, VCS read, or commit, with the commit whose files
// complete them once read.
func commandOutputRows(item appServerItem) ([]activityui.ChangeRow, gitCommitKey) {
	if rows := mchangesOutputRows(item); rows != nil || item.AggregatedOutput == nil {
		return rows, gitCommitKey{}
	}
	return vcsOutputRows(item.Command, item.Cwd, *item.AggregatedOutput)
}

// vcsOutputRows reads a successful VCS command's output as change rows: a
// commit's recorded files, a diff's or stat's files and counts, or a
// status's paths. The output is the evidence, except that a git commit's
// per-file counts come from the commit object its output names, which
// commitChanges reads without blocking; the key names that object. Staging
// and checks, which print nothing on success, may precede a commit in one
// output; other lists interleave output, so they keep the plain tail, as do
// commands without a recognized row.
func vcsOutputRows(command, cwd, output string) ([]activityui.ChangeRow, gitCommitKey) {
	program, err := syntax.NewParser().Parse(strings.NewReader(appServerDisplayCommand(command)), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil, gitCommitKey{}
	}
	chain := vcsChain(program.Stmts[0])
	call, ok := vcsStatement(chain[len(chain)-1])
	if !ok {
		return nil, gitCommitKey{}
	}
	for _, statement := range chain[:len(chain)-1] {
		if prior, ok := vcsStatement(statement); !ok || call.kind != vcsCommit || prior.kind != vcsStage && prior.kind != vcsCheck {
			return nil, gitCommitKey{}
		}
	}
	lines := strings.Split(strings.ReplaceAll(ansi.Strip(output), "\r\n", "\n"), "\n")
	switch {
	case call.kind == vcsCommit && call.tool == "git":
		return gitCommitRows(call, cwd, lines)
	case call.kind == vcsCommit:
		return svnCommitRows(lines), gitCommitKey{}
	case call.kind == vcsDiff:
		return patchRows(lines), gitCommitKey{}
	case call.kind == vcsStat && call.tool == "mchanges":
		return nil, gitCommitKey{} // mchangesOutputRows reads its summaries.
	case call.kind == vcsStat && call.format == "summarize":
		return svnSummaryRows(lines), gitCommitKey{}
	case call.kind == vcsStat:
		return gitStatRows(call.format, lines), gitCommitKey{}
	case call.kind == vcsStatus && call.tool == "git":
		return gitStatusRows(lines), gitCommitKey{}
	case call.kind == vcsStatus:
		return svnStatusRows(lines), gitCommitKey{}
	}
	return nil, gitCommitKey{}
}

var (
	gitCommitHead   = regexp.MustCompile(`^\[(.+) ([0-9a-f]{4,64})\] (.*)$`)
	gitChangedFiles = regexp.MustCompile(`^ (\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?$`)
	gitSummaryMode  = regexp.MustCompile(`^ (create|delete) mode \d+ (.+)$`)
	gitSummaryMove  = regexp.MustCompile(`^ (rename|copy) (.+) \(\d+%\)$`)
)

// gitCommitRows reads the commit a git commit's output names: its hash,
// branch, and totals, and the created, deleted, and moved files it lists,
// without counts. The key names the commit object whose files replace them
// once read; without a host cwd there is none.
func gitCommitRows(call vcsCall, cwd string, lines []string) ([]activityui.ChangeRow, gitCommitKey) {
	var head []string
	footer := activityui.ChangeRow{Footer: true}
	var fallback []activityui.ChangeRow
	for _, line := range lines {
		if match := gitCommitHead.FindStringSubmatch(line); match != nil && head == nil {
			head = match
			continue
		}
		if head == nil {
			continue // Hook output precedes the commit.
		}
		if match := gitChangedFiles.FindStringSubmatch(line); match != nil {
			footer.Note = match[1] + " files"
			if match[1] == "1" {
				footer.Note = "1 file"
			}
			footer.Added, _ = strconv.Atoi(match[2])
			footer.Removed, _ = strconv.Atoi(match[3])
		} else if row, ok := gitSummaryRow(line); ok {
			fallback = append(fallback, row)
		}
	}
	if head == nil {
		return nil, gitCommitKey{}
	}
	branch, _ := strings.CutSuffix(head[1], " (root-commit)")
	notes := []string{"on " + livediff.Safe(branch, false)}
	if call.subject == "" {
		notes = append([]string{livediff.Safe(head[3], false)}, notes...)
	}
	if footer.Note != "" {
		notes = append(notes, footer.Note)
	}
	footer.Label, footer.Note = head[2], strings.Join(notes, " · ")
	var key gitCommitKey
	if dir := vcsDirectory(cwd, call.dir); dir != "" {
		key = gitCommitKey{dir, head[2]}
	}
	return append(fallback, footer), key
}

// gitSummaryRow reads one --summary line: a created, deleted, or moved file.
func gitSummaryRow(line string) (activityui.ChangeRow, bool) {
	if match := gitSummaryMode.FindStringSubmatch(line); match != nil {
		verb := "Created"
		if match[1] == "delete" {
			verb = "Deleted"
		}
		return activityui.ChangeRow{Verb: verb, Label: vcsPath(match[2])}, true
	}
	if match := gitSummaryMove.FindStringSubmatch(line); match != nil {
		from, to := gitRenamePaths(match[2])
		row := activityui.ChangeRow{Verb: "Moved", From: vcsPath(from), Label: vcsPath(to)}
		if match[1] == "copy" {
			row.Verb, row.From, row.Note = "Created", "", "copy of "+vcsPath(from)
		}
		return row, true
	}
	return activityui.ChangeRow{}, false
}

// vcsDirectory resolves a git -C directory against the command's own cwd.
// Without a host cwd there is no directory to read.
func vcsDirectory(cwd, dir string) string {
	switch {
	case filepath.IsAbs(dir):
		return dir
	case cwd == "":
		return ""
	}
	if dir == "" {
		return cwd
	}
	return cwd + string(filepath.Separator) + dir
}

// A prior unclassified shell command may change the shell's directory. Do
// not enrich a later commit against the invocation's initial cwd in that case.
// Literal VCS commands change only their own process directory, if any.
func vcsSegmentCwd(cwd string, prior []commandSegment) string {
	for _, segment := range prior {
		if segment.skipped {
			continue
		}
		program, err := syntax.NewParser().Parse(strings.NewReader(segment.source), "")
		if err != nil || len(program.Stmts) != 1 {
			return ""
		}
		if _, ok := vcsStatement(program.Stmts[0]); !ok {
			return ""
		}
	}
	return cwd
}

// Reading a commit object never touches the index or worktree. It runs off
// the UI goroutine, bounded in time, output, and concurrent readers, and a
// hash names immutable content, so each object's rows are cached.
const (
	gitShowTimeout    = 2 * time.Second
	gitShowWaitDelay  = 500 * time.Millisecond // After the timeout, for a child still holding stdout.
	gitShowBytes      = 4 << 20
	gitShowCacheLimit = 256
	gitShowReaders    = 2
)

// gitCommitKey names a commit object by the directory its command ran in
// and the hash its output printed.
type gitCommitKey struct{ dir, hash string }

// gitCommitRead is a commit object's files, or ok false when it could not
// be read.
type gitCommitRead struct {
	rows []activityui.ChangeRow
	ok   bool
}

var gitShowCache = struct {
	sync.Mutex
	reads  map[gitCommitKey]gitCommitRead
	order  []gitCommitKey        // Cached reads, oldest first, for eviction.
	wanted []gitCommitKey        // Requested and not yet started.
	queued map[gitCommitKey]bool // Wanted or being read.
}{reads: make(map[gitCommitKey]gitCommitRead), queued: make(map[gitCommitKey]bool)}

var gitShowSlots = make(chan struct{}, gitShowReaders)

// gitShowRun runs git; tests replace it.
var gitShowRun = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
	command.WaitDelay = gitShowWaitDelay
	var stdout vcsBoundedBuffer
	command.Stdout = &stdout
	err := command.Run()
	if stdout.overflow {
		return nil, errGitShowBound
	}
	if err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

var errGitShowBound = errors.New("git show output exceeds its bound")

type vcsBoundedBuffer struct {
	bytes.Buffer
	overflow bool
}

// Write fails past the bound, so git stops on a closed pipe rather than
// writing until its timeout.
func (b *vcsBoundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > gitShowBytes {
		b.overflow = true
		return 0, errGitShowBound
	}
	return b.Buffer.Write(p)
}

// commitChanges completes a git commit's output rows with the files and
// counts its object records, once a background read has cached them. Until
// then, and for an unreadable object, the output's rows stay; an unread
// object is requested for gitShowRequests.
func commitChanges(rows []activityui.ChangeRow, key gitCommitKey) []activityui.ChangeRow {
	if key.hash == "" || len(rows) == 0 {
		return rows
	}
	footer := rows[len(rows)-1]
	if !footer.Footer || footer.Label != key.hash {
		return rows
	}
	c := &gitShowCache
	c.Lock()
	defer c.Unlock()
	read, found := c.reads[key]
	if !found && !c.queued[key] {
		c.queued[key] = true
		c.wanted = append(c.wanted, key)
	}
	if !read.ok {
		return rows
	}
	return append(slices.Clone(read.rows), footer)
}

// gitShowRequests takes the commit objects requested since the last call.
func gitShowRequests() []gitCommitKey {
	c := &gitShowCache
	c.Lock()
	defer c.Unlock()
	wanted := c.wanted
	c.wanted = nil
	return wanted
}

// gitShowRead reads and caches one commit object's files, waiting for a
// reader slot. It reports whether the read succeeded; when ctx ends first,
// nothing is cached and the object may be requested again.
func gitShowRead(ctx context.Context, key gitCommitKey) bool {
	c := &gitShowCache
	select {
	case gitShowSlots <- struct{}{}:
		defer func() { <-gitShowSlots }()
	case <-ctx.Done():
		c.Lock()
		delete(c.queued, key)
		c.Unlock()
		return false
	}
	readCtx, cancel := context.WithTimeout(ctx, gitShowTimeout)
	defer cancel()
	output, err := gitShowRun(readCtx, key.dir, "-c", "core.quotepath=off", "show", "--no-color", "--no-ext-diff", "--no-textconv",
		"-M", "--numstat", "--summary", "--format=", key.hash+"^{commit}", "--")
	read := gitCommitRead{ok: err == nil}
	if read.ok {
		// The commit's own footer states its totals; a second would repeat them.
		read.rows = slices.DeleteFunc(gitStatRows("numstat", strings.Split(string(output), "\n")), func(row activityui.ChangeRow) bool { return row.Footer })
	}
	c.Lock()
	defer c.Unlock()
	delete(c.queued, key)
	if ctx.Err() != nil {
		return false
	}
	if len(c.order) >= gitShowCacheLimit {
		delete(c.reads, c.order[0])
		c.order = slices.Delete(c.order, 0, 1)
	}
	c.reads[key] = read
	c.order = append(c.order, key)
	return read.ok
}

var (
	svnCommitFile     = regexp.MustCompile(`^(Sending|Adding|Deleting|Replacing) +(?:\(bin\) +)?(.+)$`)
	svnCommitRevision = regexp.MustCompile(`^Committed revision (\d+)\.$`)
	svnCommitVerbs    = map[string]string{"Sending": "Edited", "Adding": "Created", "Deleting": "Deleted", "Replacing": "Edited"}
)

// svnCommitRows reads the files svn commit sent and the revision it made.
// svn reports no line counts.
func svnCommitRows(lines []string) []activityui.ChangeRow {
	var rows []activityui.ChangeRow
	revision := ""
	for _, line := range lines {
		if match := svnCommitFile.FindStringSubmatch(line); match != nil {
			row := activityui.ChangeRow{Verb: svnCommitVerbs[match[1]], Label: vcsPath(match[2])}
			if match[1] == "Replacing" {
				row.Note = "replaced"
			}
			rows = append(rows, row)
		} else if match := svnCommitRevision.FindStringSubmatch(line); match != nil {
			revision = match[1]
		}
	}
	if revision == "" {
		return nil // Nothing claims a commit.
	}
	return append(rows, activityui.ChangeRow{Footer: true, Label: "r" + revision})
}

// patchFile is one file of a unified diff.
type patchFile struct {
	from, to         string
	added, removed   int
	created, deleted bool
	copied           bool
	binary           bool
	git              bool // Paths carry a/ and b/ prefixes.
	old, hunks       bool // A --- header or hunk was read.
}

var patchHunk = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@`)

// patchRows reads a unified diff from git, svn, or mchanges as one row per
// file, counting lines within each hunk's stated extent so content that
// looks like a header is still counted as content.
func patchRows(lines []string) []activityui.ChangeRow {
	var files []*patchFile
	var file *patchFile
	start := func(git bool) {
		file = &patchFile{git: git}
		files = append(files, file)
	}
	oldLeft, newLeft, combined := 0, 0, false
	for _, line := range lines {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, "+"):
				file.added++
				newLeft--
				continue
			case strings.HasPrefix(line, "-"):
				file.removed++
				oldLeft--
				continue
			case strings.HasPrefix(line, " ") || line == "":
				oldLeft--
				newLeft--
				continue
			case strings.HasPrefix(line, `\`):
				continue
			}
			oldLeft, newLeft = 0, 0
		}
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "Index: ") {
			combined = false
		}
		switch {
		case combined:
		case strings.HasPrefix(line, "diff --git "):
			start(true)
			file.from, file.to = gitHeaderPaths(strings.TrimPrefix(line, "diff --git "))
		case strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined "):
			// A merge's combined diff has no per-parent counts; its
			// headers name no file of their own.
			file, combined = nil, true
		case strings.HasPrefix(line, "Index: "):
			start(false)
			file.from = vcsPath(strings.TrimPrefix(line, "Index: "))
			file.to = file.from
		case strings.HasPrefix(line, "--- "):
			if file == nil || file.old || file.hunks {
				start(false)
			}
			file.old = true
			file.from = patchPath(strings.TrimPrefix(line, "--- "), file.git)
			file.created = file.created || file.from == ""
		case file == nil:
		case strings.HasPrefix(line, "+++ "):
			file.to = patchPath(strings.TrimPrefix(line, "+++ "), file.git)
			file.deleted = file.deleted || file.to == ""
		case strings.HasPrefix(line, "new file mode"):
			file.created = true
		case strings.HasPrefix(line, "deleted file mode"):
			file.deleted = true
		case strings.HasPrefix(line, "copy from "):
			file.copied, file.from = true, vcsPath(strings.TrimPrefix(line, "copy from "))
		case strings.HasPrefix(line, "copy to "):
			file.copied, file.to = true, vcsPath(strings.TrimPrefix(line, "copy to "))
		case strings.HasPrefix(line, "rename from "):
			file.from = vcsPath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			file.to = vcsPath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch" || strings.HasPrefix(line, "Cannot display: file marked as a binary type"):
			file.binary = true
		default:
			if match := patchHunk.FindStringSubmatch(line); match != nil {
				file.hunks = true
				oldLeft, newLeft = patchExtent(match[1]), patchExtent(match[2])
			}
		}
	}
	var rows []activityui.ChangeRow
	index := make(map[string]int)
	for _, file := range files {
		row := activityui.ChangeRow{Verb: "Edited", Label: file.to, Added: file.added, Removed: file.removed}
		switch {
		case file.copied:
			row.Verb, row.Note = "Created", "copy of "+file.from
		case file.created && !file.deleted:
			row.Verb = "Created"
		case file.deleted || file.to == "":
			row.Verb, row.Label = "Deleted", file.from
		case file.from != "" && file.from != file.to:
			row.Verb, row.From = "Moved", file.from
		}
		if file.binary {
			if row.Note != "" {
				row.Note += " · "
			}
			row.Note += "binary"
		}
		if row.Label == "" {
			continue
		}
		// mchanges reads each record's patch; one path's records sum.
		if i, seen := index[row.Label]; seen {
			rows[i].Added += row.Added
			rows[i].Removed += row.Removed
			continue
		}
		index[row.Label] = len(rows)
		rows = append(rows, row)
	}
	return vcsTotals(rows)
}

// vcsTotals closes more rows than a group shows with their totals.
func vcsTotals(rows []activityui.ChangeRow) []activityui.ChangeRow {
	if len(rows) <= activityui.ChangeRowsShown {
		return rows
	}
	added, removed := activityui.ChangeTotals(rows)
	return append(rows, activityui.ChangeRow{Footer: true, Note: fmt.Sprintf("%d files", len(rows)), Added: added, Removed: removed})
}

func patchExtent(count string) int {
	if count == "" {
		return 1
	}
	n, _ := strconv.Atoi(count)
	return n
}

// gitHeaderPaths reads the paths of a `diff --git a/X b/Y` header. An
// unquoted header is ambiguous unless both sides name one path; later
// headers supply what it cannot.
func gitHeaderPaths(header string) (from, to string) {
	if strings.HasPrefix(header, `"`) {
		quoted, err := strconv.QuotedPrefix(header)
		if err != nil {
			return "", ""
		}
		return patchPath(quoted, true), patchPath(strings.TrimPrefix(header[len(quoted):], " "), true)
	}
	if half := (len(header) - 1) / 2; len(header)%2 == 1 && header[half] == ' ' && strings.HasPrefix(header, "a/") &&
		strings.HasPrefix(header[half+1:], "b/") && header[2:half] == header[half+3:] {
		path := vcsPath(header[2:half])
		return path, path
	}
	return "", ""
}

// patchPath reads a --- or +++ path: /dev/null is an absent side, a tab ends
// the path before a timestamp or revision, and quoted paths unquote.
func patchPath(path string, git bool) string {
	if strings.HasPrefix(path, `"`) {
		if unquoted, err := strconv.Unquote(path); err == nil {
			path = unquoted
		}
	} else if before, after, found := strings.Cut(path, "\t"); found {
		// svn marks an absent side (nonexistent) after the path.
		if path = before; after == "(nonexistent)" {
			return ""
		}
	}
	if path == "/dev/null" {
		return ""
	}
	if git {
		if trimmed, ok := strings.CutPrefix(path, "a/"); ok {
			path = trimmed
		} else if trimmed, ok := strings.CutPrefix(path, "b/"); ok {
			path = trimmed
		}
	}
	return vcsPath(path)
}

// vcsPath is a displayed path: unquoted when git quoted it, and sanitized.
func vcsPath(path string) string {
	if strings.HasPrefix(path, `"`) {
		if unquoted, err := strconv.Unquote(path); err == nil {
			path = unquoted
		}
	}
	return livediff.Safe(path, false)
}

// gitRenamePaths expands git's compact rename forms, `old => new` and
// `dir/{old => new}/file`, into both paths.
func gitRenamePaths(path string) (from, to string) {
	if open := strings.Index(path, "{"); open >= 0 {
		if end := strings.Index(path[open:], "}"); end >= 0 {
			if before, after, found := strings.Cut(path[open+1:open+end], " => "); found {
				prefix, suffix := path[:open], path[open+end+1:]
				join := func(middle string) string {
					return strings.ReplaceAll(prefix+middle+suffix, "//", "/")
				}
				return join(before), join(after)
			}
		}
	}
	if before, after, found := strings.Cut(path, " => "); found {
		return before, after
	}
	return path, path
}

var (
	gitNumstatRow    = regexp.MustCompile(`^(\d+|-)\t(\d+|-)\t(.+)$`)
	gitStatRow       = regexp.MustCompile(`^ (.+?) +\| +(?:(\d+)(?: ([+-]+))?|(Bin.*))$`)
	gitNameStatusRow = regexp.MustCompile(`^([ACDMRTUXB])(\d*)\t([^\t]+)(?:\t(.+))?$`)
	gitNameStatus    = map[string]string{"A": "Created", "C": "Created", "D": "Deleted", "M": "Edited", "T": "Edited", "R": "Moved", "U": "Conflict", "X": "?", "B": "Edited"}
	gitShowHeader    = regexp.MustCompile(`^(?:commit [0-9a-f]{7,}|Merge: |Author: |AuthorDate: |Commit: |CommitDate: |Date: )`)
)

// gitStatRows reads git's file summaries: --stat, --numstat (with any
// --summary lines), --shortstat, --name-status, and --name-only, including
// git show's commit header before them. A --stat graph scaled to fit its
// width does not split a file's count exactly, so its rows show only the
// total; the closing totals are exact either way.
func gitStatRows(format string, lines []string) []activityui.ChangeRow {
	var rows []activityui.ChangeRow
	var footer *activityui.ChangeRow
	verbs := make(map[string]activityui.ChangeRow)
	scaled := false
	for _, line := range lines {
		if match := gitChangedFiles.FindStringSubmatch(line); match != nil && format != "numstat" {
			footer = &activityui.ChangeRow{Footer: true, Note: match[1] + " files"}
			if match[1] == "1" {
				footer.Note = "1 file"
			}
			footer.Added, _ = strconv.Atoi(match[2])
			footer.Removed, _ = strconv.Atoi(match[3])
			continue
		}
		if format == "numstat" || format == "stat" {
			if row, ok := gitSummaryRow(line); ok {
				verbs[row.Label] = row
				continue
			}
		}
		switch format {
		case "numstat":
			if match := gitNumstatRow.FindStringSubmatch(line); match != nil {
				from, to := gitRenamePaths(match[3])
				row := activityui.ChangeRow{Verb: "Edited", Label: vcsPath(to)}
				if from != to {
					row.Verb, row.From = "Moved", vcsPath(from)
				}
				if match[1] == "-" {
					row.Note = "binary"
				} else {
					row.Added, _ = strconv.Atoi(match[1])
					row.Removed, _ = strconv.Atoi(match[2])
				}
				rows = append(rows, row)
			}
		case "stat":
			match := gitStatRow.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			from, to := gitRenamePaths(match[1])
			row := activityui.ChangeRow{Verb: "Edited", Label: vcsPath(to)}
			if from != to {
				row.Verb, row.From = "Moved", vcsPath(from)
			}
			total, _ := strconv.Atoi(match[2])
			added, removed := strings.Count(match[3], "+"), strings.Count(match[3], "-")
			switch {
			case match[4] != "":
				row.Note = "binary"
			case added+removed != total:
				scaled = true
				row.Note = vcsLines(total)
			default:
				row.Added, row.Removed = added, removed
			}
			rows = append(rows, row)
		case "name-status":
			match := gitNameStatusRow.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			row := activityui.ChangeRow{Verb: gitNameStatus[match[1]], Label: vcsPath(match[3])}
			if match[4] != "" {
				row.Label = vcsPath(match[4])
				if match[1] == "R" {
					row.From = vcsPath(match[3])
				} else {
					row.Note = "copy of " + vcsPath(match[3])
				}
			}
			rows = append(rows, row)
		case "name-only":
			if line != "" && !strings.HasPrefix(line, " ") && !gitShowHeader.MatchString(line) {
				rows = append(rows, activityui.ChangeRow{Label: vcsPath(line)})
			}
		}
	}
	if scaled {
		// One scaled row means the whole graph was scaled.
		for i := range rows {
			if rows[i].Note == "" && rows[i].Added+rows[i].Removed > 0 {
				rows[i].Note = vcsLines(rows[i].Added + rows[i].Removed)
				rows[i].Added, rows[i].Removed = 0, 0
			}
		}
	}
	for i, row := range rows {
		if summary, found := verbs[row.Label]; found {
			rows[i].Verb, rows[i].From = summary.Verb, summary.From
			if summary.Note != "" {
				rows[i].Note = summary.Note
				if row.Note != "" {
					rows[i].Note += " · " + row.Note
				}
			}
		}
	}
	if footer != nil {
		return append(rows, *footer)
	}
	return vcsTotals(rows)
}

func vcsLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

var (
	svnSummaryRow   = regexp.MustCompile(`^([ ADM])([ M]) +(.+)$`)
	svnSummaryVerbs = map[byte]string{'A': "Created", 'D': "Deleted", 'M': "Edited", ' ': "Edited"}
)

// svnSummaryRows reads svn diff --summarize: each path's item and property
// status, without line counts.
func svnSummaryRows(lines []string) []activityui.ChangeRow {
	var rows []activityui.ChangeRow
	for _, line := range lines {
		match := svnSummaryRow.FindStringSubmatch(line)
		if match == nil || match[1] == " " && match[2] == " " {
			continue
		}
		row := activityui.ChangeRow{Verb: svnSummaryVerbs[match[1][0]], Label: vcsPath(match[3])}
		if match[1] == " " {
			row.Note = "properties"
		}
		rows = append(rows, row)
	}
	return rows
}

var (
	gitStatusEntry    = regexp.MustCompile(`^\t(new file|modified|deleted|renamed|copied|typechange|both modified|both added|both deleted|added by us|added by them|deleted by us|deleted by them):\s+(.+?)(?: \((?:new commits|modified content|untracked content)(?:, [a-z ]+)*\))?$`)
	gitStatusLetters  = map[string]string{"new file": "A", "modified": "M", "deleted": "D", "renamed": "R", "copied": "C", "typechange": "T"}
	gitStatusMerge    = map[string]string{"both modified": "UU", "both added": "AA", "both deleted": "DD", "added by us": "AU", "added by them": "UA", "deleted by us": "DU", "deleted by them": "UD"}
	gitStatusSections = map[string]string{"Changes to be committed:": "staged", "Changes not staged for commit:": "unstaged",
		"Untracked files:": "untracked", "Unmerged paths:": "unmerged", "Ignored files:": "ignored"}
	gitStatusShort   = regexp.MustCompile(`^([ MTADRCU?!])([ MTADRCU?!]) (.+)$`)
	gitStatusHeader  = regexp.MustCompile(`^## (?:No commits yet on |Initial commit on )?(.+?)(?:\.\.\.\S+)?(?: \[(?:ahead (\d+))?(?:, )?(?:behind (\d+))?\])?$`)
	gitStatusAhead   = regexp.MustCompile(`^Your branch is ahead of .+ by (\d+) commits?\.$`)
	gitStatusBehind  = regexp.MustCompile(`^Your branch is behind .+ by (\d+) commits?, `)
	gitStatusDiverge = regexp.MustCompile(`^and have (\d+) and (\d+) different commits each`)
)

// gitStatusRows reads git status output, long or short, as two-cell status
// rows closed by the branch and its distance from upstream.
func gitStatusRows(lines []string) []activityui.ChangeRow {
	type entry struct{ x, y byte }
	var rows []activityui.ChangeRow
	codes := make(map[string]*entry)
	add := func(path string, x, y byte) {
		from, to, moved := strings.Cut(path, " -> ")
		if !moved {
			to = from
		}
		label := vcsPath(to)
		if code, seen := codes[label]; seen {
			code.x, code.y = max(code.x, x), max(code.y, y)
			return
		}
		codes[label] = &entry{x, y}
		row := activityui.ChangeRow{Label: label}
		if moved {
			row.From = vcsPath(from)
		}
		rows = append(rows, row)
	}
	branch, ahead, behind, clean, section, long := "", "", "", false, "", false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "On branch "):
			branch, long = strings.TrimPrefix(line, "On branch "), true
		case strings.HasPrefix(line, "HEAD detached "):
			branch, long = line, true
		case strings.HasPrefix(line, "## "):
			if match := gitStatusHeader.FindStringSubmatch(line); match != nil {
				branch, ahead, behind = match[1], match[2], match[3]
			}
		case strings.HasPrefix(line, "nothing to commit, working tree clean") || line == "nothing to commit (working directory clean)":
			clean = true
		case gitStatusSections[line] != "":
			section, long = gitStatusSections[line], true
		case line == "":
		case strings.HasPrefix(line, "\t") && section != "":
			match := gitStatusEntry.FindStringSubmatch(line)
			switch {
			case section == "untracked":
				add(strings.TrimPrefix(line, "\t"), '?', '?')
			case section == "ignored":
				add(strings.TrimPrefix(line, "\t"), '!', '!')
			case match == nil:
			case section == "unmerged" && gitStatusMerge[match[1]] != "":
				code := gitStatusMerge[match[1]]
				add(match[2], code[0], code[1])
			case section == "staged" && gitStatusLetters[match[1]] != "":
				add(match[2], gitStatusLetters[match[1]][0], ' ')
			case section == "unstaged" && gitStatusLetters[match[1]] != "":
				add(match[2], ' ', gitStatusLetters[match[1]][0])
			}
		default:
			if match := gitStatusAhead.FindStringSubmatch(line); match != nil {
				ahead = match[1]
			} else if match := gitStatusBehind.FindStringSubmatch(line); match != nil {
				behind = match[1]
			} else if match := gitStatusDiverge.FindStringSubmatch(line); match != nil {
				ahead, behind = match[1], match[2]
			} else if match := gitStatusShort.FindStringSubmatch(line); match != nil && !long {
				add(match[3], match[1][0], match[2][0])
			}
		}
	}
	for i := range rows {
		code := codes[rows[i].Label]
		rows[i].Code = string([]byte{code.x, code.y})
	}
	if branch == "" && !clean {
		return rows
	}
	var notes []string
	if ahead != "" && ahead != "0" {
		notes = append(notes, "↑"+ahead)
	}
	if behind != "" && behind != "0" {
		notes = append(notes, "↓"+behind)
	}
	note := strings.Join(notes, " ")
	if clean && len(rows) == 0 {
		note = strings.TrimSpace(note + " · clean")
		note = strings.TrimPrefix(note, "· ")
	}
	return append(rows, activityui.ChangeRow{Footer: true, Label: livediff.Safe(branch, false), Note: note})
}

var svnStatusRow = regexp.MustCompile(`^([ ACDIMRX?!~])([ CM])[ L][ +][ SX][ KOTB][ C] (.+)$`)

// svnStatusRows reads svn status: each path's item and property state.
func svnStatusRows(lines []string) []activityui.ChangeRow {
	var rows []activityui.ChangeRow
	for _, line := range lines {
		if match := svnStatusRow.FindStringSubmatch(line); match != nil && match[1]+match[2] != "  " {
			rows = append(rows, activityui.ChangeRow{Code: match[1] + match[2], Label: vcsPath(match[3])})
		}
	}
	return rows
}

// startCommitReads reads requested commit objects off the UI goroutine.
// Each commit read arrives on commitReads for commitRead.
func (u *appServerUI) startCommitReads() {
	keys := gitShowRequests()
	if len(keys) == 0 {
		return
	}
	if u.commitReads == nil {
		u.commitReads = make(chan gitCommitKey, gitShowCacheLimit)
	}
	ctx, reads := u.ctx, u.commitReads
	if ctx == nil {
		ctx = context.Background()
	}
	for _, key := range keys {
		go func() {
			if gitShowRead(ctx, key) {
				select {
				case reads <- key:
				case <-ctx.Done():
				}
			}
		}()
	}
}

// commitRead completes the rows of commits awaiting the object key names.
func (u *appServerUI) commitRead(key gitCommitKey) bool {
	main := u.view.resolveCommit(key)
	return u.agents.resolveCommit(key) || main
}

// resolveCommit completes, in place, the rows of entries awaiting commit
// key; the rows' blocks otherwise keep their state.
func (v *liveActivityView) resolveCommit(key gitCommitKey) bool {
	if v == nil {
		return false
	}
	changed := false
	for i, entry := range v.entries {
		if entry.native == nil || entry.native.commit != key &&
			!slices.ContainsFunc(entry.native.segments, func(segment commandSegment) bool { return segment.commit == key }) {
			continue
		}
		for j := range v.blocks[i] {
			v.blocks[i][j].Changes = commitChanges(v.blocks[i][j].Changes, key)
		}
		changed = true
	}
	if changed {
		v.runs = nil
	}
	return changed
}
