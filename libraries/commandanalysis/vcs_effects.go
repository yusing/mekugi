package commandanalysis

// Source: internal/vcsguard/vcsguard.go:148:663@[8dfc5600] shared VCS facts.

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
)

// VCSValueOption identifies global options whose next argument is a value.
func VCSValueOption(tool, option string) bool {
	switch tool {
	case "git":
		return slices.Contains([]string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--super-prefix", "--attr-source"}, option)
	case "hg":
		return slices.Contains([]string{"-R", "--repository", "--repo", "--cwd", "--config", "--encoding", "--encodingmode", "--color", "--pager"}, option)
	case "svn":
		return slices.Contains([]string{"--username", "--password", "--config-dir", "--config-option"}, option)
	case "jj":
		return slices.Contains([]string{"-R", "--repository", "--at-operation", "--at-op", "--color", "--config", "--config-toml", "--config-file"}, option)
	}
	return false
}

// HasHelpFlag recognizes explicit help requests among already-separated options.
func HasHelpFlag(args []string) bool {
	return slices.ContainsFunc(args, func(arg string) bool { return arg == "--help" || arg == "-h" })
}

// GitInvocation shares global-option parsing between approval and observation.
func GitInvocation(args []string) (globals []string, command string, rest []string) {
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		if VCSValueOption("git", arg) {
			if i+1 == len(args) {
				return globals, "", nil
			}
			globals = append(globals, arg, args[i+1])
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}
		if arg == "--help" || arg == "-h" || arg == "--version" || arg == "-v" || arg == "--exec-path" || arg == "--html-path" || arg == "--man-path" || arg == "--info-path" {
			return globals, "", nil
		}
		globals = append(globals, arg)
	}
	if i == len(args) {
		return globals, "", nil
	}
	return globals, args[i], args[i+1:]
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

// GitHubRemoteWrite reports possible GitHub API or repository mutations.
func GitHubRemoteWrite(args []string) bool {
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
	if HasHelpFlag(args) {
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
	if endpoint == "" || endpoint == shellsyntax.Dynamic || method == shellsyntax.Dynamic {
		return true
	}
	if endpoint == "graphql" || endpoint == "/graphql" {
		if method != "" && method != "POST" && method != "GET" {
			return true
		}
		for _, field := range fields {
			_, value, _ := strings.Cut(field, "=")
			if strings.HasPrefix(field, "@") || strings.HasPrefix(value, "@") || strings.Contains(field, shellsyntax.Dynamic) || strings.Contains(strings.ToLower(value), "mutation") {
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

var gitReadOnlySubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "rev-parse": true, "ls-files": true,
	"grep": true, "blame": true, "describe": true, "show-ref": true, "cat-file": true,
	"merge-base": true, "rev-list": true, "shortlog": true, "ls-tree": true, "for-each-ref": true,
	"name-rev": true, "whatchanged": true, "check-ignore": true, "check-attr": true, "var": true,
	"help": true, "version": true, "count-objects": true, "cherry": true, "range-diff": true,
}

// GitReadOnlyCommand reports built-in readers with no worktree mutation.
// Options that write output or run a pager need separate caller checks.
func GitReadOnlyCommand(command string) bool { return gitReadOnlySubcommands[command] }

// WorktreeWrites reports whether a VCS invocation may import, discard, or
// write out repository content. Unknown commands remain possible writers. This is effect
// attribution, independent of remote-write approval.
func WorktreeWrites(argv []string) bool {
	if len(argv) == 0 || !IsVCSTool(argv[0]) {
		return false
	}
	args := argv[1:]
	switch filepath.Base(argv[0]) {
	case "git":
		_, command, rest := GitInvocation(args)
		if slices.Contains([]string{"diff", "log", "show"}, command) && slices.ContainsFunc(rest, func(arg string) bool {
			return strings.HasPrefix(arg, "-o") || arg == "--output" || strings.HasPrefix(arg, "--output=")
		}) {
			return true
		}
		return command != "" && !GitReadOnlyCommand(command)
	case "gh":
		words := positionals(args)
		if len(words) > 1 && slices.Contains([]string{"checkout", "co", "clone"}, words[1]) {
			return true
		}
		return GitHubRemoteWrite(args)
	case "hg":
		command, rest := commandAfterGlobals(args, "hg")
		if command == "cat" {
			return slices.ContainsFunc(rest, func(arg string) bool {
				return strings.HasPrefix(arg, "-o") || arg == "--output" || strings.HasPrefix(arg, "--output=")
			})
		}
		if command == "branch" {
			return len(rest) > 0
		}
		return command != "" && !slices.Contains([]string{"status", "st", "log", "diff", "annotate", "blame", "root", "id", "identify", "paths"}, command)
	case "svn":
		command, _ := commandAfterGlobals(args, "svn")
		return command != "" && !SVNReadOnlyCommand(command)
	case "jj":
		command, _ := commandAfterGlobals(args, "jj")
		return command != "" && !slices.Contains([]string{"log", "diff", "status", "st", "show", "root"}, command)
	}
	return true
}

// SVNReadOnlyCommand identifies Subversion's built-in readers and aliases.
func SVNReadOnlyCommand(command string) bool {
	return slices.Contains([]string{"status", "st", "stat", "info", "log", "diff", "di", "cat", "list", "ls", "blame", "praise", "annotate", "ann", "propget", "pg", "proplist", "pl", "help"}, command)
}

// commandAfterGlobals reads the subcommand without treating global option
// values as command names. It never resolves or executes those values.
func commandAfterGlobals(args []string, tool string) (string, []string) {
	for i := 0; i < len(args); i++ {
		if VCSValueOption(tool, args[i]) {
			i++
			continue
		}
		if !strings.HasPrefix(args[i], "-") {
			return args[i], args[i+1:]
		}
	}
	return "", nil
}
