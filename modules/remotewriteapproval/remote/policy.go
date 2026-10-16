// Package remote classifies remote-write approval policy.
package remote

// Source: internal/vcsguard/vcsguard.go:64:419@[bff7c655] remote-write policy.
import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// Lookup resolves a Git alias with the invocation's global options. It
// reports false when the name is not an alias.
type Lookup func(globals []string, name string) (string, bool)

// Writes reports whether argv, whose first element names the tool, may
// write to a remote repository or hosting service.
func Writes(argv []string, lookup Lookup) bool {
	return writes(argv, lookup, 0)
}

func writes(argv []string, lookup Lookup, depth int) bool {
	if len(argv) == 0 || depth > 8 {
		return depth > 8
	}
	args := argv[1:]
	switch filepath.Base(argv[0]) {
	case "git":
		return gitWrites(args, lookup, depth)
	case "gh":
		return commandanalysis.GitHubRemoteWrite(args)
	case "hg":
		return hgWrites(args)
	case "svn":
		return svnWrites(args)
	case "jj":
		return jjWrites(args)
	}
	return false
}

// scriptWrites reports whether a shell script calls a guarded tool for a
// remote write. Git runs aliases, submodule foreach and rebase --exec with
// its own exec path first in PATH, where the guard cannot intercept a nested
// git, so their scripts are classified before Git starts. A script that does
// not parse fails closed.
func scriptWrites(script string, lookup Lookup, depth int) bool {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return true
	}
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || found {
			return !found
		}
		at, _ := shellsyntax.ExecutableWord(call.Args)
		if at < 0 || !commandanalysis.IsVCSTool(literal(call.Args[at])) {
			return true
		}
		words := make([]string, len(call.Args)-at)
		for i, word := range call.Args[at:] {
			words[i] = literal(word)
		}
		found = writes(words, lookup, depth+1)
		return !found
	})
	return found
}

// literal is a word's value when it has no expansions, and dynamic otherwise.
func literal(word *syntax.Word) string {
	var value strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			value.WriteString(part.Value)
		case *syntax.SglQuoted:
			value.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return shellsyntax.Dynamic
				}
				value.WriteString(lit.Value)
			}
		default:
			return shellsyntax.Dynamic
		}
	}
	return value.String()
}

// Git commands that cannot be aliases and push nothing themselves: Git runs
// a built-in or installed command before it considers an alias of the same
// name. Any other command may be an external program that pushes through
// Git's exec-path, unguarded, so it counts as a write unless it is an alias.
var gitCommands = []string{
	"add", "am", "annotate", "apply", "archive", "bisect", "blame", "branch", "bugreport", "bundle", "cat-file",
	"check-attr", "check-ignore", "check-mailmap", "check-ref-format", "checkout", "checkout-index", "cherry",
	"cherry-pick", "citool", "clean", "clone", "column", "commit", "commit-graph", "commit-tree", "config",
	"count-objects", "credential", "credential-cache", "credential-store", "describe", "diagnose", "diff",
	"diff-files", "diff-index", "diff-tree", "difftool", "fast-export", "fast-import", "fetch", "fetch-pack",
	"filter-branch", "fmt-merge-msg", "for-each-ref", "for-each-repo", "format-patch", "fsck", "fsck-objects", "gc",
	"get-tar-commit-id", "grep", "gui", "hash-object", "help", "hook", "index-pack", "init", "init-db", "instaweb",
	"interpret-trailers", "log", "ls-files", "ls-remote", "ls-tree", "mailinfo", "mailsplit", "maintenance", "merge",
	"merge-base", "merge-file", "merge-tree", "mergetool", "mktag", "mktree", "multi-pack-index", "mv", "name-rev",
	"notes", "pack-objects", "pack-redundant", "pack-refs", "patch-id", "prune", "prune-packed", "pull", "push",
	"range-diff", "read-tree", "rebase", "reflog", "refs", "remote", "repack", "replace", "replay", "request-pull",
	"rerere", "reset", "restore", "rev-list", "rev-parse", "revert", "rm", "send-email", "send-pack", "shortlog",
	"show", "show-branch", "show-index", "show-ref", "sparse-checkout", "stage", "stash", "status", "stripspace",
	"submodule", "subtree", "switch", "symbolic-ref", "tag", "unpack-file", "unpack-objects", "update-index",
	"update-ref", "update-server-info", "var", "verify-commit", "verify-pack", "verify-tag", "version",
	"whatchanged", "worktree", "write-tree", "svn", "p4", "lfs",
}

