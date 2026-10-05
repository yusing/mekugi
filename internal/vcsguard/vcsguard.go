// Package vcsguard decides which version-control invocations publish to a
// remote, and defines the approval channel between a guarded command and the
// router. The helper that intercepts these commands and the router that asks
// the user share this package, so both agree on the wire format.
//
// The classification covers remote writes only: pushes of any ref or tag to
// any remote, Subversion commits and repository-side operations, and hosting
// mutations through gh. Local history edits such as commit, merge or rebase
// are not remote writes. Commands that cannot be classified fail closed.
package vcsguard

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// Timeout bounds how long a guarded command waits for an answer. A command
// that receives none is denied.
const Timeout = 5 * time.Minute

// Directory names the per-session directory of guarded command names, beside
// the frontend directory.
const Directory = "vcs-guard"

// Channel names the session-local Unix approval socket.
const Channel = "vcs-approval.sock"

// Tools are the command names the guard intercepts.
var Tools = []string{"git", "gh", "hg", "svn", "jj"}

// Message asks the router to approve one remote write.
type Message struct {
	Thread string   `json:"thread,omitempty"`
	Cwd    string   `json:"cwd,omitempty"`
	Argv   []string `json:"argv"`
}

// Reply answers a Message. Reason explains a denial to the command's stderr.
type Reply struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// MaxMessage bounds one approval request or reply.
const MaxMessage = 1 << 20

// Paths returns the guard directory and approval channel for a frontend
// directory.
func Paths(frontendDirectory string) (directory, channel string) {
	root := filepath.Dir(frontendDirectory)
	return filepath.Join(root, Directory), filepath.Join(root, Channel)
}

// ChannelOf returns the approval channel beside a guard directory.
func ChannelOf(directory string) string {
	return filepath.Join(filepath.Dir(directory), Channel)
}

// Lookup resolves a Git alias with the invocation's global options. It
// reports false when the name is not an alias.
type Lookup func(globals []string, name string) (string, bool)

// dynamic stands for a shell word whose value is known only at run time.
const dynamic = "\x00"

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
		return ghWrites(args)
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
		at, _ := executableWord(call.Args)
		if at < 0 || !slices.Contains(Tools, filepath.Base(literal(call.Args[at]))) {
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
					return dynamic
				}
				value.WriteString(lit.Value)
			}
		default:
			return dynamic
		}
	}
	return value.String()
}

func helps(args []string) bool {
	return slices.ContainsFunc(args, func(arg string) bool { return arg == "--help" || arg == "-h" })
}

// Git's global options that take a separate value.
var gitValueGlobals = []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--super-prefix", "--attr-source"}

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

func gitWrites(args []string, lookup Lookup, depth int) bool {
	var globals []string
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		if slices.Contains(gitValueGlobals, arg) {
			if i+1 == len(args) {
				return false
			}
			globals = append(globals, arg, args[i+1])
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}
		if arg == "--help" || arg == "-h" || arg == "--version" || arg == "-v" || arg == "--exec-path" || arg == "--html-path" || arg == "--man-path" || arg == "--info-path" {
			return false
		}
		globals = append(globals, arg)
	}
	if i == len(args) {
		return false
	}
	command, rest := args[i], args[i+1:]
	if command == dynamic {
		return true
	}
	if helps(rest) && command != "submodule" && command != "rebase" && command != "bisect" {
		return false
	}
	switch command {
	case "push", "send-email", "send-pack":
		return !dryRun(rest)
	case "subtree":
		return slices.Contains(rest, "push") || slices.Contains(rest, dynamic)
	case "svn":
		return len(rest) > 0 && slices.Contains([]string{"dcommit", "commit-diff", "set-tree", "branch", "tag", dynamic}, rest[0])
	case "p4":
		return len(rest) > 0 && (rest[0] == "submit" || rest[0] == dynamic)
	case "lfs":
		return len(rest) > 0 && slices.Contains([]string{"push", "lock", "unlock", dynamic}, rest[0])
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
			if ok && (script == dynamic || scriptWrites(script, lookup, depth+1)) {
				return true
			}
		}
		return false
	case "bisect":
		if len(rest) > 1 && rest[0] == "run" {
			words := rest[1:]
			at, _ := commandWord(words)
			return words[0] == dynamic || at >= 0 && slices.Contains(Tools, filepath.Base(words[at])) && writes(words[at:], lookup, depth+2)
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
	if word == dynamic {
		return `"$1"`
	}
	return shellsyntax.Quote(word)
}

// positionals returns the arguments that are not options. Option values are
// indistinguishable from operands without each command's option table, so
// callers only rely on the leading command words, which precede options in
// ordinary use, and treat anything unrecognized as a write.
func positionals(args []string) []string {
	var words []string
	for i, arg := range args {
		if arg == "--" {
			return append(words, args[i+1:]...)
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			words = append(words, arg)
		}
	}
	return words
}

// gh command groups that only change local configuration or credentials.
var ghLocal = []string{"alias", "auth", "config", "completion", "help", "version", "status", "search", "browse"}