// dryRun reports whether the last dry-run option enables it.
func dryRun(args []string) bool {
	dry := false
	for _, arg := range args {
		switch arg {
		case "--dry-run", "-n":
			dry = true
		case "--no-dry-run":
			dry = false
		}
	}
	return dry
}

// Push permits interspersed options. Consume value operands before interpreting
// help or dry-run, including -o clusters and Git's long-option abbreviations.
func gitPushWrites(args []string) bool {
	dry := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg, "=")
			if attached {
				continue
			}
			switch name {
			case "--help":
				return false
			case "--dry-run":
				dry = true
			default:
				if strings.HasPrefix("--no-dry-run", name) {
					dry = false
				} else if slices.ContainsFunc([]string{"--push-option", "--repo", "--receive-pack", "--exec", "--recurse-submodules"}, func(option string) bool { return strings.HasPrefix(option, name) }) {
					i++
				}
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		for j := 1; j < len(arg); j++ {
			switch arg[j] {
			case 'h':
				return false
			case 'n':
				dry = true
			case 'o':
				if j == len(arg)-1 {
					i++
				}
				j = len(arg)
			}
		}
	}
	return !dry
}

func gitWrites(args []string, lookup Lookup, depth int) bool {
	globals, command, rest := commandanalysis.GitInvocation(args)
	if command == "" {
		return false
	}
	if command == shellsyntax.Dynamic {
		return true
	}
	if command == "push" {
		return gitPushWrites(rest)
	}
	if commandanalysis.HasHelpFlag(rest) && command != "submodule" && command != "rebase" && command != "bisect" {
		return false
	}
	switch command {
	case "send-email", "send-pack":
		return !dryRun(rest)
	case "subtree":
		return slices.Contains(rest, "push") || slices.Contains(rest, shellsyntax.Dynamic)
	case "svn":
		return len(rest) > 0 && slices.Contains([]string{"dcommit", "commit-diff", "set-tree", "branch", "tag", shellsyntax.Dynamic}, rest[0])
	case "p4":
		return len(rest) > 0 && (rest[0] == "submit" || rest[0] == shellsyntax.Dynamic)
	case "lfs":
		return len(rest) > 0 && slices.Contains([]string{"push", "lock", "unlock", shellsyntax.Dynamic}, rest[0])
	case "submodule":
		at := slices.Index(rest, "foreach")
		if at < 0 {
			return false
		}
		body := rest[at+1:]
		for len(body) > 0 && (body[0] == "--recursive" || body[0] == "-q" || body[0] == "--quiet") {
			body = body[1:]
		}
		return scriptWrites(strings.Join(body, " "), lookup, depth+1)
	case "rebase":
		for j, arg := range rest {
			script, ok := strings.CutPrefix(arg, "--exec=")
			if !ok && (arg == "-x" || arg == "--exec") && j+1 < len(rest) {
				script, ok = rest[j+1], true
			} else if !ok && strings.HasPrefix(arg, "-x") && len(arg) > 2 {
				script, ok = arg[2:], true
			}
			if ok && (script == shellsyntax.Dynamic || scriptWrites(script, lookup, depth+1)) {
				return true
			}
		}
		return false
	case "bisect":
		if len(rest) > 1 && rest[0] == "run" {
			words := rest[1:]
			at, _ := shellsyntax.CommandWord(words)
			return words[0] == shellsyntax.Dynamic || at >= 0 && commandanalysis.IsVCSTool(words[at]) && writes(words[at:], lookup, depth+2)
		}
		return false
	}
	if slices.Contains(gitCommands, command) {
		return false
	}
	if lookup == nil {
		return true
	}
	expansion, ok := lookup(globals, command)
	if !ok {
		return true // An external command, which may push unguarded.
	}
	if script, shell := strings.CutPrefix(expansion, "!"); shell {
		// Git appends the arguments as "$@", which a nested git receives as
		// its own words.
		for _, arg := range rest {
			script += " " + quote(arg)
		}
		return scriptWrites(script, lookup, depth+1)
	}
	words, ok := splitAlias(expansion)
	if !ok || len(words) == 0 {
		return true
	}
	expanded := slices.Concat(globals, words, rest)
	return gitWrites(expanded, lookup, depth+1)
}