// Verbs that read from a hosting service or only change local checkouts.
var ghReads = []string{
	"list", "ls", "view", "status", "diff", "checks", "checkout", "co", "clone", "download", "watch", "verify",
	"verify-asset", "get", "check", "field-list", "item-list", "logs", "trusted-root", "search", "set-default",
}

func ghWrites(args []string) bool {
	if len(args) == 0 || args[0] == "--version" || args[0] == "--help" || args[0] == "-h" {
		return false
	}
	words := positionals(args)
	if len(words) == 0 {
		return false
	}
	if slices.Contains(ghLocal, words[0]) {
		return false
	}
	if words[0] == "api" {
		return ghAPIWrites(args[slices.Index(args, "api")+1:])
	}
	if helps(args) {
		return false
	}
	if words[0] == "extension" || words[0] == "extensions" || words[0] == "ext" {
		return len(words) < 2 || !slices.Contains([]string{"list", "ls", "search", "browse"}, words[1])
	}
	if len(words) < 2 {
		return true
	}
	verb := words[1]
	// Nested groups name their verb next: gh repo deploy-key list.
	if slices.Contains([]string{"deploy-key", "autolink", "gitignore", "license", "ports"}, verb) && len(words) > 2 {
		verb = words[2]
	}
	if verb == "clone" {
		// gh label clone copies labels into the current repository.
		return words[0] != "repo" && words[0] != "gist"
	}
	return !slices.Contains(ghReads, verb)
}

// ghAPIWrites follows gh api: a request is a GET unless it names a method
// or carries parameters, and GraphQL always posts, so only its document
// distinguishes a query from a mutation.
func ghAPIWrites(args []string) bool {
	method := ""
	parameters := false
	var fields []string
	endpoint := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := ""
		hasValue := false
		name := arg
		if before, after, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(arg, "--") {
			name, value, hasValue = before, after, true
		} else if len(arg) > 2 && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune("XfFHqtp", rune(arg[1])) {
			name, value, hasValue = arg[:2], arg[2:], true
		}
		takes := slices.Contains([]string{"-X", "--method", "-f", "--raw-field", "-F", "--field", "--input", "-H", "--header", "-q", "--jq", "-t", "--template", "-p", "--preview", "--hostname", "--cache"}, name)
		if takes && !hasValue {
			if i+1 == len(args) {
				return true
			}
			value = args[i+1]
			i++
		}
		switch name {
		case "-X", "--method":
			method = strings.ToUpper(value)
		case "-f", "--raw-field", "-F", "--field":
			parameters = true
			fields = append(fields, value)
		case "--input":
			parameters = true
			fields = append(fields, "@"+value)
		default:
			if !takes && !strings.HasPrefix(arg, "-") && endpoint == "" {
				endpoint = arg
			}
		}
	}
	if endpoint == "" || endpoint == dynamic || method == dynamic {
		return true
	}
	if endpoint == "graphql" || endpoint == "/graphql" {
		if method != "" && method != "POST" && method != "GET" {
			return true
		}
		for _, field := range fields {
			_, value, _ := strings.Cut(field, "=")
			if strings.HasPrefix(field, "@") || strings.HasPrefix(value, "@") || strings.Contains(field, dynamic) || strings.Contains(strings.ToLower(value), "mutation") {
				return true
			}
		}
		return false
	}
	if method == "" {
		if parameters {
			return true
		}
		method = "GET"
	}
	return method != "GET" && method != "HEAD"
}

// Mercurial global options that take a separate value.
var hgValueGlobals = []string{"-R", "--repository", "--repo", "--cwd", "--config", "--encoding", "--encodingmode", "--color", "--pager"}

func hgWrites(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if slices.Contains(hgValueGlobals, arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == dynamic {
			return true
		}
		if helps(args[i+1:]) {
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
		if slices.Contains([]string{"--username", "--password", "--config-dir", "--config-option"}, arg) {
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
	if helps(rest) {
		return false
	}
	switch {
	case command == dynamic:
		return true
	case slices.Contains([]string{"commit", "ci", "import", "lock", "unlock"}, command):
		return true
	case slices.Contains(svnURLWrites, command):
		return slices.ContainsFunc(rest, func(arg string) bool {
			return arg == "--revprop" || arg == dynamic || strings.Contains(arg, "://") || strings.HasPrefix(arg, "^/")
		})
	}
	return false
}

// Jujutsu global options that take a separate value.
var jjValueGlobals = []string{"-R", "--repository", "--at-operation", "--at-op", "--color", "--config", "--config-toml", "--config-file"}

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
		if slices.Contains(jjValueGlobals, arg) {
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
		return len(words) > 1 && (words[1] == "push" || words[1] == dynamic) && !slices.Contains(args, "--dry-run")
	case "gerrit":
		return len(words) > 1 && (words[1] == "upload" || words[1] == dynamic) && !slices.Contains(args, "--dry-run")
	}
	return !slices.Contains(jjCommands, words[0])
}