// splitAlias splits a non-shell alias as Git's split_cmdline does: by
// whitespace, honoring single and double quotes and backslash escapes.
func splitAlias(text string) ([]string, bool) {
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range text {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, true
}

func quote(word string) string {
	if word == shellsyntax.Dynamic {
		return `"$1"`
	}
	return commandargs.Quote(word)
}

func hgWrites(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if commandanalysis.VCSValueOption("hg", arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == shellsyntax.Dynamic {
			return true
		}
		if commandanalysis.HasHelpFlag(args[i+1:]) {
			return false
		}
		// Mercurial accepts unambiguous command prefixes.
		for _, command := range []struct {
			name   string
			prefix int
		}{{"push", 3}, {"email", 2}, {"phabsend", 5}} {
			if len(arg) >= command.prefix && strings.HasPrefix(command.name, arg) {
				return true
			}
		}
		return false
	}
	return false
}

// Subversion commands that change the repository when an operand is a URL.
var svnURLWrites = []string{
	"copy", "cp", "move", "mv", "rename", "ren", "delete", "del", "remove", "rm", "mkdir",
	"propset", "pset", "ps", "propdel", "pdel", "pd", "propedit", "pedit", "pe",
}

func svnWrites(args []string) bool {
	command := ""
	at := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if commandanalysis.VCSValueOption("svn", arg) {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			command, at = arg, i
			break
		}
	}
	if command == "" {
		return false
	}
	rest := args[at+1:]
	if commandanalysis.HasHelpFlag(rest) {
		return false
	}
	switch {
	case command == shellsyntax.Dynamic:
		return true
	case slices.Contains([]string{"commit", "ci", "import", "lock", "unlock"}, command):
		return true
	case slices.Contains(svnURLWrites, command):
		return slices.ContainsFunc(rest, func(arg string) bool {
			return arg == "--revprop" || arg == shellsyntax.Dynamic || strings.Contains(arg, "://") || strings.HasPrefix(arg, "^/")
		})
	}
	return false
}

// Jujutsu's built-in commands; any other command may be an alias.
var jjCommands = []string{
	"abandon", "absorb", "backout", "bisect", "bookmark", "b", "branch", "cat", "checkout", "chmod", "commit", "ci",
	"config", "debug", "describe", "desc", "diff", "diffedit", "duplicate", "edit", "evolog", "obslog", "file",
	"files", "fix", "help", "interdiff", "log", "merge", "metaedit", "move", "new", "next", "operation", "op",
	"parallelize", "prev", "rebase", "redo", "resolve", "restore", "revert", "root", "run", "show", "sign",
	"simplify-parents", "sparse", "split", "squash", "st", "status", "tag", "undo", "unsign", "untrack", "util",
	"version", "workspace",
}

func jjWrites(args []string) bool {
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if commandanalysis.VCSValueOption("jj", arg) {
			i++
			continue
		}
		if arg == "--help" || arg == "-h" || arg == "--version" || arg == "-V" {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			words = append(words, arg)
		}
	}
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "git":
		return len(words) > 1 && (words[1] == "push" || words[1] == shellsyntax.Dynamic) && !slices.Contains(args, "--dry-run")
	case "gerrit":
		return len(words) > 1 && (words[1] == "upload" || words[1] == shellsyntax.Dynamic) && !slices.Contains(args, "--dry-run")
	}
	return !slices.Contains(jjCommands, words[0])
}
